package auth

import (
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

var testSecret = []byte("01234567890123456789012345678901")

func TestNewTokenManagerRejectsEmptyOrShortSecret(t *testing.T) {
	for _, secret := range [][]byte{nil, []byte(""), []byte("short-secret")} {
		if _, err := NewTokenManager(secret, time.Hour); err == nil {
			t.Fatalf("expected error for secret length %d", len(secret))
		}
	}
}

func TestNewTokenManagerRejectsNonPositiveTTL(t *testing.T) {
	for _, ttl := range []time.Duration{0, -time.Second} {
		if _, err := NewTokenManager(testSecret, ttl); err == nil {
			t.Fatalf("expected error for ttl %s", ttl)
		}
	}
}

func TestIssueAndVerifyMemberToken(t *testing.T) {
	mgr := newTestTokenManager(t)
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

func TestIssueAndVerifyPendingToken(t *testing.T) {
	mgr := newTestTokenManager(t)
	token, err := mgr.IssuePending("ch_1", "dev_1", "join_1")
	if err != nil {
		t.Fatal(err)
	}
	claims, err := mgr.Verify(token)
	if err != nil {
		t.Fatal(err)
	}
	if claims.ChannelID != "ch_1" || claims.DeviceID != "dev_1" || claims.Role != RolePending || claims.JoinRequestID != "join_1" {
		t.Fatalf("unexpected claims: %+v", claims)
	}
}

func TestSourceSecretMutationDoesNotAffectTokenManager(t *testing.T) {
	secret := []byte("01234567890123456789012345678901")
	mgr, err := NewTokenManager(secret, time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	beforeMutation, err := mgr.IssueMember("ch_1", "dev_1")
	if err != nil {
		t.Fatal(err)
	}
	for i := range secret {
		secret[i] = 'x'
	}
	if _, err := mgr.Verify(beforeMutation); err != nil {
		t.Fatalf("expected token issued before source secret mutation to verify: %v", err)
	}

	afterMutation, err := mgr.IssueMember("ch_2", "dev_2")
	if err != nil {
		t.Fatal(err)
	}
	claims, err := mgr.Verify(afterMutation)
	if err != nil {
		t.Fatalf("expected token issued after source secret mutation to verify: %v", err)
	}
	if claims.ChannelID != "ch_2" || claims.DeviceID != "dev_2" || claims.Role != RoleMember {
		t.Fatalf("unexpected claims: %+v", claims)
	}
}

func TestRejectsInvalidToken(t *testing.T) {
	mgr := newTestTokenManager(t)
	if _, err := mgr.Verify("not-a-token"); err == nil {
		t.Fatal("expected invalid token error")
	}
}

func TestRejectsExpiredToken(t *testing.T) {
	mgr := newTestTokenManager(t)
	token := signClaims(t, testSecret, Claims{
		ChannelID: "ch_1",
		DeviceID:  "dev_1",
		Role:      RoleMember,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(-time.Minute)),
		},
	})
	if _, err := mgr.Verify(token); err == nil {
		t.Fatal("expected expired token error")
	}
}

func TestRejectsTokenSignedWithWrongSecret(t *testing.T) {
	mgr := newTestTokenManager(t)
	wrongSecret := []byte("abcdefghijklmnopqrstuvwxyz123456")
	token := signClaims(t, wrongSecret, validClaims())
	if _, err := mgr.Verify(token); err == nil {
		t.Fatal("expected wrong secret token error")
	}
}

func TestRejectsTokenSignedWithNonHS256Method(t *testing.T) {
	mgr := newTestTokenManager(t)
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS384, validClaims()).SignedString(testSecret)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.Verify(token); err == nil {
		t.Fatal("expected non-HS256 signing method error")
	}
}

func TestRejectsTokenMissingExpiration(t *testing.T) {
	mgr := newTestTokenManager(t)
	token := signClaims(t, testSecret, Claims{
		ChannelID: "ch_1",
		DeviceID:  "dev_1",
		Role:      RoleMember,
	})
	if _, err := mgr.Verify(token); err == nil {
		t.Fatal("expected missing expiration error")
	}
}

func TestRejectsInvalidCustomClaims(t *testing.T) {
	cases := []struct {
		name   string
		claims Claims
	}{
		{
			name: "missing channel id",
			claims: Claims{
				DeviceID: "dev_1",
				Role:     RoleMember,
			},
		},
		{
			name: "missing device id",
			claims: Claims{
				ChannelID: "ch_1",
				Role:      RoleMember,
			},
		},
		{
			name: "invalid role",
			claims: Claims{
				ChannelID: "ch_1",
				DeviceID:  "dev_1",
				Role:      Role("admin"),
			},
		},
		{
			name: "pending without join request id",
			claims: Claims{
				ChannelID: "ch_1",
				DeviceID:  "dev_1",
				Role:      RolePending,
			},
		},
	}

	mgr := newTestTokenManager(t)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			token := signClaims(t, testSecret, withExpiration(tc.claims))
			if _, err := mgr.Verify(token); err == nil {
				t.Fatal("expected invalid custom claims error")
			}
		})
	}
}

func newTestTokenManager(t *testing.T) *TokenManager {
	t.Helper()
	mgr, err := NewTokenManager(testSecret, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return mgr
}

func validClaims() Claims {
	return withExpiration(Claims{ChannelID: "ch_1", DeviceID: "dev_1", Role: RoleMember})
}

func withExpiration(claims Claims) Claims {
	claims.ExpiresAt = jwt.NewNumericDate(time.Now().Add(time.Hour))
	return claims
}

func signClaims(t *testing.T, secret []byte, claims Claims) string {
	t.Helper()
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(secret)
	if err != nil {
		t.Fatal(err)
	}
	return token
}
