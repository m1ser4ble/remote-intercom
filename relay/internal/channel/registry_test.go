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
