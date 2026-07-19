package channel

import (
	"errors"
	"testing"
)

func TestFirstConnectCreatesOfflineChannelMember(t *testing.T) {
	r := NewRegistry()
	res := r.Connect("dwkim", "1234", "dev_a", "macbook")
	if res.Status != StatusCreated {
		t.Fatalf("status = %s", res.Status)
	}
	if res.Channel.CurrentOwnerID != "" {
		t.Fatalf("owner = %s, want empty until websocket connects", res.Channel.CurrentOwnerID)
	}
	if res.Member == nil || res.Member.Online {
		t.Fatalf("member = %+v, want offline until websocket connects", res.Member)
	}
}

func TestSecondConnectCreatesPendingJoin(t *testing.T) {
	r := NewRegistry()
	r.Connect("dwkim", "1234", "dev_a", "macbook")
	res := r.Connect("dwkim", "1234", "dev_b", "mini")
	if res.Status != StatusPending {
		t.Fatalf("status = %s", res.Status)
	}
	if res.JoinRequest == nil {
		t.Fatal("missing join request")
	}
}

func TestApproveJoinAddsOfflineMember(t *testing.T) {
	r := NewRegistry()
	r.Connect("dwkim", "1234", "dev_a", "macbook")
	pending := r.Connect("dwkim", "1234", "dev_b", "mini")
	if err := r.Approve(pending.Channel.ID, pending.JoinRequest.ID); err != nil {
		t.Fatal(err)
	}
	ch := r.Channel(pending.Channel.ID)
	member, ok := ch.Members["dev_b"]
	if !ok {
		t.Fatal("dev_b not admitted")
	}
	if member.Online {
		t.Fatal("approved member should remain offline until websocket connects")
	}
}

func TestConnectRejectsWhenPendingLimitReached(t *testing.T) {
	r := NewRegistry()
	r.SetLimits(32, 1)
	r.Connect("dwkim", "1234", "dev_a", "macbook")
	first := r.Connect("dwkim", "1234", "dev_b", "mini")
	if first.Err != nil || first.Status != StatusPending {
		t.Fatalf("first pending = status %s err %v", first.Status, first.Err)
	}
	second := r.Connect("dwkim", "1234", "dev_c", "phone")
	if second.Err == nil {
		t.Fatal("expected pending limit error")
	}
	if !errors.Is(second.Err, ErrLimitExceeded) {
		t.Fatalf("err = %v, want ErrLimitExceeded", second.Err)
	}
	if got := len(r.Channel(first.Channel.ID).PendingJoins); got != 1 {
		t.Fatalf("pending joins = %d, want 1", got)
	}
}

func TestConnectRejectsWhenMemberLimitReached(t *testing.T) {
	r := NewRegistry()
	r.SetLimits(1, 16)
	created := r.Connect("dwkim", "1234", "dev_a", "macbook")
	if created.Err != nil {
		t.Fatal(created.Err)
	}
	reconnected := r.Connect("dwkim", "1234", "dev_a", "macbook-renamed")
	if reconnected.Err != nil || reconnected.Status != StatusConnected {
		t.Fatalf("reconnect = status %s err %v", reconnected.Status, reconnected.Err)
	}
	pending := r.Connect("dwkim", "1234", "dev_b", "mini")
	if pending.Err == nil {
		t.Fatal("expected member limit error")
	}
	if !errors.Is(pending.Err, ErrLimitExceeded) {
		t.Fatalf("err = %v, want ErrLimitExceeded", pending.Err)
	}
}

func TestRepeatedPendingConnectReturnsExistingJoin(t *testing.T) {
	r := NewRegistry()
	r.Connect("dwkim", "1234", "dev_a", "macbook")
	first := r.Connect("dwkim", "1234", "dev_b", "mini")
	second := r.Connect("dwkim", "1234", "dev_b", "renamed")

	if second.Status != StatusPending {
		t.Fatalf("status = %s", second.Status)
	}
	if first.JoinRequest == nil || second.JoinRequest == nil {
		t.Fatal("missing join request")
	}
	if second.JoinRequest.ID != first.JoinRequest.ID {
		t.Fatalf("join request id = %s, want %s", second.JoinRequest.ID, first.JoinRequest.ID)
	}
	ch := r.Channel(first.Channel.ID)
	if got := len(ch.PendingJoins); got != 1 {
		t.Fatalf("pending joins = %d, want 1", got)
	}
}

func TestApproveRemovesAllPendingJoinsForDevice(t *testing.T) {
	r := NewRegistry()
	r.Connect("dwkim", "1234", "dev_a", "macbook")
	pending := r.Connect("dwkim", "1234", "dev_b", "mini")

	r.mu.Lock()
	r.channels[pending.Channel.ID].PendingJoins["stale_join"] = JoinRequest{
		ID:         "stale_join",
		ChannelID:  pending.Channel.ID,
		DeviceID:   "dev_b",
		DeviceName: "mini stale",
	}
	r.mu.Unlock()

	if err := r.Approve(pending.Channel.ID, pending.JoinRequest.ID); err != nil {
		t.Fatal(err)
	}
	ch := r.Channel(pending.Channel.ID)
	for id, join := range ch.PendingJoins {
		if join.DeviceID == "dev_b" {
			t.Fatalf("pending join %s for approved device remains", id)
		}
	}
}

func TestApproveStaleJoinForExistingMemberReturnsErrorWithoutMutation(t *testing.T) {
	r := NewRegistry()
	r.Connect("dwkim", "1234", "dev_a", "macbook")
	pending := r.Connect("dwkim", "1234", "dev_b", "mini")
	if err := r.Approve(pending.Channel.ID, pending.JoinRequest.ID); err != nil {
		t.Fatal(err)
	}
	before := r.Channel(pending.Channel.ID).Members["dev_b"]

	r.mu.Lock()
	r.channels[pending.Channel.ID].PendingJoins["stale_join"] = JoinRequest{
		ID:         "stale_join",
		ChannelID:  pending.Channel.ID,
		DeviceID:   "dev_b",
		DeviceName: "mutated",
	}
	r.mu.Unlock()

	if err := r.Approve(pending.Channel.ID, "stale_join"); err == nil {
		t.Fatal("expected stale approval error")
	}
	after := r.Channel(pending.Channel.ID).Members["dev_b"]
	if after != before {
		t.Fatalf("member mutated: got %+v, want %+v", after, before)
	}
}

func TestOwnerFailoverAndRestore(t *testing.T) {
	r := NewRegistry()
	created := r.Connect("dwkim", "1234", "dev_a", "macbook")
	if err := r.SetOnline(created.Channel.ID, "dev_a", true); err != nil {
		t.Fatal(err)
	}
	pending := r.Connect("dwkim", "1234", "dev_b", "mini")
	_ = r.Approve(pending.Channel.ID, pending.JoinRequest.ID)
	if err := r.SetOnline(pending.Channel.ID, "dev_b", true); err != nil {
		t.Fatal(err)
	}
	r.SetOnline(pending.Channel.ID, "dev_a", false)
	if got := r.Channel(pending.Channel.ID).CurrentOwnerID; got != "dev_b" {
		t.Fatalf("owner = %s", got)
	}
	r.SetOnline(pending.Channel.ID, "dev_a", true)
	if got := r.Channel(pending.Channel.ID).CurrentOwnerID; got != "dev_a" {
		t.Fatalf("owner = %s", got)
	}
}

func TestStaleOwnerCannotApproveAfterFailover(t *testing.T) {
	r := NewRegistry()
	created := r.Connect("dwkim", "1234", "dev_a", "macbook")
	if err := r.SetOnline(created.Channel.ID, "dev_a", true); err != nil {
		t.Fatal(err)
	}
	member := r.Connect("dwkim", "1234", "dev_b", "mini")
	if err := r.Approve(created.Channel.ID, member.JoinRequest.ID); err != nil {
		t.Fatal(err)
	}
	if err := r.SetOnline(created.Channel.ID, "dev_b", true); err != nil {
		t.Fatal(err)
	}
	if err := r.SetOnline(created.Channel.ID, "dev_a", false); err != nil {
		t.Fatal(err)
	}
	pending := r.Connect("dwkim", "1234", "dev_c", "phone")

	if err := r.ApproveByOwner(created.Channel.ID, "dev_a", pending.JoinRequest.ID); err == nil {
		t.Fatal("expected stale owner approval error")
	}
	ch := r.Channel(created.Channel.ID)
	if _, ok := ch.Members["dev_c"]; ok {
		t.Fatal("stale owner admitted dev_c")
	}
	if _, ok := ch.PendingJoins[pending.JoinRequest.ID]; !ok {
		t.Fatal("stale owner removed pending join")
	}
}

func TestStaleOwnerCannotDenyAfterFailover(t *testing.T) {
	r := NewRegistry()
	created := r.Connect("dwkim", "1234", "dev_a", "macbook")
	if err := r.SetOnline(created.Channel.ID, "dev_a", true); err != nil {
		t.Fatal(err)
	}
	member := r.Connect("dwkim", "1234", "dev_b", "mini")
	if err := r.Approve(created.Channel.ID, member.JoinRequest.ID); err != nil {
		t.Fatal(err)
	}
	if err := r.SetOnline(created.Channel.ID, "dev_b", true); err != nil {
		t.Fatal(err)
	}
	if err := r.SetOnline(created.Channel.ID, "dev_a", false); err != nil {
		t.Fatal(err)
	}
	pending := r.Connect("dwkim", "1234", "dev_c", "phone")

	if err := r.DenyByOwner(created.Channel.ID, "dev_a", pending.JoinRequest.ID); err == nil {
		t.Fatal("expected stale owner denial error")
	}
	ch := r.Channel(created.Channel.ID)
	if _, ok := ch.PendingJoins[pending.JoinRequest.ID]; !ok {
		t.Fatal("stale owner removed pending join")
	}
}

func TestDenyRemovesPendingJoin(t *testing.T) {
	r := NewRegistry()
	r.Connect("dwkim", "1234", "dev_a", "macbook")
	pending := r.Connect("dwkim", "1234", "dev_b", "mini")
	if err := r.Deny(pending.Channel.ID, pending.JoinRequest.ID); err != nil {
		t.Fatal(err)
	}
	ch := r.Channel(pending.Channel.ID)
	if _, ok := ch.PendingJoins[pending.JoinRequest.ID]; ok {
		t.Fatal("join request still pending")
	}
	if _, ok := ch.Members["dev_b"]; ok {
		t.Fatal("denied device admitted")
	}
}

func TestReconnectAdmittedDeviceRestoresOwnerWhenWebSocketConnects(t *testing.T) {
	r := NewRegistry()
	created := r.Connect("dwkim", "1234", "dev_a", "macbook")
	if err := r.SetOnline(created.Channel.ID, "dev_a", true); err != nil {
		t.Fatal(err)
	}
	pending := r.Connect("dwkim", "1234", "dev_b", "mini")
	_ = r.Approve(pending.Channel.ID, pending.JoinRequest.ID)
	if err := r.SetOnline(created.Channel.ID, "dev_b", true); err != nil {
		t.Fatal(err)
	}
	if err := r.SetOnline(created.Channel.ID, "dev_a", false); err != nil {
		t.Fatal(err)
	}
	res := r.Connect("dwkim", "1234", "dev_a", "macbook")
	if res.Status != StatusConnected {
		t.Fatalf("status = %s", res.Status)
	}
	if got := res.Channel.CurrentOwnerID; got != "dev_b" {
		t.Fatalf("owner after HTTP connect = %s, want dev_b", got)
	}
	if err := r.SetOnline(created.Channel.ID, "dev_a", true); err != nil {
		t.Fatal(err)
	}
	if got := r.Channel(created.Channel.ID).CurrentOwnerID; got != "dev_a" {
		t.Fatalf("owner after websocket connect = %s", got)
	}
}

func TestExpireLastOfflineMemberDeletesChannelKey(t *testing.T) {
	r := NewRegistry()
	created := r.Connect("dwkim", "1234", "dev_a", "macbook")
	if err := r.SetOnline(created.Channel.ID, "dev_a", true); err != nil {
		t.Fatal(err)
	}

	change, err := r.ExpireOfflineMember(created.Channel.ID, "dev_a")
	if err != nil {
		t.Fatal(err)
	}
	if !change.Deleted {
		t.Fatal("expected channel deletion")
	}
	if ch := r.Channel(created.Channel.ID); ch != nil {
		t.Fatalf("channel remains after deletion: %+v", ch)
	}

	fresh := r.Connect("dwkim", "1234", "dev_a", "macbook")
	if fresh.Status != StatusCreated {
		t.Fatalf("fresh status = %s, want %s", fresh.Status, StatusCreated)
	}
}

func TestSameChannelNameDifferentPINCreatesDifferentChannel(t *testing.T) {
	r := NewRegistry()
	first := r.Connect("dwkim", "1234", "dev_a", "macbook")
	second := r.Connect("dwkim", "5678", "dev_b", "mini")
	if second.Status != StatusCreated {
		t.Fatalf("status = %s", second.Status)
	}
	if first.Channel.ID == second.Channel.ID {
		t.Fatal("expected different channels")
	}
}

func TestChannelKeyDoesNotCollideOnDelimiterAmbiguity(t *testing.T) {
	r := NewRegistry()
	first := r.Connect("team:123", "4", "dev_a", "macbook")
	second := r.Connect("team", "123:4", "dev_b", "mini")
	if second.Status != StatusCreated {
		t.Fatalf("status = %s", second.Status)
	}
	if first.Channel.ID == second.Channel.ID {
		t.Fatal("expected delimiter-ambiguous inputs to create different channels")
	}
}

func TestChannelNameNormalizationUsesSameChannel(t *testing.T) {
	r := NewRegistry()
	created := r.Connect(" DWKIM ", "1234", "dev_a", "macbook")
	pending := r.Connect("dwkim", "1234", "dev_b", "mini")
	if pending.Status != StatusPending {
		t.Fatalf("status = %s", pending.Status)
	}
	if pending.Channel.ID != created.Channel.ID {
		t.Fatalf("channel id = %s, want %s", pending.Channel.ID, created.Channel.ID)
	}
}
