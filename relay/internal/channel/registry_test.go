package channel

import "testing"

func TestFirstConnectCreatesChannelAndOwner(t *testing.T) {
	r := NewRegistry()
	res := r.Connect("dwkim", "1234", "dev_a", "macbook")
	if res.Status != StatusCreated {
		t.Fatalf("status = %s", res.Status)
	}
	if res.Channel.CurrentOwnerID != "dev_a" {
		t.Fatalf("owner = %s", res.Channel.CurrentOwnerID)
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

func TestApproveJoinAddsMember(t *testing.T) {
	r := NewRegistry()
	r.Connect("dwkim", "1234", "dev_a", "macbook")
	pending := r.Connect("dwkim", "1234", "dev_b", "mini")
	if err := r.Approve(pending.Channel.ID, pending.JoinRequest.ID); err != nil {
		t.Fatal(err)
	}
	ch := r.Channel(pending.Channel.ID)
	if _, ok := ch.Members["dev_b"]; !ok {
		t.Fatal("dev_b not admitted")
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
	r.Connect("dwkim", "1234", "dev_a", "macbook")
	pending := r.Connect("dwkim", "1234", "dev_b", "mini")
	_ = r.Approve(pending.Channel.ID, pending.JoinRequest.ID)
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
	member := r.Connect("dwkim", "1234", "dev_b", "mini")
	if err := r.Approve(created.Channel.ID, member.JoinRequest.ID); err != nil {
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
	member := r.Connect("dwkim", "1234", "dev_b", "mini")
	if err := r.Approve(created.Channel.ID, member.JoinRequest.ID); err != nil {
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

func TestReconnectAdmittedDeviceRestoresOwner(t *testing.T) {
	r := NewRegistry()
	created := r.Connect("dwkim", "1234", "dev_a", "macbook")
	pending := r.Connect("dwkim", "1234", "dev_b", "mini")
	_ = r.Approve(pending.Channel.ID, pending.JoinRequest.ID)
	if err := r.SetOnline(created.Channel.ID, "dev_a", false); err != nil {
		t.Fatal(err)
	}
	res := r.Connect("dwkim", "1234", "dev_a", "macbook")
	if res.Status != StatusConnected {
		t.Fatalf("status = %s", res.Status)
	}
	if got := res.Channel.CurrentOwnerID; got != "dev_a" {
		t.Fatalf("owner = %s", got)
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
