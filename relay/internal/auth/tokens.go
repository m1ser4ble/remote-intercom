package auth

import (
	"errors"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

type Role string

const (
	RoleMember  Role = "member"
	RolePending Role = "pending"
)

type Claims struct {
	ChannelID     string `json:"channelId"`
	DeviceID      string `json:"deviceId"`
	Role          Role   `json:"role"`
	JoinRequestID string `json:"joinRequestId,omitempty"`
	jwt.RegisteredClaims
}

type TokenManager struct {
	secret []byte
	ttl    time.Duration
}

func NewTokenManager(secret []byte, ttl time.Duration) *TokenManager {
	return &TokenManager{secret: secret, ttl: ttl}
}

func (m *TokenManager) IssueMember(channelID, deviceID string) (string, error) {
	return m.issue(Claims{ChannelID: channelID, DeviceID: deviceID, Role: RoleMember})
}

func (m *TokenManager) IssuePending(channelID, deviceID, joinRequestID string) (string, error) {
	return m.issue(Claims{ChannelID: channelID, DeviceID: deviceID, Role: RolePending, JoinRequestID: joinRequestID})
}

func (m *TokenManager) issue(claims Claims) (string, error) {
	now := time.Now()
	claims.RegisteredClaims = jwt.RegisteredClaims{IssuedAt: jwt.NewNumericDate(now), ExpiresAt: jwt.NewNumericDate(now.Add(m.ttl))}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(m.secret)
}

func (m *TokenManager) Verify(raw string) (*Claims, error) {
	token, err := jwt.ParseWithClaims(raw, &Claims{}, func(token *jwt.Token) (any, error) {
		if token.Method != jwt.SigningMethodHS256 {
			return nil, errors.New("unexpected signing method")
		}
		return m.secret, nil
	})
	if err != nil {
		return nil, err
	}
	claims, ok := token.Claims.(*Claims)
	if !ok || !token.Valid {
		return nil, errors.New("invalid token")
	}
	return claims, nil
}
