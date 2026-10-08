package store

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"
	"github.com/kzzz/atc-cf-bot/internal/domain"
)

type ReconcileItem struct {
	GuildID   string
	Operation Operation
}

func (s *Store) Uncertain(ctx context.Context) ([]ReconcileItem, error) {
	rows, e := s.Pool.Query(ctx, "SELECT d.guild_id,"+prefixColumns()+" FROM outbound_operations o JOIN daily_publications d ON d.id=o.daily_id WHERE o.status IN ('sending','ambiguous','needs_reconciliation') ORDER BY d.guild_id,o.sequence_no")
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	var out []ReconcileItem
	for rows.Next() {
		var item ReconcileItem
		var payload []byte
		o := &item.Operation
		e = rows.Scan(&item.GuildID, &o.ID, &o.DailyID, &o.PublicationID, &o.Sequence, &o.Kind, &payload, &o.Hash, &o.BeforeHash, &o.Status, &o.DiscordID, &o.Result, &o.SendStartedAt)
		if e != nil {
			return nil, e
		}
		if e = json.Unmarshal(payload, &o.Payload); e != nil {
			return nil, e
		}
		out = append(out, item)
	}
	return out, rows.Err()
}
func (s *Store) Decide(ctx context.Context, item ReconcileItem, r DiscordResult, decision, actor, reason string) error {
	if actor == "" || reason == "" || (decision != "adopt" && decision != "not_delivered") {
		return domain.Fail(domain.Invalid)
	}
	return s.transaction(ctx, func(tx pgx.Tx) error {
		o := item.Operation
		if _, e := tx.Exec(ctx, "SELECT guild_id FROM guild_runtime WHERE guild_id=$1 FOR UPDATE", item.GuildID); e != nil {
			return e
		}
		if decision == "adopt" {
			raw, _ := json.Marshal(r)
			tag, e := tx.Exec(ctx, "UPDATE outbound_operations SET status='confirmed',discord_id=$3,result=$4 WHERE id=$1 AND daily_id=$2 AND status IN ('sending','ambiguous','needs_reconciliation')", o.ID, o.DailyID, r.ID, raw)
			if e != nil {
				return e
			}
			if tag.RowsAffected() != 1 {
				return domain.Fail(domain.Conflict)
			}
			if o.Kind == "forum" {
				if _, e = tx.Exec(ctx, "UPDATE daily_publications SET thread_id=$3,starter_message_id=$4 WHERE id=$1 AND guild_id=$2", o.DailyID, item.GuildID, r.ThreadID, r.StarterID); e != nil {
					return e
				}
			}
			if o.PublicationID != "" {
				if _, e = tx.Exec(ctx, "UPDATE problem_publications SET first_published_at=COALESCE(first_published_at,clock_timestamp()),status='publishing' WHERE id=$1 AND guild_id=$2", o.PublicationID, item.GuildID); e != nil {
					return e
				}
			}
		} else {
			_, e := tx.Exec(ctx, "UPDATE outbound_operations SET status='prepared',last_error_code=NULL WHERE id=$1 AND daily_id=$2 AND status IN ('sending','ambiguous','needs_reconciliation')", o.ID, o.DailyID)
			if e != nil {
				return e
			}
		}
		if _, e := tx.Exec(ctx, "INSERT INTO reconciliation_decisions(id,operation_id,actor_label,decision,reason,adopted_discord_id) VALUES($1,$2,$3,$4,$5,$6)", domain.NewID(), o.ID, actor, decision, reason, r.ID); e != nil {
			return e
		}
		var unresolved bool
		if e := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM outbound_operations o JOIN daily_publications d ON d.id=o.daily_id WHERE d.guild_id=$1 AND o.status IN ('sending','ambiguous','needs_reconciliation'))", item.GuildID).Scan(&unresolved); e != nil {
			return e
		}
		if unresolved {
			return nil
		}
		if _, e := tx.Exec(ctx, "UPDATE guild_runtime SET block_reason=NULL WHERE guild_id=$1 AND block_reason='needs_reconciliation'", item.GuildID); e != nil {
			return e
		}
		_, e := tx.Exec(ctx, `UPDATE posting_jobs j SET status=CASE WHEN d.ends_at>clock_timestamp() THEN 'queued' ELSE 'expired' END,deadline_at=d.ends_at FROM daily_publications d WHERE j.guild_id=$1 AND j.daily_id=d.id AND j.status='needs_reconciliation'`, item.GuildID)
		return e
	})
}
