package httpapi

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/remote-intercom/remote-intercom/relay/internal/auth"
	"github.com/remote-intercom/remote-intercom/relay/internal/channel"
	"github.com/remote-intercom/remote-intercom/relay/internal/protocol"
)

const defaultVersion = "0.1.0"

// Server exposes the relay bootstrap HTTP API.
type Server struct {
	Registry     *channel.Registry
	TokenManager *auth.TokenManager
	Version      string
}

func NewServer(registry *channel.Registry, tokenManager *auth.TokenManager, version string) *Server {
	if version == "" {
		version = defaultVersion
	}
	return &Server{Registry: registry, TokenManager: tokenManager, Version: version}
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("GET /version", s.handleVersion)
	mux.HandleFunc("GET /install.sh", s.handleInstall)
	mux.HandleFunc("POST /channels/connect", s.handleConnect)
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
	httpURL, wsURL := relayURLs(r)
	w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprintf(w, `#!/usr/bin/env sh
set -eu

RELAY_HTTP_URL=%q
RELAY_WS_URL=%q
export RELAY_HTTP_URL RELAY_WS_URL

echo "Remote Intercom relay configured: ${RELAY_HTTP_URL}"
`, httpURL, wsURL)
}

func (s *Server) handleConnect(w http.ResponseWriter, r *http.Request) {
	if s.Registry == nil {
		http.Error(w, "registry is not configured", http.StatusInternalServerError)
		return
	}
	if s.TokenManager == nil {
		http.Error(w, "token manager is not configured", http.StatusInternalServerError)
		return
	}

	var request protocol.ConnectRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		http.Error(w, "invalid connect request", http.StatusBadRequest)
		return
	}

	request.ChannelName = strings.TrimSpace(request.ChannelName)
	request.PIN = strings.TrimSpace(request.PIN)
	request.DeviceName = strings.TrimSpace(request.DeviceName)
	request.DeviceID = strings.TrimSpace(request.DeviceID)
	if request.ChannelName == "" || request.PIN == "" || request.DeviceName == "" {
		http.Error(w, "channelName, pin, and deviceName are required", http.StatusBadRequest)
		return
	}
	if request.DeviceID == "" {
		deviceID, err := generateDeviceID()
		if err != nil {
			http.Error(w, "could not generate device id", http.StatusInternalServerError)
			return
		}
		request.DeviceID = deviceID
	}

	result := s.Registry.Connect(request.ChannelName, request.PIN, request.DeviceID, request.DeviceName)
	if result.Channel == nil {
		http.Error(w, "channel connect failed", http.StatusInternalServerError)
		return
	}

	response := protocol.ConnectResponse{
		Status:    string(result.Status),
		ChannelID: result.Channel.ID,
		DeviceID:  request.DeviceID,
		WSURL:     wsURL(r),
	}

	var err error
	switch result.Status {
	case channel.StatusCreated, channel.StatusConnected:
		response.Token, err = s.TokenManager.IssueMember(response.ChannelID, response.DeviceID)
	case channel.StatusPending:
		if result.JoinRequest == nil {
			http.Error(w, "join request missing", http.StatusInternalServerError)
			return
		}
		response.JoinRequestID = result.JoinRequest.ID
		response.Token, err = s.TokenManager.IssuePending(response.ChannelID, response.DeviceID, response.JoinRequestID)
	default:
		http.Error(w, "unknown connect status", http.StatusInternalServerError)
		return
	}
	if err != nil {
		http.Error(w, "could not issue token", http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, response)
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func relayURLs(r *http.Request) (string, string) {
	proto := requestProto(r)
	host := requestHost(r)
	wsScheme := "ws"
	if proto == "https" {
		wsScheme = "wss"
	}
	return proto + "://" + host, wsScheme + "://" + host + "/ws"
}

func wsURL(r *http.Request) string {
	_, wsURL := relayURLs(r)
	return wsURL
}

func requestProto(r *http.Request) string {
	if forwarded := strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")); forwarded != "" {
		if comma := strings.Index(forwarded, ","); comma >= 0 {
			forwarded = forwarded[:comma]
		}
		return strings.ToLower(strings.TrimSpace(forwarded))
	}
	return "http"
}

func requestHost(r *http.Request) string {
	if r.Host != "" {
		return r.Host
	}
	return r.URL.Host
}

func generateDeviceID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return "dev_" + hex.EncodeToString(b[:]), nil
}
