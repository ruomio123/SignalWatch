package collector

import (
	"context"
	"crypto/rand"
	"errors"
	"gorm.io/gorm"
	"signalwatch/internal/platform/fence"
	"time"
)

type ReleaseFunc func(context.Context) error
type LockManager interface {
	Acquire(context.Context, time.Duration) (context.Context, ReleaseFunc, bool, error)
}
type MySQLLockManager struct {
	db   *gorm.DB
	name string
}

func NewMySQLLockManager(db *gorm.DB, name string) (*MySQLLockManager, error) {
	if db == nil || name == "" {
		return nil, errors.New("invalid collection lease")
	}
	return &MySQLLockManager{db, name}, nil
}
func (m *MySQLLockManager) Acquire(ctx context.Context, ttl time.Duration) (context.Context, ReleaseFunc, bool, error) {
	if ttl < 3*time.Second {
		return ctx, nil, false, errors.New("collection lease TTL must be at least 3 seconds")
	}
	token := fence.Token{Name: m.name, Owner: rand.Text()}
	acquired := false
	err := m.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("INSERT IGNORE INTO collection_leases(name,owner,epoch,expires_at) VALUES (?,'',0,UTC_TIMESTAMP(6))", m.name).Error; err != nil {
			return err
		}
		r := tx.Exec("UPDATE collection_leases SET owner=?,epoch=epoch+1,expires_at=DATE_ADD(UTC_TIMESTAMP(6),INTERVAL ? MICROSECOND) WHERE name=? AND expires_at<=UTC_TIMESTAMP(6)", token.Owner, ttl.Microseconds(), m.name)
		if r.Error != nil {
			return r.Error
		}
		if r.RowsAffected == 0 {
			return nil
		}
		acquired = true
		return tx.Table("collection_leases").Select("epoch").Where("name=?", m.name).Scan(&token.Epoch).Error
	})
	if err != nil || !acquired {
		return ctx, nil, acquired, err
	}
	work, cancel := context.WithCancel(fence.With(ctx, token))
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(ttl / 3)
		defer ticker.Stop()
		for {
			select {
			case <-work.Done():
				return
			case <-ticker.C:
				renewal, stop := context.WithTimeout(work, min(ttl/3, 5*time.Second))
				r := m.db.WithContext(renewal).Exec("UPDATE collection_leases SET expires_at=DATE_ADD(UTC_TIMESTAMP(6),INTERVAL ? MICROSECOND) WHERE name=? AND owner=? AND epoch=? AND expires_at>UTC_TIMESTAMP(6)", ttl.Microseconds(), token.Name, token.Owner, token.Epoch)
				stop()
				if r.Error != nil || r.RowsAffected != 1 {
					cancel()
					return
				}
			}
		}
	}()
	release := func(ctx context.Context) error {
		cancel()
		<-done
		return m.db.WithContext(ctx).Exec("UPDATE collection_leases SET expires_at=UTC_TIMESTAMP(6) WHERE name=? AND owner=? AND epoch=?", token.Name, token.Owner, token.Epoch).Error
	}
	return work, release, true, nil
}
