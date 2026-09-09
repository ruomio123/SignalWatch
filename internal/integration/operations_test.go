package integration_test

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"signalwatch/internal/operator"
	"signalwatch/internal/user"
)

// TestOperatorRoleManagement exercises the role migration and the local CLI
// service against the same real MySQL boundary used by the acceptance suite.
func TestOperatorRoleManagement(t *testing.T) {
	database, sqlDB, _ := openM1TestDatabase(t)
	t.Cleanup(func() {
		if err := sqlDB.Close(); err != nil {
			t.Errorf("close operator test connection pool: %v", err)
		}
	})
	nonce := strconv.FormatInt(time.Now().UTC().UnixNano(), 36)
	emailA := "ops-a-" + nonce + "@example.test"
	emailB := "ops-b-" + nonce + "@example.test"
	disabledEmail := "ops-disabled-" + nonce + "@example.test"
	emails := []string{emailA, emailB, disabledEmail}
	t.Cleanup(func() {
		if err := database.Where("email IN ?", emails).Delete(&user.User{}).Error; err != nil {
			t.Errorf("clean operator users: %v", err)
		}
	})

	var existingActiveOperators int64
	if err := database.Model(&user.User{}).
		Where("role = ? AND status = ?", user.RoleOperator, user.StatusActive).
		Count(&existingActiveOperators).Error; err != nil {
		t.Fatalf("count existing operators: %v", err)
	}

	now := time.Now().UTC()
	if err := database.Exec(`INSERT INTO users
		(email, password_hash, timezone, digest_time, max_items_per_digest, status, created_at, updated_at)
		VALUES (?, ?, 'UTC', '08:00:00', 50, 'active', ?, ?)`,
		emailA, "not-used-by-this-test", now, now).Error; err != nil {
		t.Fatalf("insert user through database default: %v", err)
	}
	accountB := user.NewUser(emailB, "not-used-by-this-test")
	disabled := user.NewUser(disabledEmail, "not-used-by-this-test")
	disabled.Status = "disabled"
	if err := database.Create(&accountB).Error; err != nil {
		t.Fatalf("create second role fixture: %v", err)
	}
	if err := database.Create(&disabled).Error; err != nil {
		t.Fatalf("create disabled role fixture: %v", err)
	}

	var defaulted user.User
	if err := database.Where("email = ?", emailA).Take(&defaulted).Error; err != nil {
		t.Fatalf("load defaulted role: %v", err)
	}
	if defaulted.Role != user.RoleUser {
		t.Fatalf("expected database default role user, got %q", defaulted.Role)
	}
	if err := database.Model(&user.User{}).Where("id = ?", defaulted.ID).
		Update("role", "administrator").Error; err == nil {
		t.Fatal("expected users.role CHECK constraint to reject an invalid role")
	}

	service, err := operator.NewService(database, func() time.Time { return now.Add(time.Minute) })
	if err != nil {
		t.Fatalf("create operator service: %v", err)
	}
	granted, err := service.Grant(context.Background(), emailA)
	if err != nil || granted.Role != user.RoleOperator {
		t.Fatalf("grant operator: account=%+v error=%v", granted, err)
	}
	if _, err := service.Grant(context.Background(), emailA); err != nil {
		t.Fatalf("idempotent grant: %v", err)
	}
	if _, err := service.Grant(context.Background(), disabledEmail); !errors.Is(err, operator.ErrAccountInactive) {
		t.Fatalf("expected inactive-account error, got %v", err)
	}
	if existingActiveOperators == 0 {
		if _, err := service.Revoke(context.Background(), emailA); !errors.Is(err, operator.ErrLastOperator) {
			t.Fatalf("expected last active operator protection, got %v", err)
		}
	}
	if _, err := service.Grant(context.Background(), emailB); err != nil {
		t.Fatalf("grant second operator: %v", err)
	}
	revoked, err := service.Revoke(context.Background(), emailA)
	if err != nil || revoked.Role != user.RoleUser {
		t.Fatalf("revoke operator: account=%+v error=%v", revoked, err)
	}
	if _, err := service.Revoke(context.Background(), emailA); err != nil {
		t.Fatalf("idempotent revoke: %v", err)
	}
	listed, err := service.List(context.Background())
	if err != nil {
		t.Fatalf("list operators: %v", err)
	}
	foundB := false
	for _, account := range listed {
		foundB = foundB || account.Email == emailB
	}
	if !foundB {
		t.Fatalf("expected second operator in list: %+v", listed)
	}
}
