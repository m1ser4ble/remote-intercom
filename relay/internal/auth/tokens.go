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

func (c Claims) Validate() error {
	return validateClaims(&c)
}

type TokenManager struct {
	secret []byte
	ttl    time.Duration
}

func NewTokenManager(secret []byte, ttl time.Duration) (*TokenManager, error) {
	if len(secret) < 32 {
		return nil, errors.New("token secret must be at least 32 bytes")
	}
	if ttl <= 0 {
		return nil, errors.New("token ttl must be positive")
	}
	secretCopy := make([]byte, len(secret))
	copy(secretCopy, secret)
	return &TokenManager{secret: secretCopy, ttl: ttl}, nil
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
	claims := &Claims{}
	token, err := jwt.ParseWithClaims(raw, claims, func(token *jwt.Token) (any, error) {
		if token.Method.Alg() != jwt.SigningMethodHS256.Alg() {
			return nil, errors.New("unexpected signing method")
		}
		return m.secret, nil
	}, jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}), jwt.WithExpirationRequired())
	if err != nil {
		return nil, err
	}
	if !token.Valid {
		return nil, errors.New("invalid token")
	}
	if err := validateClaims(claims); err != nil {
		return nil, err
	}
	return claims, nil
}

func validateClaims(claims *Claims) error {
	if claims.ChannelID == "" {
		return errors.New("missing channel id")
	}
	if claims.DeviceID == "" {
		return errors.New("missing device id")
	}
	switch claims.Role {
	case RoleMember:
		return nil
	case RolePending:
		if claims.JoinRequestID == "" {
			return errors.New("missing join request id")
		}
		return nil
	default:
		return errors.New("invalid role")
	}
}
