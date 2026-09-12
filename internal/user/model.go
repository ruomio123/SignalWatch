package user

import (
	"time"
)

// 定义注册默认值常量
const (
	DefaultTimezone                 = "UTC"
	DefaultDigestTime               = "08:00:00"
	DefaultMaxItemsPerDigest uint16 = 20
	StatusActive                    = "active"
)

type User struct {
	AIEnabled  bool   `gorm:"column:ai_enabled" json:"ai_enabled"`
	AILanguage string `gorm:"column:ai_language;default:zh" json:"ai_language"`
	//同一份用户数据，在“数据库、Go 程序、HTTP JSON”三种形式之间转换。
	ID                uint64    `gorm:"column:id;primaryKey;autoIncrement" json:"id"`
	Email             string    `gorm:"column:email" json:"email"`
	PasswordHash      string    `gorm:"column:password_hash" json:"-"`
	Timezone          string    `gorm:"column:timezone" json:"timezone"`
	DigestTime        string    `gorm:"column:digest_time" json:"digest_time"`
	MaxItemsPerDigest uint16    `gorm:"column:max_items_per_digest" json:"max_items_per_digest"`
	Status            string    `gorm:"column:status" json:"status"`
	CreatedAt         time.Time `gorm:"column:created_at" json:"created_at"`
	UpdatedAt         time.Time `gorm:"column:updated_at" json:"updated_at"`
}

// 定义独立的 PublicUser,只包含可公开字段，绝不含密码哈希。
type PublicUser struct {
	AIEnabled         bool      `json:"ai_enabled"`
	AILanguage        string    `json:"ai_language"`
	ID                uint64    `json:"id"`
	Email             string    `json:"email"`
	Timezone          string    `json:"timezone"`
	DigestTime        string    `json:"digest_time"`
	MaxItemsPerDigest uint16    `json:"max_items_per_digest"`
	Status            string    `json:"status"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

func (User) TableName() string {
	return "users"
}
func NewUser(email, passwordHash string) User {
	return User{AILanguage: "zh",
		Email:             email,
		PasswordHash:      passwordHash,
		Timezone:          DefaultTimezone,
		DigestTime:        DefaultDigestTime,
		MaxItemsPerDigest: DefaultMaxItemsPerDigest,
		Status:            StatusActive,
	}
}

// User转化为PublicUser
// u User:值接收者：通常用于读取数据
func (u User) Public() PublicUser {
	return PublicUser{AIEnabled: u.AIEnabled, AILanguage: u.AILanguage,
		ID:                u.ID,
		Email:             u.Email,
		Timezone:          u.Timezone,
		DigestTime:        apiDigestTime(u.DigestTime),
		MaxItemsPerDigest: u.MaxItemsPerDigest,
		Status:            u.Status,
		CreatedAt:         u.CreatedAt,
		UpdatedAt:         u.UpdatedAt,
	}
}

// api的digest_time建议输出08:00
func apiDigestTime(value string) string {
	if len(value) == len("08:00:00") && value[2] == ':' && value[5] == ':' {
		return value[:5]
	}
	return value
}
