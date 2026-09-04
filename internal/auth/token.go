package auth

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	minimumJWTSecretBytes = 32
	maximumJWTTTL         = 24 * time.Hour
)

var ErrInvalidToken = errors.New("invalid access token")

// Clock makes token creation and validation deterministic in tests.
type Clock func() time.Time

type IssuedToken struct {
	AccessToken string
	ExpiresIn   int64
}

type TokenService struct {
	secret []byte
	issuer string
	ttl    time.Duration
	clock  Clock
}

func NewTokenService(
	secret []byte,
	issuer string,
	ttl time.Duration,
	clock Clock,
) (*TokenService, error) {
	if len(secret) < minimumJWTSecretBytes {
		return nil, errors.New("JWT secret must be at least 32 bytes")
	}
	if strings.TrimSpace(issuer) == "" {
		return nil, errors.New("JWT issuer is required")
	}
	if ttl <= 0 || ttl > maximumJWTTTL {
		return nil, errors.New("JWT TTL must be greater than 0 and at most 24h")
	}
	if clock == nil {
		return nil, errors.New("clock is required")
	}

	secretCopy := append([]byte(nil), secret...)
	return &TokenService{
		secret: secretCopy,
		issuer: issuer,
		ttl:    ttl,
		clock:  clock,
	}, nil
}

func (service *TokenService) Issue(userID uint64) (IssuedToken, error) {
	if userID == 0 {
		return IssuedToken{}, errors.New("user ID must be positive")
	}

	now := service.clock().UTC()
	expiresAt := now.Add(service.ttl)
	claims := Claims{RegisteredClaims: jwt.RegisteredClaims{
		Subject:   strconv.FormatUint(userID, 10),
		Issuer:    service.issuer,
		IssuedAt:  jwt.NewNumericDate(now),
		ExpiresAt: jwt.NewNumericDate(expiresAt),
	}}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString(service.secret)
	if err != nil {
		return IssuedToken{}, fmt.Errorf("sign access token: %w", err)
	}

	return IssuedToken{
		AccessToken: signed,
		ExpiresIn:   int64(service.ttl / time.Second),
	}, nil
}

func (service *TokenService) Verify(rawToken string) (uint64, error) {
	claims := new(Claims)
	token, err := jwt.ParseWithClaims(
		rawToken,
		claims,
		func(token *jwt.Token) (any, error) {
			if token.Method != jwt.SigningMethodHS256 {
				return nil, ErrInvalidToken
			}
			return service.secret, nil
		},
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithIssuer(service.issuer),
		jwt.WithExpirationRequired(),
		jwt.WithIssuedAt(),
		jwt.WithTimeFunc(service.clock),
	)
	if err != nil || token == nil || !token.Valid {
		return 0, ErrInvalidToken
	}
	if claims.IssuedAt == nil || claims.ExpiresAt == nil {
		return 0, ErrInvalidToken
	}

	userID, err := strconv.ParseUint(claims.Subject, 10, 64)
	if err != nil || userID == 0 {
		return 0, ErrInvalidToken
	}

	return userID, nil
}
