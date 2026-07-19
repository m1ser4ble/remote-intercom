package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
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

	joinRequest := readUntil(t, aliceWS, "join.request", func(event protocol.Event) bool {
		return event.From == "dev_bob"
	})
	if joinRequest.ChannelID != alice.ChannelID {
		t.Fatalf("join request channel id = %q, want %q", joinRequest.ChannelID, alice.ChannelID)
	}
	if joinRequest.To != "dev_alice" {
		t.Fatalf("join request to = %q, want dev_alice", joinRequest.To)
	}
	if got := stringPayload(t, joinRequest.Payload, "joinRequestId"); got != bob.JoinRequestID {
		t.Fatalf("join request id = %q, want %q", got, bob.JoinRequestID)
	}
	if got := stringPayload(t, joinRequest.Payload, "deviceId"); got != "dev_bob" {
		t.Fatalf("join request device id = %q, want dev_bob", got)
	}
	if got := stringPayload(t, joinRequest.Payload, "deviceName"); got != "bob-laptop" {
		t.Fatalf("join request device name = %q, want bob-laptop", got)
	}

	writeEvent(t, aliceWS, protocol.Event{
		ID:   "approve-bob",
		Type: "join.approve",
		Payload: map[string]any{
			"joinRequestId": bob.JoinRequestID,
		},
	})
	approved := readUntil(t, bobWS, "join.approved", func(event protocol.Event) bool { return event.ReplyTo == "approve-bob" })
	if approved.ChannelID != alice.ChannelID {
		t.Fatalf("join approval channel id = %q, want %q", approved.ChannelID, alice.ChannelID)
	}
	if approved.From != "dev_alice" || approved.To != "dev_bob" {
		t.Fatalf("join approval route = from %q to %q, want dev_alice -> dev_bob", approved.From, approved.To)
	}
	if got := stringPayload(t, approved.Payload, "joinRequestId"); got != bob.JoinRequestID {
		t.Fatalf("join approval request id = %q, want %q", got, bob.JoinRequestID)
	}
	if got := stringPayload(t, approved.Payload, "deviceId"); got != "dev_bob" {
		t.Fatalf("join approval device id = %q, want dev_bob", got)
	}
	decisionAck := readUntil(t, aliceWS, "join.decision.ack", func(event protocol.Event) bool { return event.ReplyTo == "approve-bob" })
	assertAck(t, decisionAck, "join.decision.ack", "approve-bob", "dev_bob")
	_ = readUntil(t, aliceWS, "presence.changed", func(event protocol.Event) bool {
		return stringPayload(t, event.Payload, "deviceId") == "dev_bob"
	})

	writeEvent(t, aliceWS, protocol.Event{
		ID:   "send-1",
		Type: "message.send",
		To:   "dev_bob",
		Payload: map[string]any{
			"kind": "send",
			"text": "hello bob",
		},
	})
	message := readUntil(t, bobWS, "message.send", func(event protocol.Event) bool { return event.ID == "send-1" })
	assertMessage(t, message, "message.send", alice.ChannelID, "dev_alice", "dev_bob", "send", "hello bob", "")
	assertAck(t, readUntil(t, aliceWS, "message.ack", func(event protocol.Event) bool { return event.ReplyTo == "send-1" }), "message.ack", "send-1", "dev_bob")

	writeEvent(t, aliceWS, protocol.Event{
		ID:   "ask-1",
		Type: "message.ask",
		To:   "dev_bob",
		Payload: map[string]any{
			"kind": "ask",
			"text": "approve deploy?",
		},
	})
	ask := readUntil(t, bobWS, "message.ask", func(event protocol.Event) bool { return event.ID == "ask-1" })
	assertMessage(t, ask, "message.ask", alice.ChannelID, "dev_alice", "dev_bob", "ask", "approve deploy?", "")
	assertAck(t, readUntil(t, aliceWS, "message.ack", func(event protocol.Event) bool { return event.ReplyTo == "ask-1" }), "message.ack", "ask-1", "dev_bob")

	writeEvent(t, bobWS, protocol.Event{
		ID:      "reply-1",
		Type:    "message.reply",
		To:      "dev_alice",
		ReplyTo: "ask-1",
		Payload: map[string]any{
			"kind": "reply",
			"text": "approved",
		},
	})
	reply := readUntil(t, aliceWS, "message.reply", func(event protocol.Event) bool { return event.ID == "reply-1" }, "presence.changed", "owner.changed")
	assertMessage(t, reply, "message.reply", alice.ChannelID, "dev_bob", "dev_alice", "reply", "approved", "ask-1")
	assertAck(t, readUntil(t, bobWS, "message.ack", func(event protocol.Event) bool { return event.ReplyTo == "reply-1" }), "message.ack", "reply-1", "dev_alice")

	if err := aliceWS.Close(websocket.StatusNormalClosure, "e2e disconnect alice"); err != nil {
		t.Fatalf("close alice websocket: %v", err)
	}
	waitForOwner(t, bobWS, alice.ChannelID, "dev_bob", bob.JoinRequestID, "dev_bob")

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
	waitForOwner(t, reconnectedAliceWS, alice.ChannelID, "dev_alice", "", "dev_alice")
}

func TestRemoteIntercomFiveMessageBurst(t *testing.T) {
	fixture := newRelayFixture(t)
	alice := fixture.connect(t, protocol.ConnectRequest{ChannelName: "burst", PIN: "123456", DeviceName: "alice", DeviceID: "dev_alice"})
	aliceWS := dialMemberWS(t, alice.WSURL, alice.Token)
	bob := fixture.connect(t, protocol.ConnectRequest{ChannelName: "burst", PIN: "123456", DeviceName: "bob", DeviceID: "dev_bob"})
	bobWS := dialMemberWS(t, bob.WSURL, bob.Token)
	_ = readUntil(t, aliceWS, "join.request", func(event protocol.Event) bool { return event.From == "dev_bob" })

	writeEvent(t, aliceWS, protocol.Event{
		ID: "approve-burst", Type: "join.approve",
		Payload: map[string]any{"joinRequestId": bob.JoinRequestID},
	})
	_ = readUntil(t, bobWS, "join.approved", func(event protocol.Event) bool { return event.ReplyTo == "approve-burst" })
	decisionAck := readUntil(t, aliceWS, "join.decision.ack", func(event protocol.Event) bool { return event.ReplyTo == "approve-burst" })
	assertAck(t, decisionAck, "join.decision.ack", "approve-burst", "dev_bob")
	_ = readUntil(t, aliceWS, "presence.changed", func(event protocol.Event) bool {
		return stringPayload(t, event.Payload, "deviceId") == "dev_bob"
	})

	for i := 1; i <= 5; i++ {
		id := fmt.Sprintf("chunk-%d", i)
		text := fmt.Sprintf("CHUNK %d/5 ", i) + strings.Repeat(fmt.Sprintf("%d", i), 12_000)
		writeEvent(t, aliceWS, protocol.Event{
			ID: id, Type: "message.send", To: "dev_bob",
			Payload: map[string]any{"kind": "send", "text": text},
		})
		received := readUntil(t, bobWS, "message.send", func(event protocol.Event) bool { return event.ID == id })
		assertMessage(t, received, "message.send", alice.ChannelID, "dev_alice", "dev_bob", "send", text, "")
		ack := readUntil(t, aliceWS, "message.ack", func(event protocol.Event) bool { return event.ReplyTo == id })
		assertAck(t, ack, "message.ack", id, "dev_bob")
	}
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
	relay.Hub.SetReconnectGrace(25 * time.Millisecond)
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
	client := http.Client{Timeout: 5 * time.Second}
	resp, err := client.Post(f.server.URL+"/channels/connect", "application/json", bytes.NewReader(body))
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

func readUntil(t *testing.T, conn *websocket.Conn, expectedType string, match func(protocol.Event) bool, allowedIntermediaryTypes ...string) protocol.Event {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var seen []string
	allowedIntermediaries := make(map[string]struct{}, len(allowedIntermediaryTypes))
	for _, eventType := range allowedIntermediaryTypes {
		allowedIntermediaries[eventType] = struct{}{}
	}
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
		if event.Type == "error" {
			t.Fatalf("received error event while waiting for %s after seeing [%s]: replyTo=%q payload=%v", expectedType, joinSeen(seen), event.ReplyTo, event.Payload)
		}
		if event.Type != expectedType {
			if _, ok := allowedIntermediaries[event.Type]; ok {
				continue
			}
			t.Fatalf("unexpected event type = %q, want %q after seeing [%s]", event.Type, expectedType, joinSeen(seen))
		}
		if match(event) {
			return event
		}
		t.Fatalf("unexpected %s event after seeing [%s]: id=%q from=%q to=%q replyTo=%q payload=%v", expectedType, joinSeen(seen), event.ID, event.From, event.To, event.ReplyTo, event.Payload)
	}
	t.Fatalf("timed out waiting for %s websocket event after seeing [%s]", expectedType, joinSeen(seen))
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

func waitForOwner(t *testing.T, conn *websocket.Conn, channelID, deviceID, joinRequestID, wantOwnerID string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	attempt := 0
	var lastOwnerID string
	for time.Now().Before(deadline) {
		attempt++
		eventID := fmt.Sprintf("status-%s-%d", wantOwnerID, attempt)
		writeEvent(t, conn, protocol.Event{ID: eventID, Type: "status.request"})
		status := readUntil(t, conn, "status.response", func(event protocol.Event) bool {
			return event.ReplyTo == eventID
		}, "presence.changed", "owner.changed")
		assertStatusResponse(t, status, channelID, deviceID, joinRequestID)
		lastOwnerID = stringPayload(t, status.Payload, "ownerId")
		if lastOwnerID == wantOwnerID {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("owner id = %q, want %q", lastOwnerID, wantOwnerID)
}

func assertStatusResponse(t *testing.T, event protocol.Event, channelID, deviceID, joinRequestID string) {
	t.Helper()
	if event.Type != "status.response" {
		t.Fatalf("event type = %q, want status.response", event.Type)
	}
	if event.ChannelID != channelID {
		t.Fatalf("status response channel id = %q, want %q", event.ChannelID, channelID)
	}
	if event.To != deviceID {
		t.Fatalf("status response to = %q, want %q", event.To, deviceID)
	}
	if got := stringPayload(t, event.Payload, "status"); got != "member" {
		t.Fatalf("status response status = %q, want member", got)
	}
	if got := stringPayload(t, event.Payload, "channelId"); got != channelID {
		t.Fatalf("status response payload channel id = %q, want %q", got, channelID)
	}
	if got := stringPayload(t, event.Payload, "deviceId"); got != deviceID {
		t.Fatalf("status response payload device id = %q, want %q", got, deviceID)
	}
	if got := stringPayload(t, event.Payload, "joinRequestId"); got != joinRequestID {
		t.Fatalf("status response payload join request id = %q, want %q", got, joinRequestID)
	}
}

func assertMessage(t *testing.T, event protocol.Event, eventType, channelID, from, to, kind, text, replyTo string) {
	t.Helper()
	if event.Type != eventType {
		t.Fatalf("event type = %q, want %s", event.Type, eventType)
	}
	if event.ChannelID != channelID {
		t.Fatalf("message channel id = %q, want %q", event.ChannelID, channelID)
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

func assertAck(t *testing.T, event protocol.Event, eventType, replyTo, deviceID string) {
	t.Helper()
	if event.Type != eventType || event.ReplyTo != replyTo {
		t.Fatalf("ack = type %q replyTo %q, want %q/%q", event.Type, event.ReplyTo, eventType, replyTo)
	}
	if got := stringPayload(t, event.Payload, "deviceId"); got != deviceID {
		t.Fatalf("ack device id = %q, want %q", got, deviceID)
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
