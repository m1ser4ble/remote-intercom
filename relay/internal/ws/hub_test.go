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
	server := newRelayFixture(t, 50*time.Millisecond).server

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

	joinRequest := readEventOfType(t, aliceWS, "join.request", nil)
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
	if token, ok := approved.Payload["token"].(string); !ok || token == "" {
		t.Fatalf("join.approved missing member token payload: %+v", approved.Payload)
	}

	writeEvent(t, bobWS, protocol.Event{
		ID:   "send-joined",
		Type: "message.send",
		To:   "dev_alice",
		Payload: map[string]any{
			"text": "hello alice",
		},
	})

	fromJoinedMember := readEventOfType(t, aliceWS, "message.send", func(event protocol.Event) bool { return event.ID == "send-joined" })
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
		ID:   "ask-1",
		Type: "message.ask",
		To:   "dev_bob",
		Payload: map[string]any{
			"text": "approve deploy?",
		},
	})
	ask := readEvent(t, bobWS)
	if ask.Type != "message.ask" || ask.From != "dev_alice" || ask.To != "dev_bob" {
		t.Fatalf("ask route = type %s %s -> %s, want message.ask dev_alice -> dev_bob", ask.Type, ask.From, ask.To)
	}

	writeEvent(t, bobWS, protocol.Event{
		ID:      "reply-1",
		Type:    "message.reply",
		To:      "dev_alice",
		ReplyTo: "ask-1",
		Payload: map[string]any{
			"text": "approved",
		},
	})
	reply := readEventOfType(t, aliceWS, "message.reply", func(event protocol.Event) bool { return event.ID == "reply-1" })
	if reply.From != "dev_bob" || reply.To != "dev_alice" || reply.ReplyTo != "ask-1" {
		t.Fatalf("reply route = %s -> %s replyTo %s, want dev_bob -> dev_alice replyTo ask-1", reply.From, reply.To, reply.ReplyTo)
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
	server := newRelayFixture(t, 50*time.Millisecond).server

	alice := postConnect(t, server, protocol.ConnectRequest{ChannelName: "ops", PIN: "123456", DeviceName: "alice", DeviceID: "dev_alice"})
	aliceWS := dialWSWithTokenQuery(t, alice.WSURL, alice.Token)
	bob := postConnect(t, server, protocol.ConnectRequest{ChannelName: "ops", PIN: "123456", DeviceName: "bob", DeviceID: "dev_bob"})
	bobWS := dialWSWithTokenQuery(t, bob.WSURL, bob.Token)
	_ = readEventOfType(t, aliceWS, "join.request", nil)

	writeEvent(t, bobWS, protocol.Event{ID: "send-pending", Type: "message.ask", To: "dev_alice"})

	errorEvent := readEvent(t, bobWS)
	if errorEvent.Type != "error" {
		t.Fatalf("event type = %q, want error", errorEvent.Type)
	}
	if got := stringPayload(t, errorEvent.Payload, "code"); got != "unauthorized" {
		t.Fatalf("error code = %q, want unauthorized", got)
	}
}

func TestOversizedWebSocketMessageIsRejected(t *testing.T) {
	server := newRelayFixture(t, 50*time.Millisecond).server
	alice := postConnect(t, server, protocol.ConnectRequest{ChannelName: "ops", PIN: "123456", DeviceName: "alice", DeviceID: "dev_alice"})
	aliceWS := dialWSWithTokenQuery(t, alice.WSURL, alice.Token)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := aliceWS.Write(ctx, websocket.MessageText, bytes.Repeat([]byte("a"), 64*1024+1)); err != nil {
		t.Fatal(err)
	}
	assertCloseStatus(t, aliceWS, websocket.StatusMessageTooBig)
}

func TestDeniedPendingConnectionReceivesDeniedThenCloses(t *testing.T) {
	server := newRelayFixture(t, 50*time.Millisecond).server

	alice := postConnect(t, server, protocol.ConnectRequest{ChannelName: "ops", PIN: "123456", DeviceName: "alice", DeviceID: "dev_alice"})
	aliceWS := dialWSWithTokenQuery(t, alice.WSURL, alice.Token)
	bob := postConnect(t, server, protocol.ConnectRequest{ChannelName: "ops", PIN: "123456", DeviceName: "bob", DeviceID: "dev_bob"})
	bobWS := dialWSWithTokenQuery(t, bob.WSURL, bob.Token)
	_ = readEventOfType(t, aliceWS, "join.request", nil)

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
	server := newRelayFixture(t, 50*time.Millisecond).server

	alice := postConnect(t, server, protocol.ConnectRequest{ChannelName: "ops", PIN: "123456", DeviceName: "alice", DeviceID: "dev_alice"})
	aliceWS := dialWSWithTokenQuery(t, alice.WSURL, alice.Token)
	bob := postConnect(t, server, protocol.ConnectRequest{ChannelName: "ops", PIN: "123456", DeviceName: "bob", DeviceID: "dev_bob"})
	bobWS := dialWSWithTokenQuery(t, bob.WSURL, bob.Token)
	_ = readEventOfType(t, aliceWS, "join.request", nil)

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
	_ = readEventOfType(t, aliceWS, "presence.changed", func(event protocol.Event) bool {
		return stringPayload(t, event.Payload, "deviceId") == "dev_bob"
	})

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

func TestLastMemberDisconnectGraceDeletesChannel(t *testing.T) {
	fixture := newRelayFixture(t, 25*time.Millisecond)
	alice := postConnect(t, fixture.server, protocol.ConnectRequest{ChannelName: "ops", PIN: "123456", DeviceName: "alice", DeviceID: "dev_alice"})
	aliceWS := dialWSWithTokenQuery(t, alice.WSURL, alice.Token)

	if err := aliceWS.Close(websocket.StatusNormalClosure, "disconnect alice"); err != nil {
		t.Fatal(err)
	}
	waitForCondition(t, func() bool { return fixture.registry.Channel(alice.ChannelID) == nil })

	fresh := postConnect(t, fixture.server, protocol.ConnectRequest{ChannelName: "ops", PIN: "123456", DeviceName: "alice", DeviceID: "dev_alice"})
	if fresh.Status != string(channel.StatusCreated) {
		t.Fatalf("fresh status = %q, want %q", fresh.Status, channel.StatusCreated)
	}
}

func TestHTTPConnectWithoutWebSocketDoesNotCreateOnlineOwner(t *testing.T) {
	fixture := newRelayFixture(t, 25*time.Millisecond)
	alice := postConnect(t, fixture.server, protocol.ConnectRequest{ChannelName: "ops", PIN: "123456", DeviceName: "alice", DeviceID: "dev_alice"})

	ch := fixture.registry.Channel(alice.ChannelID)
	if ch == nil {
		t.Fatal("channel missing")
	}
	if ch.CurrentOwnerID != "" {
		t.Fatalf("owner = %q, want empty before websocket connects", ch.CurrentOwnerID)
	}
	if ch.Members["dev_alice"].Online {
		t.Fatal("HTTP-only member is online")
	}
}

func TestOwnerDisconnectResendsPendingJoinToFailoverOwner(t *testing.T) {
	fixture := newRelayFixture(t, 25*time.Millisecond)
	alice := postConnect(t, fixture.server, protocol.ConnectRequest{ChannelName: "ops", PIN: "123456", DeviceName: "alice", DeviceID: "dev_alice"})
	aliceWS := dialWSWithTokenQuery(t, alice.WSURL, alice.Token)
	bob := postConnect(t, fixture.server, protocol.ConnectRequest{ChannelName: "ops", PIN: "123456", DeviceName: "bob", DeviceID: "dev_bob"})
	bobWS := dialWSWithTokenQuery(t, bob.WSURL, bob.Token)
	_ = readEventOfType(t, aliceWS, "join.request", func(event protocol.Event) bool { return event.From == "dev_bob" })

	writeEvent(t, aliceWS, protocol.Event{ID: "approve-bob", Type: "join.approve", Payload: map[string]any{"joinRequestId": bob.JoinRequestID}})
	_ = readEventOfType(t, bobWS, "join.approved", func(event protocol.Event) bool { return event.ReplyTo == "approve-bob" })
	_ = readEventOfType(t, aliceWS, "presence.changed", func(event protocol.Event) bool {
		return stringPayload(t, event.Payload, "deviceId") == "dev_bob"
	})

	carol := postConnect(t, fixture.server, protocol.ConnectRequest{ChannelName: "ops", PIN: "123456", DeviceName: "carol", DeviceID: "dev_carol"})
	carolWS := dialWSWithTokenQuery(t, carol.WSURL, carol.Token)
	_ = carolWS
	_ = readEventOfType(t, aliceWS, "join.request", func(event protocol.Event) bool { return event.From == "dev_carol" })

	if err := aliceWS.Close(websocket.StatusNormalClosure, "disconnect owner"); err != nil {
		t.Fatal(err)
	}
	resent := readEventOfType(t, bobWS, "join.request", func(event protocol.Event) bool { return event.From == "dev_carol" })
	if got := stringPayload(t, resent.Payload, "joinRequestId"); got != carol.JoinRequestID {
		t.Fatalf("resent joinRequestId = %q, want %q", got, carol.JoinRequestID)
	}
}

func TestOriginalOwnerReconnectRestoresOwner(t *testing.T) {
	fixture := newRelayFixture(t, 25*time.Millisecond)
	alice := postConnect(t, fixture.server, protocol.ConnectRequest{ChannelName: "ops", PIN: "123456", DeviceName: "alice", DeviceID: "dev_alice"})
	aliceWS := dialWSWithTokenQuery(t, alice.WSURL, alice.Token)
	bob := postConnect(t, fixture.server, protocol.ConnectRequest{ChannelName: "ops", PIN: "123456", DeviceName: "bob", DeviceID: "dev_bob"})
	bobWS := dialWSWithTokenQuery(t, bob.WSURL, bob.Token)
	_ = readEventOfType(t, aliceWS, "join.request", nil)
	writeEvent(t, aliceWS, protocol.Event{ID: "approve-bob", Type: "join.approve", Payload: map[string]any{"joinRequestId": bob.JoinRequestID}})
	_ = readEventOfType(t, bobWS, "join.approved", nil)
	_ = readEventOfType(t, aliceWS, "presence.changed", nil)

	if err := aliceWS.Close(websocket.StatusNormalClosure, "disconnect alice"); err != nil {
		t.Fatal(err)
	}
	_ = readEventOfType(t, bobWS, "owner.changed", func(event protocol.Event) bool {
		return stringPayload(t, event.Payload, "ownerId") == "dev_bob"
	})

	carol := postConnect(t, fixture.server, protocol.ConnectRequest{ChannelName: "ops", PIN: "123456", DeviceName: "carol", DeviceID: "dev_carol"})
	carolWS := dialWSWithTokenQuery(t, carol.WSURL, carol.Token)
	_ = carolWS
	_ = readEventOfType(t, bobWS, "join.request", func(event protocol.Event) bool { return event.From == "dev_carol" })

	reconnected := postConnect(t, fixture.server, protocol.ConnectRequest{ChannelName: "ops", PIN: "123456", DeviceName: "alice-reconnected", DeviceID: "dev_alice"})
	reconnectedAliceWS := dialWSWithTokenQuery(t, reconnected.WSURL, reconnected.Token)
	resent := readEventOfType(t, reconnectedAliceWS, "join.request", func(event protocol.Event) bool { return event.From == "dev_carol" })
	if got := stringPayload(t, resent.Payload, "joinRequestId"); got != carol.JoinRequestID {
		t.Fatalf("reconnected owner joinRequestId = %q, want %q", got, carol.JoinRequestID)
	}
	writeEvent(t, reconnectedAliceWS, protocol.Event{ID: "status-after-reconnect", Type: "status.request"})
	status := readEventOfType(t, reconnectedAliceWS, "status.response", func(event protocol.Event) bool { return event.ReplyTo == "status-after-reconnect" })
	if got := stringPayload(t, status.Payload, "ownerId"); got != "dev_alice" {
		t.Fatalf("owner after reconnect = %q, want dev_alice", got)
	}
}

func TestWebSocketRejectsInvalidToken(t *testing.T) {
	server := newRelayFixture(t, 50*time.Millisecond).server

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

type relayFixture struct {
	server   *httptest.Server
	registry *channel.Registry
	relay    *httpapi.Server
}

func newRelayFixture(t *testing.T, reconnectGrace time.Duration) relayFixture {
	t.Helper()
	tokens, err := auth.NewTokenManager([]byte("01234567890123456789012345678901"), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	registry := channel.NewRegistry()
	relay := httpapi.NewServer(registry, tokens, "0.1.0")
	relay.Hub.SetReconnectGrace(reconnectGrace)
	server := httptest.NewServer(relay.Routes())
	t.Cleanup(server.Close)
	return relayFixture{server: server, registry: registry, relay: relay}
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

func readEventOfType(t *testing.T, conn *websocket.Conn, eventType string, match func(protocol.Event) bool) protocol.Event {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			break
		}
		ctx, cancel := context.WithTimeout(context.Background(), remaining)
		messageType, data, err := conn.Read(ctx)
		cancel()
		if err != nil {
			t.Fatalf("read websocket event while waiting for %s: %v", eventType, err)
		}
		if messageType != websocket.MessageText {
			t.Fatalf("message type = %v, want text", messageType)
		}
		var event protocol.Event
		if err := json.Unmarshal(data, &event); err != nil {
			t.Fatalf("invalid event JSON %q: %v", string(data), err)
		}
		if event.Type != eventType {
			continue
		}
		if match == nil || match(event) {
			return event
		}
	}
	t.Fatalf("timed out waiting for websocket event type %s", eventType)
	return protocol.Event{}
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

func waitForCondition(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition was not met before deadline")
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
