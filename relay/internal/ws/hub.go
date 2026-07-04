package ws

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/remote-intercom/remote-intercom/relay/internal/auth"
	"github.com/remote-intercom/remote-intercom/relay/internal/channel"
	"github.com/remote-intercom/remote-intercom/relay/internal/protocol"
)

const (
	writeTimeout     = 5 * time.Second
	heartbeatEvery   = 30 * time.Second
	heartbeatTimeout = 5 * time.Second
)

// Hub manages live WebSocket connections and routes relay protocol events.
type Hub struct {
	registry *channel.Registry
	tokens   *auth.TokenManager

	mu      sync.RWMutex
	members map[string]map[string]*connection // channelID -> deviceID -> connection
	pending map[string]*connection            // joinRequestID -> connection
}

// NewHub returns a WebSocket hub backed by the shared channel registry and token manager.
func NewHub(registry *channel.Registry, tokens *auth.TokenManager) *Hub {
	return &Hub{
		registry: registry,
		tokens:   tokens,
		members:  make(map[string]map[string]*connection),
		pending:  make(map[string]*connection),
	}
}

// ServeHTTP authenticates and accepts a WebSocket connection.
func (h *Hub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
		return
	}
	if h.registry == nil || h.tokens == nil {
		http.Error(w, "websocket hub is not configured", http.StatusInternalServerError)
		return
	}

	claims, err := h.authenticate(r)
	if err != nil {
		http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
		return
	}
	if err := h.validateClaims(claims); err != nil {
		http.Error(w, http.StatusText(http.StatusForbidden), http.StatusForbidden)
		return
	}

	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}

	client := &connection{
		hub:           h,
		conn:          conn,
		channelID:     claims.ChannelID,
		deviceID:      claims.DeviceID,
		role:          claims.Role,
		joinRequestID: claims.JoinRequestID,
	}

	if client.role == auth.RoleMember {
		if err := h.registry.SetOnline(client.channelID, client.deviceID, true); err != nil {
			_ = conn.Close(websocket.StatusPolicyViolation, "member is not registered")
			return
		}
	}

	h.register(client)
	defer h.unregister(client)
	defer conn.Close(websocket.StatusNormalClosure, "")

	if client.role == auth.RolePending {
		h.notifyOwnerOfJoin(client)
	}

	done := make(chan struct{})
	defer close(done)
	go client.heartbeat(done)

	for {
		messageType, data, err := conn.Read(context.Background())
		if err != nil {
			return
		}
		if messageType != websocket.MessageText {
			client.sendError("invalid_event", "only text JSON frames are supported", "")
			continue
		}

		var event protocol.Event
		if err := json.Unmarshal(data, &event); err != nil {
			client.sendError("invalid_event", "event must be valid JSON", "")
			continue
		}
		h.handleEvent(client, event)
	}
}

func (h *Hub) authenticate(r *http.Request) (*auth.Claims, error) {
	token := tokenFromHeader(r.Header.Get("Authorization"))
	if token == "" {
		token = strings.TrimSpace(r.URL.Query().Get("token"))
	}
	if token == "" {
		return nil, errors.New("missing token")
	}
	return h.tokens.Verify(token)
}

func tokenFromHeader(header string) string {
	scheme, token, ok := strings.Cut(strings.TrimSpace(header), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return ""
	}
	return strings.TrimSpace(token)
}

func (h *Hub) validateClaims(claims *auth.Claims) error {
	ch := h.registry.Channel(claims.ChannelID)
	if ch == nil {
		return errors.New("unknown channel")
	}

	switch claims.Role {
	case auth.RoleMember:
		if _, ok := ch.Members[claims.DeviceID]; !ok {
			return errors.New("unknown member")
		}
		return nil
	case auth.RolePending:
		join, ok := ch.PendingJoins[claims.JoinRequestID]
		if !ok || join.DeviceID != claims.DeviceID {
			return errors.New("unknown pending join")
		}
		return nil
	default:
		return errors.New("invalid role")
	}
}

func (h *Hub) handleEvent(c *connection, event protocol.Event) {
	if event.ChannelID != "" && event.ChannelID != c.channelID {
		c.sendError("unauthorized", "event channel does not match token", event.ID)
		return
	}

	switch event.Type {
	case "message.send":
		h.handleMessageSend(c, event)
	case "message.broadcast":
		h.handleMessageBroadcast(c, event)
	case "join.approve":
		h.handleJoinDecision(c, event, true)
	case "join.deny":
		h.handleJoinDecision(c, event, false)
	case "list.request":
		h.sendList(c, event.ID)
	case "status.request":
		h.sendStatus(c, event.ID)
	default:
		c.sendError("invalid_event", "unknown event type", event.ID)
	}
}

func (h *Hub) handleMessageSend(c *connection, event protocol.Event) {
	if !c.isMember() {
		c.sendError("unauthorized", "pending connections cannot send messages", event.ID)
		return
	}
	if strings.TrimSpace(event.To) == "" {
		c.sendError("invalid_event", "message.send requires a target", event.ID)
		return
	}

	target := h.memberConnection(c.channelID, event.To)
	if target == nil || !target.isMember() {
		c.sendError("unknown_target", "target is not online", event.ID)
		return
	}

	event.ChannelID = c.channelID
	event.From = c.deviceID
	if event.Payload == nil {
		event.Payload = map[string]any{}
	}
	if err := target.writeEvent(event); err != nil {
		c.sendError("unknown_target", "target is not reachable", event.ID)
	}
}

func (h *Hub) handleMessageBroadcast(c *connection, event protocol.Event) {
	if !c.isMember() {
		c.sendError("unauthorized", "pending connections cannot broadcast messages", event.ID)
		return
	}

	event.ChannelID = c.channelID
	event.From = c.deviceID
	if event.Payload == nil {
		event.Payload = map[string]any{}
	}

	for _, target := range h.memberConnections(c.channelID) {
		if target.deviceID == c.deviceID || !target.isMember() {
			continue
		}
		_ = target.writeEvent(event)
	}
}

func (h *Hub) handleJoinDecision(c *connection, event protocol.Event, approve bool) {
	if !c.isMember() {
		c.sendError("unauthorized", "only members can decide join requests", event.ID)
		return
	}

	joinRequestID := joinRequestIDFromEvent(event)
	if joinRequestID == "" {
		c.sendError("invalid_event", "join decision requires joinRequestId or target", event.ID)
		return
	}

	ch := h.registry.Channel(c.channelID)
	if ch == nil {
		c.sendError("invalid_event", "channel not found", event.ID)
		return
	}
	if ch.CurrentOwnerID != c.deviceID {
		c.sendError("unauthorized", "only the current owner can decide join requests", event.ID)
		return
	}
	join, ok := ch.PendingJoins[joinRequestID]
	if !ok {
		joinRequestID, join, ok = pendingJoinByDevice(ch, event.To)
	}
	if !ok {
		c.sendError("unknown_target", "join request not found", event.ID)
		return
	}

	var err error
	if approve {
		err = h.registry.Approve(c.channelID, joinRequestID)
	} else {
		err = h.registry.Deny(c.channelID, joinRequestID)
	}
	if err != nil {
		c.sendError("invalid_event", err.Error(), event.ID)
		return
	}

	pendingConn := h.pendingConnection(joinRequestID)
	if pendingConn == nil {
		return
	}

	if approve {
		h.promotePending(pendingConn)
		_ = pendingConn.writeEvent(protocol.Event{
			Type:      "join.approved",
			ChannelID: c.channelID,
			From:      c.deviceID,
			To:        join.DeviceID,
			ReplyTo:   event.ID,
			Payload: map[string]any{
				"joinRequestId": joinRequestID,
				"deviceId":      join.DeviceID,
			},
		})
		return
	}

	_ = pendingConn.writeEvent(protocol.Event{
		Type:      "join.denied",
		ChannelID: c.channelID,
		From:      c.deviceID,
		To:        join.DeviceID,
		ReplyTo:   event.ID,
		Payload: map[string]any{
			"joinRequestId": joinRequestID,
			"deviceId":      join.DeviceID,
		},
	})
}

func joinRequestIDFromEvent(event protocol.Event) string {
	if event.Payload != nil {
		if value, ok := event.Payload["joinRequestId"].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return strings.TrimSpace(event.To)
}

func pendingJoinByDevice(ch *channel.Channel, deviceID string) (string, channel.JoinRequest, bool) {
	deviceID = strings.TrimSpace(deviceID)
	if deviceID == "" {
		return "", channel.JoinRequest{}, false
	}
	for id, join := range ch.PendingJoins {
		if join.DeviceID == deviceID {
			return id, join, true
		}
	}
	return "", channel.JoinRequest{}, false
}

func (h *Hub) sendList(c *connection, replyTo string) {
	ch := h.registry.Channel(c.channelID)
	if ch == nil {
		c.sendError("invalid_event", "channel not found", replyTo)
		return
	}

	members := make([]map[string]any, 0, len(ch.Members))
	for _, member := range ch.Members {
		members = append(members, map[string]any{
			"deviceId":   member.DeviceID,
			"deviceName": member.DeviceName,
			"online":     member.Online,
			"owner":      member.DeviceID == ch.CurrentOwnerID,
		})
	}

	_ = c.writeEvent(protocol.Event{
		Type:      "list.response",
		ChannelID: c.channelID,
		To:        c.deviceID,
		ReplyTo:   replyTo,
		Payload: map[string]any{
			"ownerId": ch.CurrentOwnerID,
			"members": members,
		},
	})
}

func (h *Hub) sendStatus(c *connection, replyTo string) {
	ch := h.registry.Channel(c.channelID)
	if ch == nil {
		c.sendError("invalid_event", "channel not found", replyTo)
		return
	}
	status := "member"
	if !c.isMember() {
		status = "pending_approval"
	}
	_ = c.writeEvent(protocol.Event{
		Type:      "status.response",
		ChannelID: c.channelID,
		To:        c.deviceID,
		ReplyTo:   replyTo,
		Payload: map[string]any{
			"status":        status,
			"channelId":     c.channelID,
			"deviceId":      c.deviceID,
			"ownerId":       ch.CurrentOwnerID,
			"joinRequestId": c.joinRequestID,
		},
	})
}

func (h *Hub) notifyOwnerOfJoin(c *connection) {
	ch := h.registry.Channel(c.channelID)
	if ch == nil {
		return
	}
	join, ok := ch.PendingJoins[c.joinRequestID]
	if !ok {
		return
	}
	owner := h.memberConnection(c.channelID, ch.CurrentOwnerID)
	if owner == nil {
		return
	}
	_ = owner.writeEvent(protocol.Event{
		Type:      "join.request",
		ChannelID: c.channelID,
		From:      c.deviceID,
		To:        owner.deviceID,
		Payload: map[string]any{
			"joinRequestId": join.ID,
			"deviceId":      join.DeviceID,
			"deviceName":    join.DeviceName,
		},
	})
}

func (h *Hub) register(c *connection) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if c.role == auth.RoleMember {
		if h.members[c.channelID] == nil {
			h.members[c.channelID] = make(map[string]*connection)
		}
		h.members[c.channelID][c.deviceID] = c
		return
	}
	h.pending[c.joinRequestID] = c
}

func (h *Hub) unregister(c *connection) {
	h.mu.Lock()
	if current := h.pending[c.joinRequestID]; current == c {
		delete(h.pending, c.joinRequestID)
	}
	removedMember := false
	if channelMembers := h.members[c.channelID]; channelMembers != nil {
		if current := channelMembers[c.deviceID]; current == c {
			delete(channelMembers, c.deviceID)
			removedMember = true
			if len(channelMembers) == 0 {
				delete(h.members, c.channelID)
			}
		}
	}
	h.mu.Unlock()

	if removedMember {
		_ = h.registry.SetOnline(c.channelID, c.deviceID, false)
	}
}

func (h *Hub) promotePending(c *connection) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if current := h.pending[c.joinRequestID]; current == c {
		delete(h.pending, c.joinRequestID)
	}
	c.mu.Lock()
	c.role = auth.RoleMember
	c.mu.Unlock()
	if h.members[c.channelID] == nil {
		h.members[c.channelID] = make(map[string]*connection)
	}
	h.members[c.channelID][c.deviceID] = c
}

func (h *Hub) memberConnection(channelID, deviceID string) *connection {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.members[channelID] == nil {
		return nil
	}
	return h.members[channelID][deviceID]
}

func (h *Hub) memberConnections(channelID string) []*connection {
	h.mu.RLock()
	defer h.mu.RUnlock()
	channelMembers := h.members[channelID]
	if len(channelMembers) == 0 {
		return nil
	}
	connections := make([]*connection, 0, len(channelMembers))
	for _, conn := range channelMembers {
		connections = append(connections, conn)
	}
	return connections
}

func (h *Hub) pendingConnection(joinRequestID string) *connection {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.pending[joinRequestID]
}

type connection struct {
	hub           *Hub
	conn          *websocket.Conn
	channelID     string
	deviceID      string
	joinRequestID string

	mu   sync.RWMutex
	role auth.Role

	writeMu sync.Mutex
}

func (c *connection) isMember() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.role == auth.RoleMember
}

func (c *connection) writeEvent(event protocol.Event) error {
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), writeTimeout)
	defer cancel()
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.conn.Write(ctx, websocket.MessageText, data)
}

func (c *connection) sendError(code, message, replyTo string) {
	_ = c.writeEvent(protocol.Event{
		Type:      "error",
		ChannelID: c.channelID,
		To:        c.deviceID,
		ReplyTo:   replyTo,
		Payload: map[string]any{
			"code":    code,
			"message": message,
		},
	})
}

func (c *connection) heartbeat(done <-chan struct{}) {
	ticker := time.NewTicker(heartbeatEvery)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			return
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(context.Background(), heartbeatTimeout)
			err := c.conn.Ping(ctx)
			cancel()
			if err != nil {
				_ = c.conn.Close(websocket.StatusPolicyViolation, "heartbeat failed")
				return
			}
		}
	}
}
