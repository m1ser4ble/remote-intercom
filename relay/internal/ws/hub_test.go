package ws_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/remote-intercom/remote-intercom/relay/internal/auth"
	"github.com/remote-intercom/remote-intercom/relay/internal/channel"
	"github.com/remote-intercom/remote-intercom/relay/internal/httpapi"
	"github.com/remote-intercom/remote-intercom/relay/internal/protocol"
)

func TestWebSocketJoinApprovalAndMessageRouting(t *testing.T) {
	server := newRelayServer(t)

	alice := postConnect(t, server, protocol.ConnectRequest{
		ChannelName: "ops",
		PIN:         "123456",
		DeviceName:  "alice-laptop",
		DeviceID:    "dev_alice",
	})
	aliceWS := dialWSWithAuthorization(t, alice.WSURL, alice.Token)

	bob := postConnect(t, server, protocol.ConnectRequest{
		ChannelName: "ops",
		PIN:         "123456",
		DeviceName:  "bob-laptop",
		DeviceID:    "dev_bob",
	})
	bobWS := dialWSWithTokenQuery(t, bob.WSURL, bob.Token)

	joinRequest := readEvent(t, aliceWS)
	if joinRequest.Type != "join.request" {
		t.Fatalf("event type = %q, want join.request", joinRequest.Type)
	}
	if joinRequest.From != "dev_bob" || joinRequest.To != "dev_alice" {
		t.Fatalf("join request route = %s -> %s, want dev_bob -> dev_alice", joinRequest.From, joinRequest.To)
	}
	if got := stringPayload(t, joinRequest.Payload, "joinRequestId"); got != bob.JoinRequestID {
		t.Fatalf("joinRequestId = %q, want %q", got, bob.JoinRequestID)
	}

	writeEvent(t, aliceWS, protocol.Event{
		ID:   "approve-1",
		Type: "join.approve",
		Payload: map[string]any{
			"joinRequestId": bob.JoinRequestID,
		},
	})

	approved := readEvent(t, bobWS)
	if approved.Type != "join.approved" {
		t.Fatalf("event type = %q, want join.approved", approved.Type)
	}
	if approved.ReplyTo != "approve-1" {
		t.Fatalf("replyTo = %q, want approve-1", approved.ReplyTo)
	}

	writeEvent(t, bobWS, protocol.Event{
		ID:   "send-joined",
		Type: "message.send",
		To:   "dev_alice",
		Payload: map[string]any{
			"text": "hello alice",
		},
	})

	fromJoinedMember := readEvent(t, aliceWS)
	if fromJoinedMember.Type != "message.send" {
		t.Fatalf("event type = %q, want message.send", fromJoinedMember.Type)
	}
	if fromJoinedMember.From != "dev_bob" || fromJoinedMember.To != "dev_alice" {
		t.Fatalf("joined member route = %s -> %s, want dev_bob -> dev_alice", fromJoinedMember.From, fromJoinedMember.To)
	}
	if got := stringPayload(t, fromJoinedMember.Payload, "text"); got != "hello alice" {
		t.Fatalf("payload text = %q, want hello alice", got)
	}

	writeEvent(t, aliceWS, protocol.Event{
		ID:   "send-1",
		Type: "message.send",
		To:   "dev_bob",
		Payload: map[string]any{
			"text": "hello bob",
		},
	})

	direct := readEvent(t, bobWS)
	if direct.Type != "message.send" {
		t.Fatalf("event type = %q, want message.send", direct.Type)
	}
	if direct.From != "dev_alice" || direct.To != "dev_bob" {
		t.Fatalf("direct route = %s -> %s, want dev_alice -> dev_bob", direct.From, direct.To)
	}
	if got := stringPayload(t, direct.Payload, "text"); got != "hello bob" {
		t.Fatalf("payload text = %q, want hello bob", got)
	}

	writeEvent(t, aliceWS, protocol.Event{
		ID:   "broadcast-1",
		Type: "message.broadcast",
		Payload: map[string]any{
			"text": "hello channel",
		},
	})

	broadcast := readEvent(t, bobWS)
	if broadcast.Type != "message.broadcast" {
		t.Fatalf("event type = %q, want message.broadcast", broadcast.Type)
	}
	if broadcast.From != "dev_alice" || broadcast.To != "" {
		t.Fatalf("broadcast route = %s -> %s, want dev_alice -> empty", broadcast.From, broadcast.To)
	}
	if got := stringPayload(t, broadcast.Payload, "text"); got != "hello channel" {
		t.Fatalf("payload text = %q, want hello channel", got)
	}
}

func TestPendingConnectionCannotRouteMessages(t *testing.T) {
	server := newRelayServer(t)

	alice := postConnect(t, server, protocol.ConnectRequest{ChannelName: "ops", PIN: "123456", DeviceName: "alice", DeviceID: "dev_alice"})
	aliceWS := dialWSWithTokenQuery(t, alice.WSURL, alice.Token)
	bob := postConnect(t, server, protocol.ConnectRequest{ChannelName: "ops", PIN: "123456", DeviceName: "bob", DeviceID: "dev_bob"})
	bobWS := dialWSWithTokenQuery(t, bob.WSURL, bob.Token)
	_ = readEvent(t, aliceWS) // join.request

	writeEvent(t, bobWS, protocol.Event{ID: "send-pending", Type: "message.send", To: "dev_alice"})

	errorEvent := readEvent(t, bobWS)
	if errorEvent.Type != "error" {
		t.Fatalf("event type = %q, want error", errorEvent.Type)
	}
	if got := stringPayload(t, errorEvent.Payload, "code"); got != "unauthorized" {
		t.Fatalf("error code = %q, want unauthorized", got)
	}
}

func TestDeniedPendingConnectionReceivesDeniedThenCloses(t *testing.T) {
	server := newRelayServer(t)

	alice := postConnect(t, server, protocol.ConnectRequest{ChannelName: "ops", PIN: "123456", DeviceName: "alice", DeviceID: "dev_alice"})
	aliceWS := dialWSWithTokenQuery(t, alice.WSURL, alice.Token)
	bob := postConnect(t, server, protocol.ConnectRequest{ChannelName: "ops", PIN: "123456", DeviceName: "bob", DeviceID: "dev_bob"})
	bobWS := dialWSWithTokenQuery(t, bob.WSURL, bob.Token)
	_ = readEvent(t, aliceWS) // join.request

	writeEvent(t, aliceWS, protocol.Event{
		ID:   "deny-1",
		Type: "join.deny",
		Payload: map[string]any{
			"joinRequestId": bob.JoinRequestID,
		},
	})

	denied := readEvent(t, bobWS)
	if denied.Type != "join.denied" {
		t.Fatalf("event type = %q, want join.denied", denied.Type)
	}
	if denied.ReplyTo != "deny-1" {
		t.Fatalf("replyTo = %q, want deny-1", denied.ReplyTo)
	}

	assertCloseStatus(t, bobWS, websocket.StatusPolicyViolation)
	if err := writeEventMaybe(bobWS, protocol.Event{ID: "status-after-deny", Type: "status.request"}); err == nil {
		t.Fatal("expected denied connection status request to fail")
	}
}

func TestDuplicateMemberConnectionRevokesOldConnection(t *testing.T) {
	server := newRelayServer(t)

	alice := postConnect(t, server, protocol.ConnectRequest{ChannelName: "ops", PIN: "123456", DeviceName: "alice", DeviceID: "dev_alice"})
	aliceWS := dialWSWithTokenQuery(t, alice.WSURL, alice.Token)
	bob := postConnect(t, server, protocol.ConnectRequest{ChannelName: "ops", PIN: "123456", DeviceName: "bob", DeviceID: "dev_bob"})
	bobWS := dialWSWithTokenQuery(t, bob.WSURL, bob.Token)
	_ = readEvent(t, aliceWS) // join.request

	writeEvent(t, aliceWS, protocol.Event{
		ID:   "approve-1",
		Type: "join.approve",
		Payload: map[string]any{
			"joinRequestId": bob.JoinRequestID,
		},
	})
	approved := readEvent(t, bobWS)
	if approved.Type != "join.approved" {
		t.Fatalf("event type = %q, want join.approved", approved.Type)
	}

	reconnected := postConnect(t, server, protocol.ConnectRequest{ChannelName: "ops", PIN: "123456", DeviceName: "alice-new", DeviceID: "dev_alice"})
	if reconnected.Status != string(channel.StatusConnected) {
		t.Fatalf("status = %q, want %q", reconnected.Status, channel.StatusConnected)
	}
	_ = dialWSWithTokenQuery(t, reconnected.WSURL, reconnected.Token)

	assertCloseStatus(t, aliceWS, websocket.StatusPolicyViolation)
	if err := writeEventMaybe(aliceWS, protocol.Event{
		ID:   "send-from-stale",
		Type: "message.send",
		To:   "dev_bob",
		Payload: map[string]any{
			"text": "from stale alice",
		},
	}); err == nil {
		t.Fatal("expected superseded connection send to fail")
	}
	assertNoEvent(t, bobWS, 200*time.Millisecond)
}

func TestWebSocketRejectsInvalidToken(t *testing.T) {
	server := newRelayServer(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, resp, err := websocket.Dial(ctx, wsURL(server.URL)+"/ws?token=not-a-valid-token", nil)
	if err == nil {
		t.Fatal("expected invalid token dial to fail")
	}
	if resp == nil {
		t.Fatal("expected HTTP response for rejected dial")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusUnauthorized)
	}
}

func newRelayServer(t *testing.T) *httptest.Server {
	t.Helper()
	tokens, err := auth.NewTokenManager([]byte("01234567890123456789012345678901"), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(httpapi.NewServer(channel.NewRegistry(), tokens, "0.1.0").Routes())
	t.Cleanup(server.Close)
	return server
}

func postConnect(t *testing.T, server *httptest.Server, request protocol.ConnectRequest) protocol.ConnectResponse {
	t.Helper()
	body, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Post(server.URL+"/channels/connect", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("connect status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	var connect protocol.ConnectResponse
	if err := json.NewDecoder(resp.Body).Decode(&connect); err != nil {
		t.Fatal(err)
	}
	return connect
}

func dialWSWithTokenQuery(t *testing.T, wsURLValue, token string) *websocket.Conn {
	t.Helper()
	u, err := url.Parse(wsURLValue)
	if err != nil {
		t.Fatal(err)
	}
	query := u.Query()
	query.Set("token", token)
	u.RawQuery = query.Encode()
	return dialWS(t, u.String(), nil)
}

func dialWSWithAuthorization(t *testing.T, wsURLValue, token string) *websocket.Conn {
	t.Helper()
	return dialWS(t, wsURLValue, &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": []string{"Bearer " + token}}})
}

func dialWS(t *testing.T, wsURLValue string, opts *websocket.DialOptions) *websocket.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, wsURLValue, opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(websocket.StatusNormalClosure, "") })
	return conn
}

func readEvent(t *testing.T, conn *websocket.Conn) protocol.Event {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	messageType, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if messageType != websocket.MessageText {
		t.Fatalf("message type = %v, want text", messageType)
	}
	var event protocol.Event
	if err := json.Unmarshal(data, &event); err != nil {
		t.Fatalf("invalid event JSON %q: %v", string(data), err)
	}
	return event
}

func writeEvent(t *testing.T, conn *websocket.Conn, event protocol.Event) {
	t.Helper()
	if err := writeEventMaybe(conn, event); err != nil {
		t.Fatal(err)
	}
}

func writeEventMaybe(conn *websocket.Conn, event protocol.Event) error {
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return conn.Write(ctx, websocket.MessageText, data)
}

func assertCloseStatus(t *testing.T, conn *websocket.Conn, want websocket.StatusCode) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _, err := conn.Read(ctx)
	if err == nil {
		t.Fatal("expected websocket close")
	}
	if got := websocket.CloseStatus(err); got != want {
		t.Fatalf("close status = %d, want %d (err: %v)", got, want, err)
	}
}

func assertNoEvent(t *testing.T, conn *websocket.Conn, duration time.Duration) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), duration)
	defer cancel()
	messageType, data, err := conn.Read(ctx)
	if errors.Is(err, context.DeadlineExceeded) {
		return
	}
	if err != nil {
		t.Fatalf("unexpected read error while waiting for no event: %v", err)
	}
	t.Fatalf("unexpected message type %v: %s", messageType, string(data))
}

func stringPayload(t *testing.T, payload map[string]any, key string) string {
	t.Helper()
	value, ok := payload[key].(string)
	if !ok {
		t.Fatalf("payload[%q] = %#v, want string", key, payload[key])
	}
	return value
}

func wsURL(httpURL string) string {
	u, err := url.Parse(httpURL)
	if err != nil {
		panic(err)
	}
	u.Scheme = "ws"
	return u.String()
}
