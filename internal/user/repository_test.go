package user

import (
	"errors"
	"fmt"
	"testing"

	"github.com/go-sql-driver/mysql"
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
