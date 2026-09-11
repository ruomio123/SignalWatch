package subscription

import (
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"strings"
	"testing"
)

func TestOwnedQueryAlwaysScopesUserAndSoftDelete(t *testing.T) {
	db, err := gorm.Open(mysql.New(mysql.Config{DSN: "test:test@tcp(localhost)/unused", SkipInitializeWithVersion: true}), &gorm.Config{DryRun: true, DisableAutomaticPing: true})
	if err != nil {
		t.Fatal(err)
	}
	sql := db.ToSQL(func(tx *gorm.DB) *gorm.DB {
		var rows []Subscription
		return scope(tx, 42, ListFilter{}).Where("id=?", 7).Find(&rows)
	})
	for _, want := range []string{"user_id=42", "deleted_at IS NULL", "id=7"} {
		if !strings.Contains(sql, want) {
			t.Fatalf("missing %s in %s", want, sql)
		}
	}
}
