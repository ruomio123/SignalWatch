package collector

import (
	"context"
	"testing"
	"time"
)

func TestMySQLLockRejectsInvalidConstruction(t *testing.T) {
	if _, err := NewMySQLLockManager(nil, "collector"); err == nil {
		t.Fatal("nil database accepted")
	}
	m := &MySQLLockManager{}
	if _, _, _, err := m.Acquire(context.Background(), time.Millisecond); err == nil {
		t.Fatal("unsafe TTL accepted")
	}
}
