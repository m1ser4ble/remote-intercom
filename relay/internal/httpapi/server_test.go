package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/remote-intercom/remote-intercom/relay/internal/auth"
	"github.com/remote-intercom/remote-intercom/relay/internal/channel"
	"github.com/remote-intercom/remote-intercom/relay/internal/protocol"
)

func TestConnectCreatesChannelAndReturnsBootstrapData(t *testing.T) {
	handler := newTestServer(t).Routes()

	resp := postConnect(t, handler, protocol.ConnectRequest{
		ChannelName: "ops",
		PIN:         "123456",
		DeviceName:  "alice-laptop",
	})

	if resp.Status != string(channel.StatusCreated) {
		t.Fatalf("status = %q, want %q", resp.Status, channel.StatusCreated)
	}
	if resp.ChannelID == "" {
		t.Fatal("expected channel id")
	}
	if resp.DeviceID == "" {
		t.Fatal("expected generated device id")
	}
	if resp.Token == "" {
		t.Fatal("expected token")
	}
	if resp.WSURL == "" {
		t.Fatal("expected ws url")
	}
}

func TestConnectSecondDeviceReturnsPendingApproval(t *testing.T) {
	handler := newTestServer(t).Routes()

	first := postConnect(t, handler, protocol.ConnectRequest{
		ChannelName: "ops",
		PIN:         "123456",
		DeviceName:  "alice-laptop",
		DeviceID:    "dev_alice",
	})
	second := postConnect(t, handler, protocol.ConnectRequest{
		ChannelName: "ops",
		PIN:         "123456",
		DeviceName:  "bob-laptop",
		DeviceID:    "dev_bob",
	})

	if second.Status != string(channel.StatusPending) {
		t.Fatalf("status = %q, want %q", second.Status, channel.StatusPending)
	}
	if second.ChannelID != first.ChannelID {
		t.Fatalf("channel id = %q, want %q", second.ChannelID, first.ChannelID)
	}
	if second.DeviceID != "dev_bob" {
		t.Fatalf("device id = %q, want dev_bob", second.DeviceID)
	}
	if second.JoinRequestID == "" {
		t.Fatal("expected join request id")
	}
	if second.Token == "" {
		t.Fatal("expected pending token")
	}
	if second.WSURL == "" {
		t.Fatal("expected ws url")
	}
}

func TestInstallScriptUsesForwardedProtoAndHost(t *testing.T) {
	t.Setenv("RELAY_INSTALL_TEMPLATE", "")
	handler := newTestServer(t).Routes()
	req := httptest.NewRequest(http.MethodGet, "/install.sh", nil)
	req.Host = "my-relay.example.com"
	req.Header.Set("X-Forwarded-Proto", "https")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `RELAY_HTTP_URL='https://my-relay.example.com'`) {
		t.Fatalf("install script missing https relay url: %s", body)
	}
	if !strings.Contains(body, `RELAY_WS_URL='wss://my-relay.example.com/ws'`) {
		t.Fatalf("install script missing wss relay url: %s", body)
	}
	if !strings.Contains(body, `"relayHttpUrl": "https://my-relay.example.com"`) {
		t.Fatalf("install script missing config http relay url: %s", body)
	}
	if !strings.Contains(body, `"relayWsUrl": "wss://my-relay.example.com/ws"`) {
		t.Fatalf("install script missing config ws relay url: %s", body)
	}
	if !strings.Contains(body, `.pi/remote-intercom/config.json`) {
		t.Fatalf("install script missing pi config path: %s", body)
	}
}

func TestInstallScriptUsesConfiguredPublicURLs(t *testing.T) {
	t.Setenv("RELAY_INSTALL_TEMPLATE", "")
	server := newTestServer(t)
	server.PublicHTTPURL = "https://public.example.com/a'b"
	server.PublicWSURL = "wss://public.example.com/ws"
	handler := server.Routes()
	req := httptest.NewRequest(http.MethodGet, "/install.sh", nil)
	req.Host = "ignored.example.com$(bad)"
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `RELAY_HTTP_URL='https://public.example.com/a'\''b'`) {
		t.Fatalf("install script missing quoted configured http relay url: %s", body)
	}
	if !strings.Contains(body, `RELAY_WS_URL='wss://public.example.com/ws'`) {
		t.Fatalf("install script missing configured ws relay url: %s", body)
	}
	if strings.Contains(body, "ignored.example.com") {
		t.Fatalf("install script reflected request host despite configured URLs: %s", body)
	}
}

func TestInstallScriptRejectsHostileHost(t *testing.T) {
	handler := newTestServer(t).Routes()
	req := httptest.NewRequest(http.MethodGet, "/install.sh", nil)
	req.Host = "relay.example.com$(touch /tmp/pwned)"
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assertJSONError(t, rec, http.StatusBadRequest)
}

func TestInvalidForwardedProtoFallsBackToHTTP(t *testing.T) {
	t.Setenv("RELAY_INSTALL_TEMPLATE", "")
	handler := newTestServer(t).Routes()
	req := httptest.NewRequest(http.MethodGet, "/install.sh", nil)
	req.Host = "my-relay.example.com"
	req.Header.Set("X-Forwarded-Proto", "javascript")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	body := rec.Body.String()
	if strings.Contains(body, "javascript://") {
		t.Fatalf("invalid forwarded proto was reflected: %s", body)
	}
	if !strings.Contains(body, `RELAY_HTTP_URL='http://my-relay.example.com'`) {
		t.Fatalf("install script missing fallback http relay url: %s", body)
	}
	if !strings.Contains(body, `RELAY_WS_URL='ws://my-relay.example.com/ws'`) {
		t.Fatalf("install script missing fallback ws relay url: %s", body)
	}
}

func TestInstallScriptUsesExplicitTemplateOverride(t *testing.T) {
	templatePath := filepath.Join(t.TempDir(), "install.tmpl")
	if err := os.WriteFile(templatePath, []byte("custom installer {{ .RelayHTTPURLJSON }} {{ .RelayWSURLJSON }}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RELAY_INSTALL_TEMPLATE", templatePath)
	handler := newTestServer(t).Routes()
	req := httptest.NewRequest(http.MethodGet, "/install.sh", nil)
	req.Host = "relay.example.com"
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body = %q", rec.Code, http.StatusOK, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `custom installer "http://relay.example.com" "ws://relay.example.com/ws"`) {
		t.Fatalf("install script did not use explicit override: %s", body)
	}
}

func TestInstallScriptFailsClosedWhenTemplateOverrideUnreadable(t *testing.T) {
	t.Setenv("RELAY_INSTALL_TEMPLATE", filepath.Join(t.TempDir(), "missing-install.tmpl"))
	handler := newTestServer(t).Routes()
	req := httptest.NewRequest(http.MethodGet, "/install.sh", nil)
	req.Host = "relay.example.com"
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assertJSONError(t, rec, http.StatusInternalServerError)
	if strings.Contains(rec.Body.String(), "Remote Intercom relay config") {
		t.Fatalf("install script fell back after unreadable override: %s", rec.Body.String())
	}
}

func TestInstallScriptIgnoresCurrentWorkingDirectoryTemplate(t *testing.T) {
	t.Setenv("RELAY_INSTALL_TEMPLATE", "")
	tempDir := t.TempDir()
	installDir := filepath.Join(tempDir, "install")
	if err := os.MkdirAll(installDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(installDir, "install.sh"), []byte("unexpected cwd template\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(oldWD); err != nil {
			t.Errorf("restore working directory: %v", err)
		}
	})
	if err := os.Chdir(tempDir); err != nil {
		t.Fatal(err)
	}

	handler := newTestServer(t).Routes()
	req := httptest.NewRequest(http.MethodGet, "/install.sh", nil)
	req.Host = "relay.example.com"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body = %q", rec.Code, http.StatusOK, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, "unexpected cwd template") {
		t.Fatalf("install script read template from current working directory: %s", body)
	}
	if !strings.Contains(body, "Remote Intercom relay config written") {
		t.Fatalf("install script did not use compiled default template: %s", body)
	}
}

func TestRenderedInstallScriptWritesConfigWithRestrictivePermissions(t *testing.T) {
	t.Setenv("RELAY_INSTALL_TEMPLATE", "")
	handler := newTestServer(t).Routes()
	req := httptest.NewRequest(http.MethodGet, "/install.sh", nil)
	req.Host = "relay.example.com:8443"
	req.Header.Set("X-Forwarded-Proto", "https")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body = %q", rec.Code, http.StatusOK, rec.Body.String())
	}

	tempDir := t.TempDir()
	configDir := filepath.Join(tempDir, "config")
	scriptPath := filepath.Join(tempDir, "install.sh")
	if err := os.WriteFile(scriptPath, rec.Body.Bytes(), 0o700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", scriptPath)
	cmd.Env = append(os.Environ(),
		"HOME="+filepath.Join(tempDir, "home"),
		"PI_REMOTE_INTERCOM_CONFIG_DIR="+configDir,
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("rendered install script failed: %v\n%s", err, output)
	}

	configPath := filepath.Join(configDir, "config.json")
	configBytes, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		RelayHTTPURL string `json:"relayHttpUrl"`
		RelayWSURL   string `json:"relayWsUrl"`
	}
	if err := json.Unmarshal(configBytes, &config); err != nil {
		t.Fatalf("config.json is not valid JSON: %v\n%s", err, configBytes)
	}
	if config.RelayHTTPURL != "https://relay.example.com:8443" {
		t.Fatalf("relayHttpUrl = %q, want https://relay.example.com:8443", config.RelayHTTPURL)
	}
	if config.RelayWSURL != "wss://relay.example.com:8443/ws" {
		t.Fatalf("relayWsUrl = %q, want wss://relay.example.com:8443/ws", config.RelayWSURL)
	}
	info, err := os.Stat(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got&0o077 != 0 {
		t.Fatalf("config file permissions = %v, want no group/world permissions", got)
	}
}

func TestConnectInvalidJSONReturnsJSONError(t *testing.T) {
	handler := newTestServer(t).Routes()
	rec := postRawConnect(t, handler, `{"channelName":`)

	assertJSONError(t, rec, http.StatusBadRequest)
}

func TestConnectUnknownFieldReturnsJSONError(t *testing.T) {
	handler := newTestServer(t).Routes()
	rec := postRawConnect(t, handler, `{"channelName":"ops","pin":"123456","deviceName":"alice-laptop","unknown":true}`)

	assertJSONError(t, rec, http.StatusBadRequest)
}

func TestConnectTrailingJSONReturnsJSONError(t *testing.T) {
	handler := newTestServer(t).Routes()
	rec := postRawConnect(t, handler, `{"channelName":"ops","pin":"123456","deviceName":"alice-laptop"}{}`)

	assertJSONError(t, rec, http.StatusBadRequest)
}

func TestConnectMissingFieldsReturnsJSONError(t *testing.T) {
	handler := newTestServer(t).Routes()
	rec := postRawConnect(t, handler, `{"channelName":"ops"}`)

	assertJSONError(t, rec, http.StatusBadRequest)
}

func TestHealthzAndVersion(t *testing.T) {
	handler := newTestServer(t).Routes()

	healthReq := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	healthRec := httptest.NewRecorder()
	handler.ServeHTTP(healthRec, healthReq)
	if healthRec.Code != http.StatusOK {
		t.Fatalf("health status = %d, want %d", healthRec.Code, http.StatusOK)
	}
	if healthRec.Body.String() != "ok\n" {
		t.Fatalf("health body = %q, want ok newline", healthRec.Body.String())
	}

	versionReq := httptest.NewRequest(http.MethodGet, "/version", nil)
	versionRec := httptest.NewRecorder()
	handler.ServeHTTP(versionRec, versionReq)
	if versionRec.Code != http.StatusOK {
		t.Fatalf("version status = %d, want %d", versionRec.Code, http.StatusOK)
	}
	var payload map[string]string
	if err := json.Unmarshal(versionRec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("version response is not json: %v", err)
	}
	if payload["version"] != "0.1.0" {
		t.Fatalf("version = %q, want 0.1.0", payload["version"])
	}
}

func newTestServer(t *testing.T) *Server {
	t.Helper()
	tokens, err := auth.NewTokenManager([]byte("01234567890123456789012345678901"), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return NewServer(channel.NewRegistry(), tokens, "0.1.0")
}

func postConnect(t *testing.T, handler http.Handler, request protocol.ConnectRequest) protocol.ConnectResponse {
	t.Helper()
	body, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	rec := postRawConnect(t, handler, string(body))
	if rec.Code != http.StatusOK {
		t.Fatalf("connect status = %d, body = %q", rec.Code, rec.Body.String())
	}
	var response protocol.ConnectResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("connect response is not json: %v", err)
	}
	return response
}

func postRawConnect(t *testing.T, handler http.Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/channels/connect", bytes.NewReader([]byte(body)))
	req.Host = "relay.test"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func assertJSONError(t *testing.T, rec *httptest.ResponseRecorder, status int) {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("status = %d, want %d, body = %q", rec.Code, status, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("content-type = %q, want application/json", got)
	}
	var payload map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("error response is not json: %v", err)
	}
	if payload["error"] == "" {
		t.Fatalf("error response missing error: %s", rec.Body.String())
	}
}
