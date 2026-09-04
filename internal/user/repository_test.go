package user

import (
	"errors"
	"fmt"
	"testing"

	"github.com/go-sql-driver/mysql"
	"gorm.io/gorm"
)

func TestMapCreateErrorMapsOnlyUsersEmailDuplicateKey(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want error
	}{
		{
			name: "users email duplicate key",
			err: fmt.Errorf(
				"create user: %w",
				&mysql.MySQLError{
					Number:  mysqlDuplicateEntryErrorNumber,
					Message: "Duplicate entry 'alice@example.com' for key 'users.uk_users_email'",
				},
			),
			want: ErrEmailAlreadyRegistered,
		},
		{
			name: "other duplicate key",
			err: &mysql.MySQLError{
				Number:  mysqlDuplicateEntryErrorNumber,
				Message: "Duplicate entry 'value' for key 'users.uk_some_other_key'",
			},
		},
		{
			name: "non duplicate database error",
			err:  errors.New("database connection lost"),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := mapCreateError(test.err)

			if test.want != nil {
				if !errors.Is(got, test.want) {
					t.Fatalf("expected %v, got %v", test.want, got)
				}
				return
			}

			if got != test.err {
				t.Fatalf("expected original error %v, got %v", test.err, got)
			}
		})
	}
}

func TestMapFindErrorMapsOnlyRecordNotFound(t *testing.T) {
	databaseError := errors.New("database unavailable")
	tests := []struct {
		name string
		err  error
		want error
	}{
		{
			name: "record not found",
			err:  fmt.Errorf("query user: %w", gorm.ErrRecordNotFound),
			want: ErrNotFound,
		},
		{
			name: "other database error",
			err:  databaseError,
			want: databaseError,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := mapFindError(test.err)
			if !errors.Is(got, test.want) {
				t.Fatalf("expected %v, got %v", test.want, got)
			}
		})
	}
}

func TestProfileUpdateColumnsContainsOnlySuppliedWhitelistFields(t *testing.T) {
	timezone := "Asia/Shanghai"
	maxItems := uint16(30)
	updates := profileUpdateColumns(ProfileChanges{
		Timezone:          &timezone,
		MaxItemsPerDigest: &maxItems,
	})

	if len(updates) != 2 {
		t.Fatalf("expected two update columns, got %+v", updates)
	}
	if updates["timezone"] != timezone {
		t.Fatalf("expected timezone %q, got %+v", timezone, updates["timezone"])
	}
	if updates["max_items_per_digest"] != maxItems {
		t.Fatalf("expected max items %d, got %+v", maxItems, updates["max_items_per_digest"])
	}
	for _, forbidden := range []string{"id", "email", "password_hash", "status"} {
		if _, exists := updates[forbidden]; exists {
			t.Fatalf("forbidden column %q was included", forbidden)
		}
	}
}

func TestProfileNeedsUpdateRecognizesIdempotentRetry(t *testing.T) {
	current := profileTestUser()
	timezone := current.Timezone
	digestTime := current.DigestTime
	maxItems := current.MaxItemsPerDigest

	if profileNeedsUpdate(current, ProfileChanges{
		Timezone:          &timezone,
		DigestTime:        &digestTime,
		MaxItemsPerDigest: &maxItems,
	}) {
		t.Fatal("expected identical profile patch not to require an update")
	}

	changedTimezone := "Asia/Shanghai"
	if !profileNeedsUpdate(current, ProfileChanges{Timezone: &changedTimezone}) {
		t.Fatal("expected changed timezone to require an update")
	}
}
