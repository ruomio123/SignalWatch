package user

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
)

func TestServiceRegisterCreatesNormalizedUserWithDefaults(t *testing.T) {
	const password = " correct-horse-123 "

	createdAt := time.Date(2026, time.September, 3, 8, 0, 0, 0, time.UTC)
	updatedAt := createdAt.Add(time.Second)

	createCalls := 0
	var captured User
	repository := repositoryStub{
		create: func(_ context.Context, user *User) error {
			createCalls++

			// Simulate values populated by GORM and MySQL after INSERT.
			user.ID = 42
			user.CreatedAt = createdAt
			user.UpdatedAt = updatedAt
			captured = *user

			return nil
		},
	}

	registered, err := NewService(repository).Register(
		context.Background(),
		RegisterInput{
			Email:    "  Alice@Example.COM  ",
			Password: password,
		},
	)
	if err != nil {
		t.Fatalf("register user: %v", err)
	}

	if createCalls != 1 {
		t.Fatalf("expected repository Create to be called once, got %d", createCalls)
	}
	if captured.Email != "alice@example.com" {
		t.Fatalf("expected normalized email, got %q", captured.Email)
	}
	if captured.PasswordHash == "" {
		t.Fatal("expected a password hash")
	}
	if captured.PasswordHash == password {
		t.Fatal("password must not be stored in plaintext")
	}
	if err := bcrypt.CompareHashAndPassword(
		[]byte(captured.PasswordHash),
		[]byte(password),
	); err != nil {
		t.Fatalf("expected bcrypt hash for the original password: %v", err)
	}
	if err := bcrypt.CompareHashAndPassword(
		[]byte(captured.PasswordHash),
		[]byte(strings.TrimSpace(password)),
	); err == nil {
		t.Fatal("password must not be trimmed before hashing")
	}

	if captured.Timezone != DefaultTimezone {
		t.Fatalf("expected timezone %q, got %q", DefaultTimezone, captured.Timezone)
	}
	if captured.DigestTime != DefaultDigestTime {
		t.Fatalf("expected digest time %q, got %q", DefaultDigestTime, captured.DigestTime)
	}
	if captured.MaxItemsPerDigest != DefaultMaxItemsPerDigest {
		t.Fatalf(
			"expected max items %d, got %d",
			DefaultMaxItemsPerDigest,
			captured.MaxItemsPerDigest,
		)
	}
	if captured.Status != StatusActive {
		t.Fatalf("expected status %q, got %q", StatusActive, captured.Status)
	}

	if registered != captured {
		t.Fatalf("expected registered user %+v, got %+v", captured, registered)
	}
}

func TestServiceRegisterRejectsInvalidInputBeforePersistence(t *testing.T) {
	tests := []struct {
		name  string
		input RegisterInput
		want  error
	}{
		{
			name: "invalid email format",
			input: RegisterInput{
				Email:    "alice.example.com",
				Password: "correct-horse-123",
			},
			want: ErrInvalidEmail,
		},
		{
			name: "email exceeds maximum byte length",
			input: RegisterInput{
				Email:    strings.Repeat("a", maxEmailBytes+1) + "@example.com",
				Password: "correct-horse-123",
			},
			want: ErrInvalidEmail,
		},
		{
			name: "password has fewer than eight unicode characters",
			input: RegisterInput{
				Email:    "alice@example.com",
				Password: "seven77",
			},
			want: ErrInvalidPassword,
		},
		{
			name: "password exceeds bcrypt byte limit",
			input: RegisterInput{
				Email:    "alice@example.com",
				Password: strings.Repeat("界", 25),
			},
			want: ErrInvalidPassword,
		},
		{
			name: "password has no number",
			input: RegisterInput{
				Email:    "alice@example.com",
				Password: "onlyletters",
			},
			want: ErrInvalidPassword,
		},
		{
			name: "password has no letter",
			input: RegisterInput{
				Email:    "alice@example.com",
				Password: "12345678",
			},
			want: ErrInvalidPassword,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			createCalls := 0
			repository := repositoryStub{
				create: func(context.Context, *User) error {
					createCalls++
					return nil
				},
			}

			_, err := NewService(repository).Register(context.Background(), test.input)
			if !errors.Is(err, test.want) {
				t.Fatalf("expected %v, got %v", test.want, err)
			}
			if createCalls != 0 {
				t.Fatalf(
					"expected repository Create not to be called, got %d calls",
					createCalls,
				)
			}
		})
	}
}

func TestIsValidPasswordUsesUnicodeCharactersAndBcryptByteLimit(t *testing.T) {
	tests := []struct {
		name     string
		password string
		want     bool
	}{
		{
			name:     "seven ASCII characters",
			password: "seven77",
			want:     false,
		},
		{name: "unicode letters and number", password: "研究信号abc1", want: true},
		{name: "letters without number", password: "abcdefgh", want: false},
		{name: "numbers without letter", password: "12345678", want: false},
		{
			name:     "exactly seventy two UTF-8 bytes",
			password: "A1" + strings.Repeat("界", 23) + "x",
			want:     true,
		},
		{
			name:     "more than seventy two UTF-8 bytes",
			password: "A1" + strings.Repeat("界", 24),
			want:     false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := isValidPassword(test.password); got != test.want {
				t.Fatalf("expected %t, got %t", test.want, got)
			}
		})
	}
}

func TestServiceRegisterPreservesRepositoryErrors(t *testing.T) {
	databaseError := errors.New("database unavailable")
	tests := []struct {
		name            string
		repositoryError error
	}{
		{
			name:            "duplicate email",
			repositoryError: ErrEmailAlreadyRegistered,
		},
		{
			name:            "unexpected database error",
			repositoryError: databaseError,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository := repositoryStub{
				create: func(context.Context, *User) error {
					return test.repositoryError
				},
			}

			_, err := NewService(repository).Register(
				context.Background(),
				RegisterInput{
					Email:    "alice@example.com",
					Password: "correct-horse-123",
				},
			)
			if !errors.Is(err, test.repositoryError) {
				t.Fatalf("expected wrapped error %v, got %v", test.repositoryError, err)
			}
		})
	}
}

type repositoryStub struct {
	create         func(context.Context, *User) error
	findActiveByID func(context.Context, uint64) (User, error)
	updateProfile  func(context.Context, uint64, ProfileChanges) (User, error)
}

func (stub repositoryStub) Create(ctx context.Context, user *User) error {
	return stub.create(ctx, user)
}

func (stub repositoryStub) FindByEmail(context.Context, string) (User, error) {
	return User{}, ErrNotFound
}

func (stub repositoryStub) FindActiveByID(ctx context.Context, userID uint64) (User, error) {
	if stub.findActiveByID == nil {
		return User{}, ErrNotFound
	}
	return stub.findActiveByID(ctx, userID)
}

func (stub repositoryStub) UpdateProfile(
	ctx context.Context,
	userID uint64,
	changes ProfileChanges,
) (User, error) {
	if stub.updateProfile == nil {
		return User{}, ErrNotFound
	}
	return stub.updateProfile(ctx, userID, changes)
}
