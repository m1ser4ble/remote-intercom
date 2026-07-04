package auth

import (
	"testing"
	"time"
)

func TestIssueAndVerifyMemberToken(t *testing.T) {
	mgr := NewTokenManager([]byte("01234567890123456789012345678901"), time.Hour)
	token, err := mgr.IssueMember("ch_1", "dev_1")
	if err != nil {
		t.Fatal(err)
	}
	claims, err := mgr.Verify(token)
	if err != nil {
		t.Fatal(err)
	}
	if claims.ChannelID != "ch_1" || claims.DeviceID != "dev_1" || claims.Role != RoleMember {
		t.Fatalf("unexpected claims: %+v", claims)
	}
}

func TestRejectsInvalidToken(t *testing.T) {
	mgr := NewTokenManager([]byte("01234567890123456789012345678901"), time.Hour)
	if _, err := mgr.Verify("not-a-token"); err == nil {
		t.Fatal("expected invalid token error")
	}
}
