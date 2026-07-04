package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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

func TestRemoteIntercomEndToEndSmoke(t *testing.T) {
	fixture := newRelayFixture(t)

	alice := fixture.connect(t, protocol.ConnectRequest{
		ChannelName:   "Ops Room",
		PIN:           "123456",
		DeviceName:    "alice-laptop",
		DeviceID:      "dev_alice",
		ClientVersion: "e2e-test",
	})
	if alice.Status != string(channel.StatusCreated) {
		t.Fatalf("alice connect status = %q, want %q", alice.Status, channel.StatusCreated)
	}
	aliceWS := dialMemberWS(t, alice.WSURL, alice.Token)

	bob := fixture.connect(t, protocol.ConnectRequest{
		ChannelName:   "ops room",
		PIN:           "123456",
		DeviceName:    "bob-laptop",
		DeviceID:      "dev_bob",
		ClientVersion: "e2e-test",
	})
	if bob.Status != string(channel.StatusPending) {
		t.Fatalf("bob connect status = %q, want %q", bob.Status, channel.StatusPending)
	}
	if bob.ChannelID != alice.ChannelID {
		t.Fatalf("bob channel id = %q, want %q", bob.ChannelID, alice.ChannelID)
	}
	if bob.JoinRequestID == "" {
		t.Fatal("expected bob join request id")
	}
	bobWS := dialMemberWS(t, bob.WSURL, bob.Token)

	joinRequest := readUntil(t, aliceWS, func(event protocol.Event) bool {
		return event.Type == "join.request" && event.From == "dev_bob"
	})
	if joinRequest.To != "dev_alice" {
		t.Fatalf("join request to = %q, want dev_alice", joinRequest.To)
	}
	if got := stringPayload(t, joinRequest.Payload, "joinRequestId"); got != bob.JoinRequestID {
		t.Fatalf("join request id = %q, want %q", got, bob.JoinRequestID)
	}

	writeEvent(t, aliceWS, protocol.Event{
		ID:   "approve-bob",
		Type: "join.approve",
		Payload: map[string]any{
			"joinRequestId": bob.JoinRequestID,
		},
	})
	approved := readUntil(t, bobWS, func(event protocol.Event) bool { return event.Type == "join.approved" })
	if approved.From != "dev_alice" || approved.To != "dev_bob" || approved.ReplyTo != "approve-bob" {
		t.Fatalf("join approval route = from %q to %q replyTo %q, want dev_alice -> dev_bob replyTo approve-bob", approved.From, approved.To, approved.ReplyTo)
	}

	writeEvent(t, aliceWS, protocol.Event{
		ID:   "send-1",
		Type: "message.send",
		To:   "dev_bob",
		Payload: map[string]any{
			"kind": "send",
			"text": "hello bob",
		},
	})
	message := readUntil(t, bobWS, func(event protocol.Event) bool { return event.ID == "send-1" })
	assertMessage(t, message, "dev_alice", "dev_bob", "send", "hello bob", "")

	writeEvent(t, aliceWS, protocol.Event{
		ID:   "ask-1",
		Type: "message.send",
		To:   "dev_bob",
		Payload: map[string]any{
			"kind": "ask",
			"text": "approve deploy?",
		},
	})
	ask := readUntil(t, bobWS, func(event protocol.Event) bool { return event.ID == "ask-1" })
	assertMessage(t, ask, "dev_alice", "dev_bob", "ask", "approve deploy?", "")

	writeEvent(t, bobWS, protocol.Event{
		ID:      "reply-1",
		Type:    "message.send",
		To:      "dev_alice",
		ReplyTo: "ask-1",
		Payload: map[string]any{
			"kind": "reply",
			"text": "approved",
		},
	})
	reply := readUntil(t, aliceWS, func(event protocol.Event) bool { return event.ID == "reply-1" })
	assertMessage(t, reply, "dev_bob", "dev_alice", "reply", "approved", "ask-1")

	if err := aliceWS.Close(websocket.StatusNormalClosure, "e2e disconnect alice"); err != nil {
		t.Fatalf("close alice websocket: %v", err)
	}
	waitForOwner(t, bobWS, "dev_bob")

	reconnectedAlice := fixture.connect(t, protocol.ConnectRequest{
		ChannelName:   "ops room",
		PIN:           "123456",
		DeviceName:    "alice-laptop-reconnected",
		DeviceID:      "dev_alice",
		ClientVersion: "e2e-test",
	})
	if reconnectedAlice.Status != string(channel.StatusConnected) {
		t.Fatalf("reconnected alice status = %q, want %q", reconnectedAlice.Status, channel.StatusConnected)
	}
	if reconnectedAlice.ChannelID != alice.ChannelID {
		t.Fatalf("reconnected alice channel id = %q, want %q", reconnectedAlice.ChannelID, alice.ChannelID)
	}
	reconnectedAliceWS := dialMemberWS(t, reconnectedAlice.WSURL, reconnectedAlice.Token)
	waitForOwner(t, reconnectedAliceWS, "dev_alice")
}

type relayFixture struct {
	server   *httptest.Server
	registry *channel.Registry
}

func newRelayFixture(t *testing.T) relayFixture {
	t.Helper()
	tokens, err := auth.NewTokenManager([]byte("01234567890123456789012345678901"), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	registry := channel.NewRegistry()
	relay := httpapi.NewServer(registry, tokens, "0.1.0")
	server := httptest.NewServer(relay.Routes())
	t.Cleanup(server.Close)
	return relayFixture{server: server, registry: registry}
}

func (f relayFixture) connect(t *testing.T, request protocol.ConnectRequest) protocol.ConnectResponse {
	t.Helper()
	body, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Post(f.server.URL+"/channels/connect", "application/json", bytes.NewReader(body))
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
	if connect.WSURL == "" {
		t.Fatal("connect response missing wsUrl")
	}
	if _, err := url.Parse(connect.WSURL); err != nil {
		t.Fatalf("connect response wsUrl %q is invalid: %v", connect.WSURL, err)
	}
	return connect
}

func dialMemberWS(t *testing.T, wsURLValue, token string) *websocket.Conn {
	t.Helper()
	u, err := url.Parse(wsURLValue)
	if err != nil {
		t.Fatal(err)
	}
	query := u.Query()
	query.Set("token", token)
	u.RawQuery = query.Encode()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, u.String(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(websocket.StatusNormalClosure, "") })
	return conn
}

func readUntil(t *testing.T, conn *websocket.Conn, match func(protocol.Event) bool) protocol.Event {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var seen []string
	for time.Now().Before(deadline) {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			break
		}
		ctx, cancel := context.WithTimeout(context.Background(), remaining)
		messageType, data, err := conn.Read(ctx)
		cancel()
		if err != nil {
			t.Fatalf("read websocket event after seeing [%s]: %v", joinSeen(seen), err)
		}
		if messageType != websocket.MessageText {
			t.Fatalf("message type = %v, want text", messageType)
		}
		var event protocol.Event
		if err := json.Unmarshal(data, &event); err != nil {
			t.Fatalf("invalid event JSON %q: %v", string(data), err)
		}
		seen = append(seen, fmt.Sprintf("%s/%s", event.Type, event.ID))
		if match(event) {
			return event
		}
	}
	t.Fatalf("timed out waiting for matching websocket event after seeing [%s]", joinSeen(seen))
	return protocol.Event{}
}

func writeEvent(t *testing.T, conn *websocket.Conn, event protocol.Event) {
	t.Helper()
	data, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := conn.Write(ctx, websocket.MessageText, data); err != nil {
		t.Fatal(err)
	}
}

func waitForOwner(t *testing.T, conn *websocket.Conn, wantOwnerID string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	attempt := 0
	var lastOwnerID string
	for time.Now().Before(deadline) {
		attempt++
		eventID := fmt.Sprintf("status-%s-%d", wantOwnerID, attempt)
		writeEvent(t, conn, protocol.Event{ID: eventID, Type: "status.request"})
		status := readUntil(t, conn, func(event protocol.Event) bool {
			return event.Type == "status.response" && event.ReplyTo == eventID
		})
		lastOwnerID = stringPayload(t, status.Payload, "ownerId")
		if lastOwnerID == wantOwnerID {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("owner id = %q, want %q", lastOwnerID, wantOwnerID)
}

func assertMessage(t *testing.T, event protocol.Event, from, to, kind, text, replyTo string) {
	t.Helper()
	if event.Type != "message.send" {
		t.Fatalf("event type = %q, want message.send", event.Type)
	}
	if event.From != from || event.To != to {
		t.Fatalf("message route = %q -> %q, want %q -> %q", event.From, event.To, from, to)
	}
	if event.ReplyTo != replyTo {
		t.Fatalf("replyTo = %q, want %q", event.ReplyTo, replyTo)
	}
	if got := stringPayload(t, event.Payload, "kind"); got != kind {
		t.Fatalf("payload kind = %q, want %q", got, kind)
	}
	if got := stringPayload(t, event.Payload, "text"); got != text {
		t.Fatalf("payload text = %q, want %q", got, text)
	}
}

func stringPayload(t *testing.T, payload map[string]any, key string) string {
	t.Helper()
	value, ok := payload[key].(string)
	if !ok {
		t.Fatalf("payload[%q] = %#v, want string", key, payload[key])
	}
	return value
}

func joinSeen(seen []string) string {
	if len(seen) == 0 {
		return ""
	}
	result := seen[0]
	for _, value := range seen[1:] {
		result += ", " + value
	}
	return result
}
