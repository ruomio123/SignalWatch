package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"signalwatch/internal/document"
	"signalwatch/internal/paper"
	"signalwatch/internal/rules"
	"signalwatch/internal/subscription"
	"time"
)

type MySQLStore struct{ db *gorm.DB }

func NewMySQLStore(db *gorm.DB) *MySQLStore { return &MySQLStore{db} }
func notFound(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrNotFound
	}
	return err
}
func (s *MySQLStore) CreateConversation(ctx context.Context, c Conversation) error {
	return s.db.WithContext(ctx).Create(&c).Error
}
func (s *MySQLStore) Conversation(ctx context.Context, u uint64, id string) (Conversation, error) {
	var c Conversation
	err := s.db.WithContext(ctx).Where("id=? AND user_id=?", id, u).Take(&c).Error
	return c, notFound(err)
}
func (s *MySQLStore) Conversations(ctx context.Context, u uint64, kind string, pid *uint64, page int) ([]Conversation, error) {
	rows := []Conversation{}
	q := s.db.WithContext(ctx).Where("user_id=?", u)
	if kind != "" {
		q = q.Where("kind=?", kind)
	}
	if pid != nil {
		q = q.Where("paper_id=?", *pid)
	}
	err := q.Order("updated_at DESC,id DESC").Offset((page - 1) * 20).Limit(20).Find(&rows).Error
	return rows, err
}
func (s *MySQLStore) DeleteConversation(ctx context.Context, u uint64, id string) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockAccount(tx, u); err != nil {
			return err
		}

		var c Conversation
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND user_id=?", id, u).Take(&c).Error; err != nil {
			return notFound(err)
		}
		return tx.Delete(&c).Error
	})
}
func (s *MySQLStore) Messages(ctx context.Context, u uint64, id string, before uint64) ([]Message, error) {
	if _, err := s.Conversation(ctx, u, id); err != nil {
		return nil, err
	}
	rows := []Message{}
	q := s.db.WithContext(ctx).Where("conversation_id=?", id)
	if before > 0 {
		q = q.Where("id<?", before)
	}
	err := q.Order("id DESC").Limit(50).Find(&rows).Error
	for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
		rows[i], rows[j] = rows[j], rows[i]
	}
	return rows, err
}
func (s *MySQLStore) History(ctx context.Context, id string) ([]Message, error) {
	rows := []Message{}
	err := s.db.WithContext(ctx).Table("agent_messages m").Select("m.*").Joins("JOIN agent_runs r ON r.id=m.run_id AND r.state='completed'").Where("m.conversation_id=?", id).Order("m.id DESC").Limit(20).Scan(&rows).Error
	for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
		rows[i], rows[j] = rows[j], rows[i]
	}
	return rows, err
}
func (s *MySQLStore) Submit(ctx context.Context, r Run) (out Run, err error) {
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockAccount(tx, r.UserID); err != nil {
			return err
		}

		var c Conversation
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND user_id=?", r.ConversationID, r.UserID).Take(&c).Error; err != nil {
			return notFound(err)
		}
		err := tx.Where("conversation_id=? AND idempotency_key=?", c.ID, r.IdempotencyKey).Take(&out).Error
		if err == nil {
			if out.InputHash != r.InputHash {
				return ErrConflict
			}
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if c.ActiveRunID != nil {
			return ErrConflict
		}
		if err := tx.Create(&r).Error; err != nil {
			return err
		}
		m := Message{ConversationID: c.ID, RunID: r.ID, Role: "user", Content: r.Question, Provider: r.Provider, Model: r.Model, Citations: json.RawMessage("[]"), CreatedAt: r.CreatedAt}
		if err := tx.Create(&m).Error; err != nil {
			return err
		}
		if err := tx.Model(&Conversation{}).Where("id=?", c.ID).Updates(map[string]any{"active_run_id": r.ID, "updated_at": r.CreatedAt}).Error; err != nil {
			return err
		}
		out = r
		return nil
	})
	return
}
func (s *MySQLStore) RunByID(ctx context.Context, u uint64, id string) (Run, error) {
	var r Run
	err := s.db.WithContext(ctx).Where("id=? AND user_id=?", id, u).Take(&r).Error
	return r, notFound(err)
}
func (s *MySQLStore) Cancel(ctx context.Context, u uint64, id string) error {
	r, err := s.RunByID(ctx, u, id)
	if err != nil {
		return err
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockAccount(tx, u); err != nil {
			return err
		}

		var c Conversation
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND user_id=?", r.ConversationID, u).Take(&c).Error; err != nil {
			return notFound(err)
		}
		if err := tx.Model(&Run{}).Where("id=? AND state IN ('pending','running')", id).Updates(map[string]any{"state": "cancelled", "updated_at": gorm.Expr("UTC_TIMESTAMP(6)")}).Error; err != nil {
			return err
		}
		return tx.Model(&Conversation{}).Where("id=? AND active_run_id=?", c.ID, id).Update("active_run_id", nil).Error
	})
}
func (s *MySQLStore) Claim(ctx context.Context, owner string) (r Run, err error) {
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).Where("state='pending' OR (state='running' AND lease_until<UTC_TIMESTAMP(6))").Order("created_at").Take(&r).Error; err != nil {
			return err
		}
		r.Epoch++
		r.LeaseOwner = owner
		r.State = "running"
		if err := tx.Model(&Run{}).Where("id=?", r.ID).Updates(map[string]any{"state": "running", "lease_owner": owner, "epoch": r.Epoch, "lease_until": gorm.Expr("DATE_ADD(UTC_TIMESTAMP(6),INTERVAL 30 SECOND)"), "deadline": gorm.Expr("COALESCE(deadline,DATE_ADD(UTC_TIMESTAMP(6),INTERVAL 180 SECOND))"), "updated_at": gorm.Expr("UTC_TIMESTAMP(6)")}).Error; err != nil {
			return err
		}
		return tx.Where("id=?", r.ID).Take(&r).Error
	})
	return
}
func owned(db *gorm.DB, r Run) *gorm.DB {
	return db.Model(&Run{}).Where("id=? AND state='running' AND epoch=? AND lease_owner=? AND lease_until>UTC_TIMESTAMP(6)", r.ID, r.Epoch, r.LeaseOwner)
}
func (s *MySQLStore) Check(ctx context.Context, r Run) error {
	var n int64
	err := owned(s.db.WithContext(ctx), r).Where("deadline>UTC_TIMESTAMP(6)").Where("EXISTS (SELECT 1 FROM users WHERE users.id=agent_runs.user_id AND users.status='active' AND users.role='user')").Count(&n).Error
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrLease
	}
	return nil
}
func (s *MySQLStore) Renew(ctx context.Context, r Run) error {
	q := owned(s.db.WithContext(ctx), r).Where("deadline>UTC_TIMESTAMP(6)").Update("lease_until", gorm.Expr("DATE_ADD(UTC_TIMESTAMP(6),INTERVAL 30 SECOND)"))
	if q.Error != nil {
		return q.Error
	}
	if q.RowsAffected != 1 {
		return ErrLease
	}
	return nil
}
func (s *MySQLStore) Save(ctx context.Context, r Run, cp Checkpoint, progress string, step *Step) error {
	raw, _ := json.Marshal(cp)
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		q := owned(tx, r).Where("deadline>UTC_TIMESTAMP(6)").Updates(map[string]any{"checkpoint": string(raw), "progress": progress, "updated_at": gorm.Expr("UTC_TIMESTAMP(6)")})
		if q.Error != nil {
			return q.Error
		}
		if q.RowsAffected != 1 {
			return ErrLease
		}
		if step != nil {
			return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(step).Error
		}
		return nil
	})
}
func (s *MySQLStore) Finish(ctx context.Context, r Run, state, code string, m *Message) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Lock order matches credential mutation and subscription confirmation.
		var user struct{ ID uint64 }
		if err := tx.Table("users").Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=?", r.UserID).Take(&user).Error; err != nil {
			return err
		}
		var c Conversation
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND user_id=?", r.ConversationID, r.UserID).Take(&c).Error; err != nil {
			return notFound(err)
		}
		if state == "completed" {
			if c.PaperID != nil && m != nil {
				var p paper.Paper
				if err := tx.Clauses(clause.Locking{Strength: "SHARE"}).Where("id=?", *c.PaperID).Take(&p).Error; err != nil {
					return err
				}
				// References are immutable snapshots. Check each against the current paper.
				var refs []Citation
				if json.Unmarshal(m.Citations, &refs) != nil {
					return ErrOutput
				}
				for _, ref := range refs {
					if ref.Page == 0 {
						h := sha256.Sum256([]byte(p.Title + "\n" + p.Abstract))
						if ref.ContentHash != hex.EncodeToString(h[:]) {
							return ErrConflict
						}
					} else if ref.DocumentID != document.Identity(document.Source{PaperID: p.ID, ArXivID: p.ArXivID, PDFURL: p.PDFURL, UpdatedAt: p.ArXivUpdatedAt}) {
						return ErrConflict
					}
				}
				var visible int64
				if err := tx.Table("subscription_papers sp").Joins("JOIN subscriptions s ON s.id=sp.subscription_id").Where("sp.paper_id=? AND s.user_id=?", p.ID, r.UserID).Count(&visible).Error; err != nil {
					return err
				}
				if visible == 0 {
					return ErrNotFound
				}
			}

			var n int64
			if err := tx.Table("user_ai_configurations").Where("user_id=? AND provider_id=? AND generation=? AND config_version=? AND status='active'", r.UserID, r.Provider, r.Generation, r.Version).Count(&n).Error; err != nil {
				return err
			}
			if n != 1 {
				state = "failed"
				code = "configuration_changed"
				m = nil
			}
		}
		q := owned(tx, r).Updates(map[string]any{"state": state, "progress": state, "failure_code": code, "updated_at": gorm.Expr("UTC_TIMESTAMP(6)")})
		if q.Error != nil {
			return q.Error
		}
		if q.RowsAffected != 1 {
			return ErrLease
		}
		if m != nil {
			if err := tx.Create(m).Error; err != nil {
				return err
			}
		}
		return tx.Model(&Conversation{}).Where("id=? AND active_run_id=?", c.ID, r.ID).Updates(map[string]any{"active_run_id": nil, "updated_at": gorm.Expr("UTC_TIMESTAMP(6)")}).Error
	})
}
func (s *MySQLStore) SaveDraft(ctx context.Context, r Run, input subscription.CreateInput) (d subscription.Draft, err error) {
	raw, _ := json.Marshal(input)
	now := time.Now().UTC()
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockAccount(tx, r.UserID); err != nil {
			return err
		}

		var c Conversation
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND user_id=?", r.ConversationID, r.UserID).Take(&c).Error; err != nil {
			return notFound(err)
		}
		var n int64
		if err := owned(tx, r).Count(&n).Error; err != nil {
			return err
		}
		if n != 1 {
			return ErrLease
		}
		// One proposal per run: replay of a completed tool is idempotent.
		err := tx.Where("id=?", r.ID).Take(&d).Error
		if err == nil {
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		version := uint32(1)
		if c.LatestDraftID != "" {
			var old subscription.Draft
			if err := tx.Where("id=?", c.LatestDraftID).Take(&old).Error; err != nil {
				return err
			}
			version = old.Version + 1
		}
		d = subscription.Draft{ID: r.ID, ConversationID: c.ID, UserID: r.UserID, Version: version, Payload: raw, CreatedAt: now, ExpiresAt: now.Add(30 * time.Minute)}
		if err := tx.Create(&d).Error; err != nil {
			return err
		}
		return tx.Model(&Conversation{}).Where("id=?", c.ID).Update("latest_draft_id", d.ID).Error
	})
	return
}
func (s *MySQLStore) Draft(ctx context.Context, u uint64, id string) (subscription.Draft, error) {
	var d subscription.Draft
	err := s.db.WithContext(ctx).Where("id=? AND user_id=?", id, u).Take(&d).Error
	return d, notFound(err)
}
func (s *MySQLStore) DigestLimit(ctx context.Context, u uint64) (uint16, error) {
	var row struct{ MaxItemsPerDigest uint16 }
	err := s.db.WithContext(ctx).Table("users").Where("id=? AND status='active'", u).Take(&row).Error
	return row.MaxItemsPerDigest, err
}
func (s *MySQLStore) Preview(ctx context.Context, sourceID uint64, rule subscription.RulesInput) (Preview, error) {
	now := time.Now().UTC()
	out := Preview{From: now.Add(-7 * 24 * time.Hour), To: now, Items: []paper.PublicPaper{}, Complete: true}
	cursor := uint64(0)
	for batch := 0; batch < 100; batch++ {
		rows := []paper.Paper{}
		if err := s.db.WithContext(ctx).Where("source_id=? AND published_at>=? AND published_at<=? AND first_seen_at<=? AND id>?", sourceID, out.From, out.To, out.To, cursor).Order("id").Limit(200).Find(&rows).Error; err != nil {
			return out, err
		}
		for _, p := range rows {
			cursor = p.ID
			var cats []string
			if json.Unmarshal(p.CategoriesJSON, &cats) != nil {
				continue
			}
			_, matched := rules.Match(rules.Paper{Title: p.Title, Abstract: p.Abstract, Categories: cats}, rules.Rule{Category: rule.Category, Keywords: rule.Keywords})
			if matched {
				out.Count++
				if len(out.Items) < 5 {
					out.Items = append(out.Items, paper.PublicPaper{ID: p.ID, Title: p.Title, Abstract: p.Abstract, ArXivURL: p.ArXivURL})
				}
			}
		}
		if len(rows) < 200 {
			return out, nil
		}
	}
	out.Complete = false
	return out, nil
}
func (s *MySQLStore) Steps(ctx context.Context, u uint64, id string) ([]Step, error) {
	if _, err := s.RunByID(ctx, u, id); err != nil {
		return nil, err
	}
	rows := []Step{}
	err := s.db.WithContext(ctx).Where("run_id=?", id).Order("sequence").Find(&rows).Error
	return rows, err
}

func (s *MySQLStore) EditDraft(ctx context.Context, u uint64, id string, version uint32, input subscription.CreateInput) (out subscription.Draft, err error) {
	raw, _ := json.Marshal(input)
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockAccount(tx, u); err != nil {
			return err
		}

		var d subscription.Draft
		if err := tx.Where("id=? AND user_id=?", id, u).Take(&d).Error; err != nil {
			return notFound(err)
		}
		var c Conversation
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND user_id=?", d.ConversationID, u).Take(&c).Error; err != nil {
			return notFound(err)
		}
		if c.ActiveRunID != nil || c.LatestDraftID != id {
			return ErrConflict
		}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=?", id).Take(&d).Error; err != nil {
			return err
		}
		if d.Version != version || d.SubscriptionID != nil {
			return ErrConflict
		}
		d.Version++
		d.Payload = raw
		d.ExpiresAt = time.Now().UTC().Add(30 * time.Minute)
		if err := tx.Model(&subscription.Draft{}).Where("id=?", id).Updates(map[string]any{"version": d.Version, "payload": string(raw), "expires_at": d.ExpiresAt}).Error; err != nil {
			return err
		}
		out = d
		return nil
	})
	return
}

func (s *MySQLStore) Stats(ctx context.Context) (map[string]int, error) {
	var rows []struct {
		State string
		Count int
	}
	err := s.db.WithContext(ctx).Table("agent_runs").Select("state,COUNT(*) AS count").Group("state").Scan(&rows).Error
	out := map[string]int{}
	for _, row := range rows {
		out[row.State] = row.Count
	}
	return out, err
}

// Account -> conversation -> run/draft is shared with subscription confirmation.
// Taking the account lock before child INSERTs also orders InnoDB FK checks.
func lockAccount(tx *gorm.DB, u uint64) error {
	var row struct{ ID uint64 }
	return notFound(tx.Table("users").Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=?", u).Take(&row).Error)
}
