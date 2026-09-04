package auth

import (
	"context"
	"errors"
	"fmt"

	"golang.org/x/crypto/bcrypt"

	"signalwatch/internal/user"
)

// This valid bcrypt hash is deliberately unrelated to any real account. It is
// always used for missing users so that the failure path still pays bcrypt's
// comparison cost.
const missingUserPasswordHash = "$2a$10$7EqJtq98hPqEX7fNZaFWoO5Wwh/RkdBbfoV3zG5K.S3t4hR.H/o4u"

var ErrInvalidCredentials = errors.New("invalid credentials")

type UserRepository interface {
	FindByEmail(ctx context.Context, normalizedEmail string) (user.User, error)
}

type TokenIssuer interface {
	Issue(userID uint64) (IssuedToken, error)
}

type LoginInput struct {
	Email    string
	Password string
}

type LoginResult struct {
	AccessToken string
	ExpiresIn   int64
}

type Service struct {
	repository UserRepository
	tokens     TokenIssuer
}

func NewService(repository UserRepository, tokens TokenIssuer) *Service {
	return &Service{repository: repository, tokens: tokens}
}

func (service *Service) Login(
	ctx context.Context,
	input LoginInput,
) (LoginResult, error) {
	email := user.NormalizeEmail(input.Email)
	storedUser, err := service.repository.FindByEmail(ctx, email)

	found := true
	passwordHash := storedUser.PasswordHash
	switch {
	case err == nil:
	case errors.Is(err, user.ErrNotFound):
		found = false
		passwordHash = missingUserPasswordHash
	default:
		return LoginResult{}, fmt.Errorf("find user by email: %w", err)
	}

	passwordMatches := bcrypt.CompareHashAndPassword(
		[]byte(passwordHash),
		[]byte(input.Password),
	) == nil
	if !found || !passwordMatches || storedUser.Status != user.StatusActive {
		return LoginResult{}, ErrInvalidCredentials
	}

	issued, err := service.tokens.Issue(storedUser.ID)
	if err != nil {
		return LoginResult{}, fmt.Errorf("issue access token: %w", err)
	}

	return LoginResult{
		AccessToken: issued.AccessToken,
		ExpiresIn:   issued.ExpiresIn,
	}, nil
}
