package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
	if !strings.Contains(body, `RELAY_HTTP_URL="https://my-relay.example.com"`) {
		t.Fatalf("install script missing https relay url: %s", body)
	}
	if !strings.Contains(body, `RELAY_WS_URL="wss://my-relay.example.com/ws"`) {
		t.Fatalf("install script missing wss relay url: %s", body)
	}
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
	req := httptest.NewRequest(http.MethodPost, "/channels/connect", bytes.NewReader(body))
	req.Host = "relay.test"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("connect status = %d, body = %q", rec.Code, rec.Body.String())
	}
	var response protocol.ConnectResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("connect response is not json: %v", err)
	}
	return response
}
