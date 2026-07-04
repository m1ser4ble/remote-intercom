package channel

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
)

type Status string

const (
	StatusCreated   Status = "created"
	StatusPending   Status = "pending_approval"
	StatusConnected Status = "connected"
)

type Registry struct {
	mu           sync.RWMutex
	channels     map[string]*Channel
	channelByKey map[string]string
	joinSeq      uint64
}

type Channel struct {
	ID             string
	Name           string
	Members        map[string]Member
	PendingJoins   map[string]JoinRequest
	CurrentOwnerID string
}

type Member struct {
	DeviceID   string
	DeviceName string
	Online     bool
	Priority   int
}

type JoinRequest struct {
	ID         string
	ChannelID  string
	DeviceID   string
	DeviceName string
}

type ConnectResult struct {
	Status      Status
	Channel     *Channel
	Member      *Member
	JoinRequest *JoinRequest
}

func NewRegistry() *Registry {
	return &Registry{
		channels:     make(map[string]*Channel),
		channelByKey: make(map[string]string),
	}
}

func (r *Registry) Connect(channelName, pin, deviceID, deviceName string) ConnectResult {
	normalizedName := normalizeChannelName(channelName)
	key := channelKey(normalizedName, pin)

	r.mu.Lock()
	defer r.mu.Unlock()

	channelID, ok := r.channelByKey[key]
	if !ok {
		member := Member{DeviceID: deviceID, DeviceName: deviceName, Online: true, Priority: 1}
		ch := &Channel{
			ID:             channelIDForKey(key),
			Name:           normalizedName,
			Members:        map[string]Member{deviceID: member},
			PendingJoins:   make(map[string]JoinRequest),
			CurrentOwnerID: deviceID,
		}
		r.channels[ch.ID] = ch
		r.channelByKey[key] = ch.ID
		return ConnectResult{Status: StatusCreated, Channel: snapshotChannel(ch), Member: copyMember(member)}
	}

	ch := r.channels[channelID]
	if member, ok := ch.Members[deviceID]; ok {
		member.DeviceName = deviceName
		member.Online = true
		ch.Members[deviceID] = member
		recomputeOwner(ch)
		return ConnectResult{Status: StatusConnected, Channel: snapshotChannel(ch), Member: copyMember(member)}
	}

	if join, ok := pendingJoinForDevice(ch, deviceID); ok {
		return ConnectResult{Status: StatusPending, Channel: snapshotChannel(ch), JoinRequest: copyJoinRequest(join)}
	}

	join := JoinRequest{
		ID:         r.nextJoinID(ch.ID, deviceID),
		ChannelID:  ch.ID,
		DeviceID:   deviceID,
		DeviceName: deviceName,
	}
	ch.PendingJoins[join.ID] = join
	return ConnectResult{Status: StatusPending, Channel: snapshotChannel(ch), JoinRequest: copyJoinRequest(join)}
}

func (r *Registry) Approve(channelID, joinRequestID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	ch, ok := r.channels[channelID]
	if !ok {
		return fmt.Errorf("channel %q not found", channelID)
	}
	join, ok := ch.PendingJoins[joinRequestID]
	if !ok {
		return fmt.Errorf("join request %q not found", joinRequestID)
	}

	if _, ok := ch.Members[join.DeviceID]; ok {
		return fmt.Errorf("device %q is already a member", join.DeviceID)
	}

	ch.Members[join.DeviceID] = Member{
		DeviceID:   join.DeviceID,
		DeviceName: join.DeviceName,
		Online:     true,
		Priority:   nextPriority(ch),
	}
	deletePendingJoinsForDevice(ch, join.DeviceID)
	recomputeOwner(ch)
	return nil
}

func (r *Registry) Deny(channelID, joinRequestID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	ch, ok := r.channels[channelID]
	if !ok {
		return fmt.Errorf("channel %q not found", channelID)
	}
	if _, ok := ch.PendingJoins[joinRequestID]; !ok {
		return fmt.Errorf("join request %q not found", joinRequestID)
	}
	delete(ch.PendingJoins, joinRequestID)
	return nil
}

func (r *Registry) SetOnline(channelID, deviceID string, online bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	ch, ok := r.channels[channelID]
	if !ok {
		return fmt.Errorf("channel %q not found", channelID)
	}
	member, ok := ch.Members[deviceID]
	if !ok {
		return fmt.Errorf("member %q not found", deviceID)
	}
	member.Online = online
	ch.Members[deviceID] = member
	recomputeOwner(ch)
	return nil
}

func (r *Registry) Channel(channelID string) *Channel {
	r.mu.RLock()
	defer r.mu.RUnlock()

	ch, ok := r.channels[channelID]
	if !ok {
		return nil
	}
	return snapshotChannel(ch)
}

func normalizeChannelName(channelName string) string {
	return strings.ToLower(strings.TrimSpace(channelName))
}

func channelKey(channelName, pin string) string {
	h := sha256.New()
	_, _ = fmt.Fprintf(h, "%d:%s%d:%s", len(channelName), channelName, len(pin), pin)
	return hex.EncodeToString(h.Sum(nil))
}

func channelIDForKey(key string) string {
	return "ch_" + key[:16]
}

func (r *Registry) nextJoinID(channelID, deviceID string) string {
	r.joinSeq++
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s:%s:%d", channelID, deviceID, r.joinSeq)))
	return "join_" + hex.EncodeToString(sum[:8])
}

func pendingJoinForDevice(ch *Channel, deviceID string) (JoinRequest, bool) {
	for _, join := range ch.PendingJoins {
		if join.DeviceID == deviceID {
			return join, true
		}
	}
	return JoinRequest{}, false
}

func deletePendingJoinsForDevice(ch *Channel, deviceID string) {
	for id, join := range ch.PendingJoins {
		if join.DeviceID == deviceID {
			delete(ch.PendingJoins, id)
		}
	}
}

func nextPriority(ch *Channel) int {
	priority := 0
	for _, member := range ch.Members {
		if member.Priority > priority {
			priority = member.Priority
		}
	}
	return priority + 1
}

func recomputeOwner(ch *Channel) {
	ownerID := ""
	ownerPriority := 0
	for id, member := range ch.Members {
		if !member.Online {
			continue
		}
		if ownerID == "" || member.Priority < ownerPriority || (member.Priority == ownerPriority && id < ownerID) {
			ownerID = id
			ownerPriority = member.Priority
		}
	}
	ch.CurrentOwnerID = ownerID
}

func snapshotChannel(ch *Channel) *Channel {
	if ch == nil {
		return nil
	}
	members := make(map[string]Member, len(ch.Members))
	for id, member := range ch.Members {
		members[id] = member
	}
	pending := make(map[string]JoinRequest, len(ch.PendingJoins))
	for id, join := range ch.PendingJoins {
		pending[id] = join
	}
	return &Channel{
		ID:             ch.ID,
		Name:           ch.Name,
		Members:        members,
		PendingJoins:   pending,
		CurrentOwnerID: ch.CurrentOwnerID,
	}
}

func copyMember(member Member) *Member {
	return &Member{
		DeviceID:   member.DeviceID,
		DeviceName: member.DeviceName,
		Online:     member.Online,
		Priority:   member.Priority,
	}
}

func copyJoinRequest(join JoinRequest) *JoinRequest {
	return &JoinRequest{
		ID:         join.ID,
		ChannelID:  join.ChannelID,
		DeviceID:   join.DeviceID,
		DeviceName: join.DeviceName,
	}
}
