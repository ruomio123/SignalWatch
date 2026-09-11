// Package fence propagates collection ownership to transactional write adapters.
package fence

import (
	"context"
	"errors"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type Token struct {
	Name, Owner string
	Epoch       uint64
}
type key struct{}

var ErrLost = errors.New("collection lease lost")

func With(ctx context.Context, t Token) context.Context { return context.WithValue(ctx, key{}, t) }

// Guard must run in the same transaction as the protected writes. The row
// lock prevents a successor from acquiring ownership before this commit.
func Guard(ctx context.Context, tx *gorm.DB) error {
	t, ok := ctx.Value(key{}).(Token)
	if !ok {
		return nil
	}
	var row Token
	err := tx.Table("collection_leases").Clauses(clause.Locking{Strength: "UPDATE"}).Where("name=? AND owner=? AND epoch=? AND expires_at>UTC_TIMESTAMP(6)", t.Name, t.Owner, t.Epoch).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrLost
	}
	return err
}
