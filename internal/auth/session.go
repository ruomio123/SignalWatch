package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

var ErrInvalidSession = errors.New("invalid or expired login session")

// Session has a fixed lifetime. Refreshing an access token never extends it.
// The cookie secret itself never crosses the persistence boundary.
type Session struct {
	TokenHash string
	UserID    uint64
	ExpiresAt time.Time
	CreatedAt time.Time
}
type SessionStore interface {
	Create(context.Context, Session) error
	FindActive(context.Context, string, time.Time) (Session, error)
	Delete(context.Context, string) error
}
type SessionTokens interface {
	IssueUntil(uint64, time.Time) (IssuedToken, error)
}
type SessionService struct {
	store  SessionStore
	tokens SessionTokens
	ttl    time.Duration
	clock  Clock
}

func NewSessionService(store SessionStore, tokens SessionTokens, ttl time.Duration, clock Clock) (*SessionService, error) {
	if store == nil || tokens == nil || clock == nil {
		return nil, errors.New("session dependencies are required")
	}
	if ttl < time.Hour || ttl > 30*24*time.Hour {
		return nil, errors.New("session TTL must be between 1h and 720h")
	}
	return &SessionService{store: store, tokens: tokens, ttl: ttl, clock: clock}, nil
}
func (s *SessionService) IssueSession(ctx context.Context, userID uint64) (LoginResult, error) {
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return LoginResult{}, fmt.Errorf("generate session: %w", err)
	}
	raw := base64.RawURLEncoding.EncodeToString(secret)
	now := s.clock().UTC()
	expires := now.Add(s.ttl)
	token, err := s.tokens.IssueUntil(userID, expires)
	if err != nil {
		return LoginResult{}, err
	}
	if err = s.store.Create(ctx, Session{TokenHash: sessionHash(raw), UserID: userID, ExpiresAt: expires, CreatedAt: now}); err != nil {
		return LoginResult{}, fmt.Errorf("create session: %w", err)
	}
	return LoginResult{AccessToken: token.AccessToken, ExpiresIn: token.ExpiresIn, RefreshToken: raw, SessionExpiresAt: expires}, nil
}
func (s *SessionService) Refresh(ctx context.Context, raw string) (IssuedToken, error) {
	if !validSessionSecret(raw) {
		return IssuedToken{}, ErrInvalidSession
	}
	session, err := s.store.FindActive(ctx, sessionHash(raw), s.clock().UTC())
	if err != nil {
		return IssuedToken{}, err
	}
	return s.tokens.IssueUntil(session.UserID, session.ExpiresAt)
}
func (s *SessionService) Logout(ctx context.Context, raw string) error {
	if !validSessionSecret(raw) {
		return nil
	}
	return s.store.Delete(ctx, sessionHash(raw))
}
func sessionHash(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}
func validSessionSecret(raw string) bool {
	if len(raw) != 43 {
		return false
	}
	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	return err == nil && len(decoded) == 32 && base64.RawURLEncoding.EncodeToString(decoded) == raw
}
