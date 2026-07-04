package httpapi

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"text/template"
	"unicode"

	"github.com/remote-intercom/remote-intercom/relay/internal/auth"
	"github.com/remote-intercom/remote-intercom/relay/internal/channel"
	"github.com/remote-intercom/remote-intercom/relay/internal/protocol"
	wshub "github.com/remote-intercom/remote-intercom/relay/internal/ws"
)

const (
	defaultVersion   = "0.1.0"
	maxJSONBodyBytes = 64 * 1024
)

const defaultInstallTemplate = `#!/usr/bin/env sh
set -eu

RELAY_HTTP_URL={{ .RelayHTTPURLShell }}
RELAY_WS_URL={{ .RelayWSURLShell }}
export RELAY_HTTP_URL RELAY_WS_URL

CONFIG_DIR="${PI_REMOTE_INTERCOM_CONFIG_DIR:-${HOME}/.pi/remote-intercom}"
CONFIG_FILE="${CONFIG_DIR}/config.json"

mkdir -p "${CONFIG_DIR}"
umask 077
TMP_FILE="${CONFIG_FILE}.tmp.$$"

cat > "${TMP_FILE}" <<'EOF_CONFIG'
{
  "relayHttpUrl": {{ .RelayHTTPURLJSON }},
  "relayWsUrl": {{ .RelayWSURLJSON }}
}
EOF_CONFIG
mv "${TMP_FILE}" "${CONFIG_FILE}"

printf '%s\n' "Remote Intercom relay config written to ${CONFIG_FILE}"
printf '%s\n' "  HTTP: ${RELAY_HTTP_URL}"
printf '%s\n' "  WS:   ${RELAY_WS_URL}"

if command -v pi >/dev/null 2>&1; then
  PI_CMD=$(command -v pi)
  printf '%s\n' "Detected pi command at ${PI_CMD}."
else
  printf '%s\n' "pi command was not found on PATH. Install pi before loading the extension."
fi

cat <<'EOF_NEXT'

Next steps (MVP/local package):
  1. From a remote-intercom checkout, build the extension:
       cd extension && npm ci && npm run build
  2. Load/register the built extension with pi according to your pi extension workflow.
  3. Pass the relay settings from ~/.pi/remote-intercom/config.json to the extension when registering it.

The extension package is not published by this MVP installer, so this script only writes safe local configuration.
EOF_NEXT
`

// Server exposes the relay bootstrap HTTP API.
type Server struct {
	Registry      *channel.Registry
	TokenManager  *auth.TokenManager
	Hub           *wshub.Hub
	Version       string
	PublicHTTPURL string
	PublicWSURL   string
}

func NewServer(registry *channel.Registry, tokenManager *auth.TokenManager, version string) *Server {
	if version == "" {
		version = defaultVersion
	}
	return &Server{Registry: registry, TokenManager: tokenManager, Hub: wshub.NewHub(registry, tokenManager), Version: version}
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("GET /version", s.handleVersion)
	mux.HandleFunc("GET /install.sh", s.handleInstall)
	mux.HandleFunc("POST /channels/connect", s.handleConnect)
	if s.Hub != nil {
		mux.Handle("GET /ws", s.Hub)
	}
	return mux
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok\n"))
}

func (s *Server) handleVersion(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"version": s.Version})
}

func (s *Server) handleInstall(w http.ResponseWriter, r *http.Request) {
	httpURL, wsURL, err := s.relayURLs(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid relay URL")
		return
	}
	script, err := renderInstallScript(httpURL, wsURL)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not render installer")
		return
	}

	w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(script))
}

func (s *Server) handleConnect(w http.ResponseWriter, r *http.Request) {
	if s.Registry == nil {
		writeError(w, http.StatusInternalServerError, "registry is not configured")
		return
	}
	if s.TokenManager == nil {
		writeError(w, http.StatusInternalServerError, "token manager is not configured")
		return
	}

	var request protocol.ConnectRequest
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid connect request")
		return
	}

	request.ChannelName = strings.TrimSpace(request.ChannelName)
	request.PIN = strings.TrimSpace(request.PIN)
	request.DeviceName = strings.TrimSpace(request.DeviceName)
	request.DeviceID = strings.TrimSpace(request.DeviceID)
	if request.ChannelName == "" || request.PIN == "" || request.DeviceName == "" {
		writeError(w, http.StatusBadRequest, "channelName, pin, and deviceName are required")
		return
	}
	if request.DeviceID == "" {
		deviceID, err := generateDeviceID()
		if err != nil {
			writeError(w, http.StatusInternalServerError, "could not generate device id")
			return
		}
		request.DeviceID = deviceID
	}

	publicWSURL, err := s.wsURL(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid relay URL")
		return
	}

	result := s.Registry.Connect(request.ChannelName, request.PIN, request.DeviceID, request.DeviceName)
	if result.Channel == nil {
		writeError(w, http.StatusInternalServerError, "channel connect failed")
		return
	}

	response := protocol.ConnectResponse{
		Status:    string(result.Status),
		ChannelID: result.Channel.ID,
		DeviceID:  request.DeviceID,
		WSURL:     publicWSURL,
	}

	switch result.Status {
	case channel.StatusCreated, channel.StatusConnected:
		response.Token, err = s.TokenManager.IssueMember(response.ChannelID, response.DeviceID)
	case channel.StatusPending:
		if result.JoinRequest == nil {
			writeError(w, http.StatusInternalServerError, "join request missing")
			return
		}
		response.JoinRequestID = result.JoinRequest.ID
		response.Token, err = s.TokenManager.IssuePending(response.ChannelID, response.DeviceID, response.JoinRequestID)
	default:
		writeError(w, http.StatusInternalServerError, "unknown connect status")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not issue token")
		return
	}

	writeJSON(w, http.StatusOK, response)
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	body := http.MaxBytesReader(w, r.Body, maxJSONBodyBytes)
	decoder := json.NewDecoder(body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return err
	}

	var extra struct{}
	if err := decoder.Decode(&extra); err != nil {
		if errors.Is(err, io.EOF) {
			return nil
		}
		return err
	}
	return errors.New("request body contains multiple JSON values")
}

func (s *Server) relayURLs(r *http.Request) (string, string, error) {
	var httpURL string
	var wsURL string
	var err error

	if s.PublicHTTPURL != "" {
		httpURL, err = validatePublicURL(s.PublicHTTPURL, "http", "https")
		if err != nil {
			return "", "", err
		}
	}
	if s.PublicWSURL != "" {
		wsURL, err = validatePublicURL(s.PublicWSURL, "ws", "wss")
		if err != nil {
			return "", "", err
		}
	}
	if httpURL != "" && wsURL != "" {
		return httpURL, wsURL, nil
	}

	inferredHTTPURL, inferredWSURL, err := inferRelayURLs(r)
	if err != nil {
		return "", "", err
	}
	if httpURL == "" {
		httpURL = inferredHTTPURL
	}
	if wsURL == "" {
		wsURL = inferredWSURL
	}
	return httpURL, wsURL, nil
}

func (s *Server) wsURL(r *http.Request) (string, error) {
	if s.PublicWSURL != "" {
		return validatePublicURL(s.PublicWSURL, "ws", "wss")
	}
	_, wsURL, err := inferRelayURLs(r)
	return wsURL, err
}

func inferRelayURLs(r *http.Request) (string, string, error) {
	proto := requestProto(r)
	host, err := requestHost(r, proto)
	if err != nil {
		return "", "", err
	}
	wsScheme := "ws"
	if proto == "https" {
		wsScheme = "wss"
	}
	return proto + "://" + host, wsScheme + "://" + host + "/ws", nil
}

func requestProto(r *http.Request) string {
	forwarded := strings.TrimSpace(r.Header.Get("X-Forwarded-Proto"))
	if strings.EqualFold(forwarded, "https") {
		return "https"
	}
	if strings.EqualFold(forwarded, "http") {
		return "http"
	}
	return "http"
}

func requestHost(r *http.Request, scheme string) (string, error) {
	host := r.Host
	if host == "" {
		return "", errors.New("missing host")
	}
	for _, r := range host {
		if unsafeHostRune(r) {
			return "", errors.New("unsafe host")
		}
	}
	u, err := url.Parse(scheme + "://" + host)
	if err != nil {
		return "", err
	}
	if u.Scheme != scheme || u.Host == "" || u.Host != host || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("invalid host")
	}
	return host, nil
}

func unsafeHostRune(r rune) bool {
	if unicode.IsControl(r) || unicode.IsSpace(r) {
		return true
	}
	switch r {
	case '/', '\\', '$', '`', '\'', '"', ';', '&', '|', '(', ')', '<', '>':
		return true
	default:
		return false
	}
}

func validatePublicURL(raw string, allowedSchemes ...string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	if u.Scheme == "" || u.Host == "" {
		return "", errors.New("missing URL scheme or host")
	}
	for _, scheme := range allowedSchemes {
		if strings.EqualFold(u.Scheme, scheme) {
			return raw, nil
		}
	}
	return "", errors.New("invalid URL scheme")
}

func renderInstallScript(httpURL, wsURL string) (string, error) {
	templateContents, err := installTemplateContents()
	if err != nil {
		return "", err
	}
	tmpl, err := template.New("install.sh").Option("missingkey=error").Parse(templateContents)
	if err != nil {
		return "", err
	}

	data := map[string]string{
		"RelayHTTPURLShell": shellQuote(httpURL),
		"RelayWSURLShell":   shellQuote(wsURL),
		"RelayHTTPURLJSON":  jsonString(httpURL),
		"RelayWSURLJSON":    jsonString(wsURL),
	}
	var out bytes.Buffer
	if err := tmpl.Execute(&out, data); err != nil {
		return "", err
	}
	return out.String(), nil
}

func installTemplateContents() (string, error) {
	if configured := strings.TrimSpace(os.Getenv("RELAY_INSTALL_TEMPLATE")); configured != "" {
		content, err := os.ReadFile(configured)
		if err != nil {
			return "", err
		}
		return string(content), nil
	}
	return defaultInstallTemplate, nil
}

func jsonString(value string) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "\"\""
	}
	return string(encoded)
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func generateDeviceID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return "dev_" + hex.EncodeToString(b[:]), nil
}
