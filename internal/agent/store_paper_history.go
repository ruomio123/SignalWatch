package agent

import (
	"context"
	"database/sql"

	"gorm.io/gorm"
)

// PaperHistory captures paired, completed questions for one immutable paper
// snapshot. Subscription history deliberately retains its separate query.
func (s *MySQLStore) PaperHistory(ctx context.Context, conversation, paperHash, excludeRunID string) (PaperConversationContext, error) {
	out := PaperConversationContext{Turns: []PaperConversationTurn{}}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Filter before LIMIT: unrelated snapshots, partial pairs, and failed
		// tasks must not crowd a valid older question out of the three turns.
		if err := tx.Table("agent_messages a").Select("u.content AS question, a.content AS answer").
			Joins("JOIN agent_runs r ON r.id=a.run_id AND r.conversation_id=a.conversation_id").
			Joins("JOIN agent_messages u ON u.run_id=r.id AND u.conversation_id=a.conversation_id AND u.role='user'").
			Where("a.conversation_id=? AND a.role='assistant' AND r.task=? AND r.state='completed' AND r.id<>?", conversation, TaskPaperFollowup, excludeRunID).
			Where("JSON_UNQUOTE(JSON_EXTRACT(a.result, '$.paper_hash'))=?", paperHash).
			Where("TRIM(u.content)<>'' AND TRIM(a.content)<>''").
			Order("a.id DESC").Limit(4).Scan(&out.Turns).Error; err != nil {
			return err
		}
		// One extra eligible pair tells the model that earlier discussion was
		// omitted, while the captured context still contains at most three.
		if len(out.Turns) > 3 {
			out.Turns = out.Turns[:3]
			out.Truncated = true
		}
		for i, j := 0, len(out.Turns)-1; i < j; i, j = i+1, j-1 {
			out.Turns[i], out.Turns[j] = out.Turns[j], out.Turns[i]
		}

		// A newer report for another snapshot does not hide an older valid
		// summary. Page through matching results to validate legacy payloads
		// without reading the entire conversation into memory.
		var before uint64
		for {
			rows := []Message{}
			q := tx.Table("agent_messages m").Select("m.*").
				Joins("JOIN agent_runs r ON r.id=m.run_id AND r.conversation_id=m.conversation_id").
				Where("m.conversation_id=? AND m.role='assistant' AND r.task=? AND r.state='completed' AND r.id<>?", conversation, TaskPaperReport, excludeRunID).
				Where("JSON_UNQUOTE(JSON_EXTRACT(m.result, '$.paper_hash'))=?", paperHash)
			if before != 0 {
				q = q.Where("m.id<?", before)
			}
			if err := q.Order("m.id DESC").Limit(20).Scan(&rows).Error; err != nil {
				return err
			}
			for _, message := range rows {
				if result, ok := validPaperReport(message, paperHash); ok {
					out.Report = result.Report
					return nil
				}
			}
			if len(rows) < 20 {
				return nil
			}
			before = rows[len(rows)-1].ID
		}
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	return out, err
}
