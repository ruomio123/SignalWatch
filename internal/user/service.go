package user

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/crypto/bcrypt"
)

const (
	maxEmailBytes    = 254
	minPasswordRunes = 8
	maxPasswordBytes = 72
)

// RegisterInput 是注册用例所需的输入。
type RegisterInput struct {
	Email    string
	Password string
}

var (
	// ErrInvalidEmail 表示邮箱不符合注册规则。
	ErrInvalidEmail = errors.New("invalid email")
	// ErrInvalidPassword 表示密码不符合注册规则。
	ErrInvalidPassword = errors.New("invalid password")
)

// Service 负责用户注册的业务规则。
type Service struct {
	repository Repository
}

// NewService 创建一个使用指定 Repository 的用户服务。
func NewService(repository Repository) *Service {
	return &Service{
		repository: repository,
	}
}

// NormalizeEmail 返回去除首尾空白并转为小写的邮箱。
func NormalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

// 邮箱校验
func isValidEmail(email string) bool {
	if email == "" || len(email) > maxEmailBytes {
		return false
	}

	address, err := mail.ParseAddress(email)
	return err == nil && address.Address == email
}

// 密码校验
func isValidPassword(password string) bool {
	// RuneCountInString 计算 Unicode 字符数；
	// len 计算 UTF-8 字节数，对应 bcrypt 的 72 字节限制。
	if !utf8.ValidString(password) ||
		utf8.RuneCountInString(password) < minPasswordRunes ||
		len(password) > maxPasswordBytes {
		return false
	}

	hasLetter := false
	hasNumber := false
	for _, character := range password {
		hasLetter = hasLetter || unicode.IsLetter(character)
		hasNumber = hasNumber || unicode.IsNumber(character)
	}
	return hasLetter && hasNumber
}

// Register 按顺序完成：邮箱归一化、参数校验、bcrypt 哈希和用户持久化。
func (service *Service) Register(ctx context.Context, input RegisterInput) (User, error) {
	email := NormalizeEmail(input.Email)

	if !isValidEmail(email) {
		return User{}, ErrInvalidEmail
	}
	if !isValidPassword(input.Password) {
		return User{}, ErrInvalidPassword
	}
	passwordHash, err := bcrypt.GenerateFromPassword(
		[]byte(input.Password),
		bcrypt.DefaultCost,
	)
	if err != nil {
		return User{}, fmt.Errorf("generate password hash: %w", err)
	}
	user := NewUser(email, string(passwordHash))
	if err := service.repository.Create(ctx, &user); err != nil {
		return User{}, fmt.Errorf("create user: %w", err)
	}
	return user, nil
}

// GetProfile returns only an active user selected by the authenticated ID.
func (service *Service) GetProfile(ctx context.Context, userID uint64) (User, error) {
	if userID == 0 {
		return User{}, ErrNotFound
	}
	profile, err := service.repository.FindActiveByID(ctx, userID)
	if err != nil {
		return User{}, fmt.Errorf("find active user profile: %w", err)
	}
	return profile, nil
}

// UpdateProfile validates and normalizes the patch before persistence.
func (service *Service) UpdateProfile(
	ctx context.Context,
	userID uint64,
	input UpdateProfileInput,
) (User, error) {
	if userID == 0 {
		return User{}, ErrNotFound
	}
	changes, err := validateProfileUpdate(input)
	if err != nil {
		return User{}, err
	}
	profile, err := service.repository.UpdateProfile(ctx, userID, changes)
	if err != nil {
		return User{}, fmt.Errorf("update active user profile: %w", err)
	}
	return profile, nil
}
