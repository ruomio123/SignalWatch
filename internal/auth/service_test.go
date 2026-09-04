package auth

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"

	"signalwatch/internal/user"
)

const testJWTSecret = "test-secret-with-at-least-thirty-two-bytes"

func TestServiceLoginNormalizesEmailAndIssuesToken(t *testing.T) {
	passwordHash, err := bcrypt.GenerateFromPassword(
		[]byte("correct-horse-123"),
		bcrypt.MinCost,
	)
	if err != nil {
		t.Fatalf("generate password hash: %v", err)
	}

	var capturedEmail string
	repository := authRepositoryStub{
		findByEmail: func(_ context.Context, email string) (user.User, error) {
			capturedEmail = email
			return user.User{
				ID:           42,
				PasswordHash: string(passwordHash),
				Status:       user.StatusActive,
			}, nil
		},
	}
	issueCalls := 0
	tokens := tokenIssuerStub{
		issue: func(userID uint64) (IssuedToken, error) {
			issueCalls++
			if userID != 42 {
				t.Fatalf("expected user ID 42, got %d", userID)
			}
			return IssuedToken{AccessToken: "signed-token", ExpiresIn: 900}, nil
		},
	}

	result, err := NewService(repository, tokens).Login(
		context.Background(),
		LoginInput{Email: "  Alice@Example.COM ", Password: "correct-horse-123"},
	)
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if capturedEmail != "alice@example.com" {
		t.Fatalf("expected normalized email, got %q", capturedEmail)
	}
	if issueCalls != 1 {
		t.Fatalf("expected one token issue, got %d", issueCalls)
	}
	if result.AccessToken != "signed-token" || result.ExpiresIn != 900 {
		t.Fatalf("unexpected login result %+v", result)
	}
}

func TestServiceLoginCredentialFailuresAreIndistinguishable(t *testing.T) {
	passwordHash, err := bcrypt.GenerateFromPassword(
		[]byte("correct-horse-123"),
		bcrypt.MinCost,
	)
	if err != nil {
		t.Fatalf("generate password hash: %v", err)
	}
	if _, err := bcrypt.Cost([]byte(missingUserPasswordHash)); err != nil {
		t.Fatalf("missing-user hash must be a valid fixed bcrypt hash: %v", err)
	}

	tests := []struct {
		name       string
		storedUser user.User
		findErr    error
		password   string
	}{
		{
			name: "wrong password",
			storedUser: user.User{
				ID: 42, PasswordHash: string(passwordHash), Status: user.StatusActive,
			},
			password: "wrong-password",
		},
		{
			name:     "missing email",
			findErr:  user.ErrNotFound,
			password: "wrong-password",
		},
		{
			name: "inactive user",
			storedUser: user.User{
				ID: 42, PasswordHash: string(passwordHash), Status: "disabled",
			},
			password: "correct-horse-123",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			issueCalls := 0
			service := NewService(
				authRepositoryStub{findByEmail: func(context.Context, string) (user.User, error) {
					return test.storedUser, test.findErr
				}},
				tokenIssuerStub{issue: func(uint64) (IssuedToken, error) {
					issueCalls++
					return IssuedToken{}, nil
				}},
			)

			_, err := service.Login(context.Background(), LoginInput{
				Email: "alice@example.com", Password: test.password,
			})
			if !errors.Is(err, ErrInvalidCredentials) {
				t.Fatalf("expected the common credentials error, got %v", err)
			}
			if issueCalls != 0 {
				t.Fatalf("expected no token issue, got %d calls", issueCalls)
			}
		})
	}
}

func TestServiceLoginPreservesOperationalErrors(t *testing.T) {
	databaseError := errors.New("database unavailable")
	service := NewService(
		authRepositoryStub{findByEmail: func(context.Context, string) (user.User, error) {
			return user.User{}, databaseError
		}},
		tokenIssuerStub{issue: func(uint64) (IssuedToken, error) {
			t.Fatal("token issuer must not be called")
			return IssuedToken{}, nil
		}},
	)

	_, err := service.Login(context.Background(), LoginInput{})
	if !errors.Is(err, databaseError) {
		t.Fatalf("expected wrapped database error, got %v", err)
	}
}

func TestTokenServiceIssuesRequiredClaimsAndVerifiesUserID(t *testing.T) {
	now := time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC)
	service := newTestTokenService(t, testJWTSecret, "signalwatch-api", time.Hour, func() time.Time {
		return now
	})

	issued, err := service.Issue(42)
	if err != nil {
		t.Fatalf("issue token: %v", err)
	}
	if issued.ExpiresIn != 3600 {
		t.Fatalf("expected expires_in 3600, got %d", issued.ExpiresIn)
	}

	claims := new(Claims)
	parsed, err := jwt.NewParser(jwt.WithoutClaimsValidation()).ParseWithClaims(
		issued.AccessToken,
		claims,
		func(token *jwt.Token) (any, error) { return []byte(testJWTSecret), nil },
	)
	if err != nil || !parsed.Valid {
		t.Fatalf("parse issued token: %v", err)
	}
	if parsed.Method != jwt.SigningMethodHS256 {
		t.Fatalf("expected HS256, got %s", parsed.Method.Alg())
	}
	if claims.Subject != "42" || claims.Issuer != "signalwatch-api" {
		t.Fatalf("unexpected claims %+v", claims.RegisteredClaims)
	}
	if !claims.IssuedAt.Time.Equal(now) || !claims.ExpiresAt.Time.Equal(now.Add(time.Hour)) {
		t.Fatalf("unexpected token times: iat=%s exp=%s", claims.IssuedAt, claims.ExpiresAt)
	}

	userID, err := service.Verify(issued.AccessToken)
	if err != nil || userID != 42 {
		t.Fatalf("verify token: user ID %d, error %v", userID, err)
	}
}

func TestTokenServiceRejectsInvalidTokens(t *testing.T) {
	now := time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC)
	clockTime := now
	service := newTestTokenService(t, testJWTSecret, "signalwatch-api", time.Hour, func() time.Time {
		return clockTime
	})
	valid, err := service.Issue(42)
	if err != nil {
		t.Fatalf("issue valid token: %v", err)
	}

	wrongSecretService := newTestTokenService(
		t, "different-secret-with-at-least-thirty-two-bytes", "signalwatch-api", time.Hour,
		func() time.Time { return now },
	)
	wrongSignature, err := wrongSecretService.Issue(42)
	if err != nil {
		t.Fatalf("issue wrong-signature token: %v", err)
	}

	wrongIssuerService := newTestTokenService(t, testJWTSecret, "other-issuer", time.Hour, func() time.Time {
		return now
	})
	wrongIssuer, err := wrongIssuerService.Issue(42)
	if err != nil {
		t.Fatalf("issue wrong-issuer token: %v", err)
	}

	hmac384 := jwt.NewWithClaims(jwt.SigningMethodHS384, standardClaims(now, "42", "signalwatch-api"))
	wrongAlgorithm, err := hmac384.SignedString([]byte(testJWTSecret))
	if err != nil {
		t.Fatalf("sign wrong-algorithm token: %v", err)
	}

	tests := []struct {
		name    string
		token   string
		expired bool
	}{
		{name: "wrong signature", token: wrongSignature.AccessToken},
		{name: "wrong issuer", token: wrongIssuer.AccessToken},
		{name: "wrong algorithm", token: wrongAlgorithm},
		{name: "expired", token: valid.AccessToken, expired: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			clockTime = now
			if test.expired {
				clockTime = now.Add(time.Hour)
			}
			if _, err := service.Verify(test.token); !errors.Is(err, ErrInvalidToken) {
				t.Fatalf("expected invalid token error, got %v", err)
			}
		})
	}
}

func TestTokenServiceRejectsInvalidSubjectAndMissingClaims(t *testing.T) {
	now := time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC)
	service := newTestTokenService(t, testJWTSecret, "signalwatch-api", time.Hour, func() time.Time {
		return now
	})

	tests := []struct {
		name   string
		claims jwt.RegisteredClaims
	}{
		{name: "zero subject", claims: standardClaims(now, "0", "signalwatch-api")},
		{name: "negative subject", claims: standardClaims(now, "-1", "signalwatch-api")},
		{name: "non decimal subject", claims: standardClaims(now, "abc", "signalwatch-api")},
		{name: "overflowing subject", claims: standardClaims(now, strconv.FormatUint(^uint64(0), 10)+"0", "signalwatch-api")},
		{name: "missing issued at", claims: jwt.RegisteredClaims{
			Subject: "42", Issuer: "signalwatch-api", ExpiresAt: jwt.NewNumericDate(now.Add(time.Hour)),
		}},
		{name: "missing expiration", claims: jwt.RegisteredClaims{
			Subject: "42", Issuer: "signalwatch-api", IssuedAt: jwt.NewNumericDate(now),
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			raw, err := jwt.NewWithClaims(jwt.SigningMethodHS256, test.claims).
				SignedString([]byte(testJWTSecret))
			if err != nil {
				t.Fatalf("sign test token: %v", err)
			}
			if _, err := service.Verify(raw); !errors.Is(err, ErrInvalidToken) {
				t.Fatalf("expected invalid token error, got %v", err)
			}
		})
	}
}

func standardClaims(now time.Time, subject, issuer string) jwt.RegisteredClaims {
	return jwt.RegisteredClaims{
		Subject: subject, Issuer: issuer,
		IssuedAt: jwt.NewNumericDate(now), ExpiresAt: jwt.NewNumericDate(now.Add(time.Hour)),
	}
}

func newTestTokenService(
	t *testing.T,
	secret string,
	issuer string,
	ttl time.Duration,
	clock Clock,
) *TokenService {
	t.Helper()
	service, err := NewTokenService([]byte(secret), issuer, ttl, clock)
	if err != nil {
		t.Fatalf("create token service: %v", err)
	}
	return service
}

type authRepositoryStub struct {
	findByEmail func(context.Context, string) (user.User, error)
}

func (stub authRepositoryStub) FindByEmail(ctx context.Context, email string) (user.User, error) {
	return stub.findByEmail(ctx, email)
}

type tokenIssuerStub struct {
	issue func(uint64) (IssuedToken, error)
}

func (stub tokenIssuerStub) Issue(userID uint64) (IssuedToken, error) {
	return stub.issue(userID)
}
