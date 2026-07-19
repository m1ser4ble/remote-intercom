package ws

import (
	"context"
	"encoding/json"
	"errors"
	"log"
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
	writeTimeout           = 5 * time.Second
	heartbeatEvery         = 25 * time.Second
	heartbeatTimeout       = 75 * time.Second // pong wait timeout for the 25s heartbeat interval.
	defaultReconnectGrace  = 30 * time.Second
	DefaultMaxMessageBytes = 64 * 1024
)

// Hub manages live WebSocket connections and routes relay protocol events.
type Hub struct {
	registry *channel.Registry
	tokens   *auth.TokenManager

	mu             sync.RWMutex
	members        map[string]map[string]*connection // channelID -> deviceID -> connection
	pending        map[string]*connection            // joinRequestID -> connection
	offlineTimers  map[string]map[string]*time.Timer // channelID -> deviceID -> timer
	reconnectGrace time.Duration
}

// NewHub returns a WebSocket hub backed by the shared channel registry and token manager.
func NewHub(registry *channel.Registry, tokens *auth.TokenManager) *Hub {
	return &Hub{
		registry:       registry,
		tokens:         tokens,
		members:        make(map[string]map[string]*connection),
		pending:        make(map[string]*connection),
		offlineTimers:  make(map[string]map[string]*time.Timer),
		reconnectGrace: defaultReconnectGrace,
	}
}

// SetReconnectGrace configures the member reconnect grace period. It is intended
// for tests and local deployments that need faster ephemeral-channel cleanup.
func (h *Hub) SetReconnectGrace(duration time.Duration) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.reconnectGrace = duration
}

// ScheduleOfflineExpiry applies reconnect grace to a newly-created member
// that has not opened its WebSocket yet. Registration cancels the timer.
func (h *Hub) ScheduleOfflineExpiry(channelID, deviceID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.members[channelID] != nil && h.members[channelID][deviceID] != nil {
		return
	}
	h.startOfflineTimerLocked(channelID, deviceID)
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
	conn.SetReadLimit(DefaultMaxMessageBytes)

	client := &connection{
		hub:           h,
		conn:          conn,
		channelID:     claims.ChannelID,
		deviceID:      claims.DeviceID,
		role:          claims.Role,
		joinRequestID: claims.JoinRequestID,
	}

	previous := h.register(client)
	if client.role == auth.RoleMember {
		change, err := h.registry.SetOnlineWithChange(client.channelID, client.deviceID, true)
		if err != nil {
			h.unregister(client)
			_ = conn.Close(websocket.StatusPolicyViolation, "member is not registered")
			return
		}
		h.emitPresenceChange(change, client.deviceID)
		if change.Channel != nil && change.PreviousOwnerID == change.CurrentOwnerID && change.CurrentOwnerID == client.deviceID {
			h.notifyOwnerOfPendingJoins(change.Channel)
		}
	}
	if previous != nil {
		log.Printf("remote-intercom websocket superseding previous connection channel=%s device=%s role=%s", client.channelID, client.deviceID, client.role)
		_ = previous.close(websocket.StatusPolicyViolation, "connection superseded")
	}
	log.Printf("remote-intercom websocket connected channel=%s device=%s role=%s joinRequest=%s", client.channelID, client.deviceID, client.role, client.joinRequestID)
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
			status := websocket.CloseStatus(err)
			log.Printf("remote-intercom websocket read closed channel=%s device=%s role=%s closeStatus=%d err=%v", client.channelID, client.deviceID, client.role, status, err)
			return
		}
		if messageType != websocket.MessageText {
			client.sendError("invalid_event", "only text JSON frames are supported", "")
			continue
		}
		if len(data) > DefaultMaxMessageBytes {
			client.sendError("invalid_event", "message exceeds maximum size", "")
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
	if !h.connectionCanSend(c) {
		return
	}
	if event.ChannelID != "" && event.ChannelID != c.channelID {
		c.sendError("unauthorized", "event channel does not match token", event.ID)
		return
	}

	switch event.Type {
	case "message.send", "message.ask", "message.reply":
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
		c.sendError("invalid_event", event.Type+" requires a target", event.ID)
		return
	}

	target := h.memberConnectionByTarget(c.channelID, event.To)
	if target == nil || !target.isMember() {
		c.sendError("unknown_target", "target is not online", event.ID)
		return
	}

	event.ChannelID = c.channelID
	event.From = c.deviceID
	event.To = target.deviceID
	if event.Payload == nil {
		event.Payload = map[string]any{}
	}
	if err := target.writeEvent(event); err != nil {
		c.sendError("target_unreachable", "target is not reachable", event.ID)
		return
	}
	_ = c.writeEvent(protocol.Event{
		Type:      "message.ack",
		ChannelID: c.channelID,
		To:        c.deviceID,
		ReplyTo:   event.ID,
		Payload: map[string]any{
			"status":   "delivered",
			"deviceId": target.deviceID,
		},
	})
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
		err = h.registry.ApproveByOwner(c.channelID, c.deviceID, joinRequestID)
	} else {
		err = h.registry.DenyByOwner(c.channelID, c.deviceID, joinRequestID)
	}
	if err != nil {
		if errors.Is(err, channel.ErrNotCurrentOwner) {
			c.sendError("unauthorized", err.Error(), event.ID)
			return
		}
		c.sendError("invalid_event", err.Error(), event.ID)
		return
	}

	decision := "denied"
	if approve {
		decision = "approved"
	}
	_ = c.writeEvent(protocol.Event{
		Type:      "join.decision.ack",
		ChannelID: c.channelID,
		To:        c.deviceID,
		ReplyTo:   event.ID,
		Payload: map[string]any{
			"joinRequestId": joinRequestID,
			"deviceId":      join.DeviceID,
			"decision":      decision,
		},
	})

	pendingConn := h.pendingConnection(joinRequestID)
	if pendingConn == nil {
		return
	}

	if approve {
		previous := h.promotePending(pendingConn)
		change, err := h.registry.SetOnlineWithChange(pendingConn.channelID, pendingConn.deviceID, true)
		if err != nil {
			h.removeMember(pendingConn)
			pendingConn.sendError("invalid_event", err.Error(), event.ID)
			return
		}
		if previous != nil {
			_ = previous.close(websocket.StatusPolicyViolation, "connection superseded")
		}
		h.emitPresenceChange(change, pendingConn.deviceID)
		memberToken, err := h.tokens.IssueMember(c.channelID, join.DeviceID)
		if err != nil {
			pendingConn.sendError("internal_error", "could not issue member token", event.ID)
			return
		}
		_ = pendingConn.writeEvent(protocol.Event{
			Type:      "join.approved",
			ChannelID: c.channelID,
			From:      c.deviceID,
			To:        join.DeviceID,
			ReplyTo:   event.ID,
			Payload: map[string]any{
				"joinRequestId": joinRequestID,
				"deviceId":      join.DeviceID,
				"token":         memberToken,
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
	h.removePending(pendingConn)
	_ = pendingConn.close(websocket.StatusPolicyViolation, "join denied")
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
	if !c.isMember() {
		c.sendError("unauthorized", "pending connections cannot list members", replyTo)
		return
	}
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
	h.notifyOwnerOfJoinRequest(ch, join)
}

func (h *Hub) notifyOwnerOfPendingJoins(ch *channel.Channel) {
	if ch == nil || ch.CurrentOwnerID == "" {
		return
	}
	for _, join := range ch.PendingJoins {
		h.notifyOwnerOfJoinRequest(ch, join)
	}
}

func (h *Hub) notifyOwnerOfJoinRequest(ch *channel.Channel, join channel.JoinRequest) {
	owner := h.memberConnection(ch.ID, ch.CurrentOwnerID)
	if owner == nil {
		return
	}
	_ = owner.writeEvent(protocol.Event{
		Type:      "join.request",
		ChannelID: ch.ID,
		From:      join.DeviceID,
		To:        owner.deviceID,
		Payload: map[string]any{
			"joinRequestId": join.ID,
			"deviceId":      join.DeviceID,
			"deviceName":    join.DeviceName,
		},
	})
}

func (h *Hub) emitPresenceChange(change *channel.PresenceChange, deviceID string) {
	if change == nil || change.Deleted || change.Channel == nil || !change.Changed {
		return
	}
	if change.PreviousOwnerID != change.CurrentOwnerID {
		h.broadcastOwnerChanged(change.Channel, change.PreviousOwnerID, change.CurrentOwnerID, deviceID)
		h.notifyOwnerOfPendingJoins(change.Channel)
	}
	h.broadcastPresenceChanged(change.Channel, deviceID)
}

func (h *Hub) broadcastOwnerChanged(ch *channel.Channel, previousOwnerID, currentOwnerID, excludeDeviceID string) {
	for _, target := range h.memberConnections(ch.ID) {
		if target.deviceID == excludeDeviceID {
			continue
		}
		_ = target.writeEvent(protocol.Event{
			Type:      "owner.changed",
			ChannelID: ch.ID,
			To:        target.deviceID,
			Payload: map[string]any{
				"previousOwnerId": previousOwnerID,
				"ownerId":         currentOwnerID,
			},
		})
	}
}

func (h *Hub) broadcastPresenceChanged(ch *channel.Channel, deviceID string) {
	member, ok := ch.Members[deviceID]
	if !ok {
		return
	}
	for _, target := range h.memberConnections(ch.ID) {
		if target.deviceID == deviceID {
			continue
		}
		_ = target.writeEvent(protocol.Event{
			Type:      "presence.changed",
			ChannelID: ch.ID,
			To:        target.deviceID,
			Payload: map[string]any{
				"deviceId":   member.DeviceID,
				"deviceName": member.DeviceName,
				"online":     member.Online,
				"ownerId":    ch.CurrentOwnerID,
			},
		})
	}
}

func (h *Hub) connectionCanSend(c *connection) bool {
	if c.isMember() {
		if h.isCurrentMember(c) {
			return true
		}
		_ = c.close(websocket.StatusPolicyViolation, "connection superseded")
		return false
	}

	if h.registry.PendingExists(c.channelID, c.deviceID, c.joinRequestID) {
		if h.isCurrentPending(c) {
			return true
		}
		_ = c.close(websocket.StatusPolicyViolation, "connection superseded")
		return false
	}

	if h.registryMemberExists(c.channelID, c.deviceID) && (h.isCurrentPending(c) || h.isCurrentMember(c)) {
		return true
	}

	h.removePending(c)
	_ = c.close(websocket.StatusPolicyViolation, "join request is no longer pending")
	return false
}

func (h *Hub) registryMemberExists(channelID, deviceID string) bool {
	ch := h.registry.Channel(channelID)
	if ch == nil {
		return false
	}
	_, ok := ch.Members[deviceID]
	return ok
}

func (h *Hub) register(c *connection) *connection {
	h.mu.Lock()
	defer h.mu.Unlock()

	if c.role == auth.RoleMember {
		h.cancelOfflineTimerLocked(c.channelID, c.deviceID)
		if h.members[c.channelID] == nil {
			h.members[c.channelID] = make(map[string]*connection)
		}
		previous := h.members[c.channelID][c.deviceID]
		h.members[c.channelID][c.deviceID] = c
		if previous != c {
			return previous
		}
		return nil
	}
	previous := h.pending[c.joinRequestID]
	h.pending[c.joinRequestID] = c
	if previous != c {
		return previous
	}
	return nil
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
	if removedMember {
		log.Printf("remote-intercom websocket disconnected channel=%s device=%s role=%s reconnectGrace=%s", c.channelID, c.deviceID, c.role, h.reconnectGrace)
		h.startOfflineTimerLocked(c.channelID, c.deviceID)
	} else if c.role == auth.RolePending {
		log.Printf("remote-intercom pending websocket disconnected channel=%s device=%s joinRequest=%s", c.channelID, c.deviceID, c.joinRequestID)
	}
	h.mu.Unlock()
}

func (h *Hub) removePending(c *connection) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if current := h.pending[c.joinRequestID]; current == c {
		delete(h.pending, c.joinRequestID)
	}
}

func (h *Hub) removeMember(c *connection) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if channelMembers := h.members[c.channelID]; channelMembers != nil {
		if current := channelMembers[c.deviceID]; current == c {
			delete(channelMembers, c.deviceID)
			if len(channelMembers) == 0 {
				delete(h.members, c.channelID)
			}
		}
	}
}

func (h *Hub) promotePending(c *connection) *connection {
	h.mu.Lock()
	if current := h.pending[c.joinRequestID]; current == c {
		delete(h.pending, c.joinRequestID)
	}
	h.cancelOfflineTimerLocked(c.channelID, c.deviceID)
	if h.members[c.channelID] == nil {
		h.members[c.channelID] = make(map[string]*connection)
	}
	previous := h.members[c.channelID][c.deviceID]
	h.members[c.channelID][c.deviceID] = c
	h.mu.Unlock()

	c.mu.Lock()
	c.role = auth.RoleMember
	c.mu.Unlock()

	if previous != c {
		return previous
	}
	return nil
}

func (h *Hub) startOfflineTimerLocked(channelID, deviceID string) {
	h.cancelOfflineTimerLocked(channelID, deviceID)
	grace := h.reconnectGrace
	if h.offlineTimers[channelID] == nil {
		h.offlineTimers[channelID] = make(map[string]*time.Timer)
	}
	h.offlineTimers[channelID][deviceID] = time.AfterFunc(grace, func() {
		h.expireOfflineMember(channelID, deviceID)
	})
}

func (h *Hub) cancelOfflineTimerLocked(channelID, deviceID string) {
	channelTimers := h.offlineTimers[channelID]
	if channelTimers == nil {
		return
	}
	if timer := channelTimers[deviceID]; timer != nil {
		timer.Stop()
		delete(channelTimers, deviceID)
	}
	if len(channelTimers) == 0 {
		delete(h.offlineTimers, channelID)
	}
}

func (h *Hub) expireOfflineMember(channelID, deviceID string) {
	h.mu.Lock()
	if channelTimers := h.offlineTimers[channelID]; channelTimers != nil {
		delete(channelTimers, deviceID)
		if len(channelTimers) == 0 {
			delete(h.offlineTimers, channelID)
		}
	}
	if channelMembers := h.members[channelID]; channelMembers != nil && channelMembers[deviceID] != nil {
		h.mu.Unlock()
		return
	}
	// Keep hub membership and registry expiry atomic so a reconnect cannot
	// register between the final live-connection check and channel deletion.
	change, err := h.registry.ExpireOfflineMember(channelID, deviceID)
	h.mu.Unlock()
	if err != nil {
		log.Printf("remote-intercom offline expiry skipped channel=%s device=%s err=%v", channelID, deviceID, err)
		return
	}
	if change.Deleted {
		log.Printf("remote-intercom channel expired after offline grace channel=%s device=%s", channelID, deviceID)
		h.closePendingForChannel(channelID)
		return
	}
	log.Printf("remote-intercom member marked offline channel=%s device=%s owner=%s", channelID, deviceID, change.CurrentOwnerID)
	h.emitPresenceChange(change, deviceID)
}

func (h *Hub) isCurrentMember(c *connection) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.members[c.channelID] == nil {
		return false
	}
	return h.members[c.channelID][c.deviceID] == c
}

func (h *Hub) isCurrentPending(c *connection) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.pending[c.joinRequestID] == c
}

func (h *Hub) memberConnection(channelID, deviceID string) *connection {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.members[channelID] == nil {
		return nil
	}
	return h.members[channelID][deviceID]
}

func (h *Hub) memberConnectionByTarget(channelID, target string) *connection {
	target = strings.TrimSpace(target)
	if target == "" {
		return nil
	}
	if conn := h.memberConnection(channelID, target); conn != nil {
		return conn
	}
	ch := h.registry.Channel(channelID)
	if ch == nil {
		return nil
	}
	for _, member := range ch.Members {
		if !member.Online || member.DeviceName != target {
			continue
		}
		if conn := h.memberConnection(channelID, member.DeviceID); conn != nil {
			return conn
		}
	}
	return nil
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

func (h *Hub) closePendingForChannel(channelID string) {
	h.mu.Lock()
	var pending []*connection
	for joinRequestID, conn := range h.pending {
		if conn.channelID == channelID {
			pending = append(pending, conn)
			delete(h.pending, joinRequestID)
		}
	}
	h.mu.Unlock()

	for _, conn := range pending {
		_ = conn.close(websocket.StatusPolicyViolation, "channel deleted")
	}
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
	if len(data) > DefaultMaxMessageBytes {
		return errors.New("event exceeds maximum message size")
	}

	ctx, cancel := context.WithTimeout(context.Background(), writeTimeout)
	defer cancel()
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.conn.Write(ctx, websocket.MessageText, data)
}

func (c *connection) close(code websocket.StatusCode, reason string) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.conn.Close(code, reason)
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
				log.Printf("remote-intercom websocket heartbeat failed channel=%s device=%s role=%s timeout=%s err=%v", c.channelID, c.deviceID, c.role, heartbeatTimeout, err)
				_ = c.conn.Close(websocket.StatusPolicyViolation, "heartbeat failed")
				return
			}
		}
	}
}
