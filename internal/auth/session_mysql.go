package auth

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
)

type mysqlSessionStore struct{ db *gorm.DB }

func NewSessionStore(db *gorm.DB) SessionStore { return &mysqlSessionStore{db: db} }
func (s *mysqlSessionStore) Create(ctx context.Context, session Session) error {
	// Expired rows have no authority and can be removed independently. Commit
	// cleanup before insertion so concurrent logins do not retain range locks.
	if err := s.db.WithContext(ctx).Table("auth_sessions").Where("user_id=? AND expires_at<=?", session.UserID, session.CreatedAt).Delete(&Session{}).Error; err != nil {
		return err
	}
	return s.db.WithContext(ctx).Table("auth_sessions").Create(&session).Error
}
func (s *mysqlSessionStore) FindActive(ctx context.Context, hash string, now time.Time) (Session, error) {
	var session Session
	err := s.db.WithContext(ctx).Table("auth_sessions AS s").Select("s.*").Joins("JOIN users AS u ON u.id=s.user_id AND u.status='active'").Where("s.token_hash=? AND s.expires_at>?", hash, now).Take(&session).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return Session{}, ErrInvalidSession
	}
	return session, err
}
func (s *mysqlSessionStore) Delete(ctx context.Context, hash string) error {
	return s.db.WithContext(ctx).Table("auth_sessions").Where("token_hash=?", hash).Delete(&Session{}).Error
}
