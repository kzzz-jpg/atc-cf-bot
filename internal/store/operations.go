package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/kzzz/atc-cf-bot/internal/domain"
	"github.com/kzzz/atc-cf-bot/internal/render"
)

type Operation struct {
	ID, DailyID, PublicationID, Kind, Status, DiscordID, Hash, BeforeHash string
	Sequence                                                              int
	Payload                                                               render.Payload
	Result                                                                json.RawMessage
	SendStartedAt                                                         *time.Time
}
type DiscordResult struct {
	ID          string              `json:"id"`
	ThreadID    string              `json:"thread_id,omitempty"`
	StarterID   string              `json:"starter_id,omitempty"`
	Attachments []render.Attachment `json:"attachments,omitempty"`
}

const opCols = "id,daily_id,COALESCE(publication_id::text,''),sequence_no,kind,payload,payload_hash,COALESCE(expected_before_hash,''),status,COALESCE(discord_id,''),result,send_started_at"

func scanOperation(row pgx.Row) (Operation, error) {
	var o Operation
	var payload []byte
	e := row.Scan(&o.ID, &o.DailyID, &o.PublicationID, &o.Sequence, &o.Kind, &payload, &o.Hash, &o.BeforeHash, &o.Status, &o.DiscordID, &o.Result, &o.SendStartedAt)
	if e == nil {
		e = json.Unmarshal(payload, &o.Payload)
	}
	return o, e
}
func (s *Store) PrepareOperations(ctx context.Context, j JobLease, pid, kind string, payloads []render.Payload, before string) error {
	if len(payloads) == 0 || (kind == "forum" && (len(payloads) != 1 || pid != "")) || (kind == "message" && pid == "") {
		return domain.Fail(domain.Invalid)
	}
	return s.transaction(ctx, func(tx pgx.Tx) error {
		if e := fence(ctx, tx, j); e != nil {
			return e
		}
		var exists bool
		e := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM outbound_operations WHERE daily_id=$1 AND (($2='' AND kind=$3) OR publication_id::text=$2))", j.DailyID, pid, kind).Scan(&exists)
		if e != nil || exists {
			return e
		}
		var seq int
		if e = tx.QueryRow(ctx, "SELECT COALESCE(max(sequence_no),0) FROM outbound_operations WHERE daily_id=$1", j.DailyID).Scan(&seq); e != nil {
			return e
		}
		for _, p := range payloads {
			seq++
			id := domain.NewID()
			p.Nonce = id[:8] + id[9:13] + id[14:18] + id[19:23]
			p.EnforceNonce = true
			raw, e := json.Marshal(p)
			if e != nil {
				return e
			}
			var pub any
			if pid != "" {
				pub = pid
			}
			var bh any
			if before != "" {
				bh = before
			}
			_, e = tx.Exec(ctx, "INSERT INTO outbound_operations(id,daily_id,publication_id,sequence_no,kind,payload,payload_hash,renderer_version,expected_before_hash,marker,status) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,'prepared')", id, j.DailyID, pub, seq, kind, raw, domain.Hash(raw), render.Version, bh, j.DailyID+":"+stringID(seq))
			if e != nil {
				return e
			}
		}
		return nil
	})
}
func stringID(n int) string { return strconv.Itoa(n) }
func (s *Store) Operations(ctx context.Context, guild, daily, pid, kind string) ([]Operation, error) {
	rows, e := s.Pool.Query(ctx, "SELECT "+prefixColumns()+" FROM outbound_operations o JOIN daily_publications d ON d.id=o.daily_id WHERE d.guild_id=$1 AND o.daily_id=$2 AND (($3='' AND o.kind=$4) OR o.publication_id::text=$3) ORDER BY o.sequence_no", guild, daily, pid, kind)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	var out []Operation
	for rows.Next() {
		o, e := scanOperation(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, o)
	}
	return out, rows.Err()
}
func prefixColumns() string {
	return "o.id,o.daily_id,COALESCE(o.publication_id::text,''),o.sequence_no,o.kind,o.payload,o.payload_hash,COALESCE(o.expected_before_hash,''),o.status,COALESCE(o.discord_id,''),o.result,o.send_started_at"
}
func (s *Store) MarkSending(ctx context.Context, j JobLease, o *Operation) error {
	return s.transaction(ctx, func(tx pgx.Tx) error {
		if e := fence(ctx, tx, j); e != nil {
			return e
		}
		var previous bool
		e := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM outbound_operations WHERE daily_id=$1 AND sequence_no<$2 AND status<>'confirmed')", j.DailyID, o.Sequence).Scan(&previous)
		if e != nil {
			return e
		}
		if previous {
			return domain.Fail(domain.Conflict)
		}
		e = tx.QueryRow(ctx, "UPDATE outbound_operations SET status='sending',send_started_at=clock_timestamp() WHERE id=$1 AND daily_id=$2 AND status IN ('prepared','failed') RETURNING send_started_at", o.ID, j.DailyID).Scan(&o.SendStartedAt)
		if e == pgx.ErrNoRows {
			return domain.Fail(domain.Conflict)
		}
		return e
	})
}
func (s *Store) RecordResult(ctx context.Context, j JobLease, o Operation, r DiscordResult) error {
	return s.transaction(ctx, func(tx pgx.Tx) error {
		if e := fence(ctx, tx, j); e != nil {
			return e
		}
		raw, _ := json.Marshal(r)
		tag, e := tx.Exec(ctx, "UPDATE outbound_operations SET status='confirmed',discord_id=$3,result=$4 WHERE id=$1 AND daily_id=$2 AND status IN ('sending','ambiguous','needs_reconciliation')", o.ID, j.DailyID, r.ID, raw)
		if e != nil {
			return e
		}
		if tag.RowsAffected() != 1 {
			return domain.Fail(domain.Conflict)
		}
		if _, e = tx.Exec(ctx, `UPDATE guild_runtime g SET block_reason=NULL WHERE guild_id=$1 AND block_reason='needs_reconciliation' AND NOT EXISTS(SELECT 1 FROM outbound_operations o JOIN daily_publications d ON d.id=o.daily_id WHERE d.guild_id=g.guild_id AND o.status IN ('sending','ambiguous','needs_reconciliation'))`, j.GuildID); e != nil {
			return e
		}
		if o.Kind == "forum" {
			_, e = tx.Exec(ctx, "UPDATE daily_publications SET thread_id=$3,starter_message_id=$4 WHERE id=$1 AND guild_id=$2", j.DailyID, j.GuildID, r.ThreadID, r.StarterID)
			return e
		}
		if o.PublicationID != "" {
			if _, e = tx.Exec(ctx, "UPDATE problem_publications SET first_published_at=COALESCE(first_published_at,clock_timestamp()),status='publishing' WHERE id=$1 AND guild_id=$2", o.PublicationID, j.GuildID); e != nil {
				return e
			}
			_, e = tx.Exec(ctx, "UPDATE guild_runtime SET resume_job_id=$2 WHERE guild_id=$1 AND lease_owner=$3 AND lease_epoch=$4", j.GuildID, j.ID, j.Owner, j.Epoch)
		}
		return e
	})
}
func (s *Store) MarkAmbiguous(ctx context.Context, guild string, o Operation) error {
	return s.transaction(ctx, func(tx pgx.Tx) error {
		if _, e := tx.Exec(ctx, "UPDATE outbound_operations SET status='ambiguous',last_error_code='ambiguous' WHERE id=$1 AND daily_id IN (SELECT id FROM daily_publications WHERE guild_id=$2) AND status='sending'", o.ID, guild); e != nil {
			return e
		}
		_, e := tx.Exec(ctx, "UPDATE guild_runtime SET block_reason='needs_reconciliation' WHERE guild_id=$1", guild)
		return e
	})
}
func (s *Store) MarkRejected(ctx context.Context, j JobLease, o Operation) error {
	return s.transaction(ctx, func(tx pgx.Tx) error {
		if e := fence(ctx, tx, j); e != nil {
			return e
		}
		_, e := tx.Exec(ctx, "UPDATE outbound_operations SET status='failed' WHERE id=$1 AND daily_id=$2 AND status='sending'", o.ID, j.DailyID)
		return e
	})
}
func (s *Store) CompletePublication(ctx context.Context, j JobLease, pid string) error {
	return s.transaction(ctx, func(tx pgx.Tx) error {
		if e := fence(ctx, tx, j); e != nil {
			return e
		}
		var count, incomplete int
		if e := tx.QueryRow(ctx, "SELECT count(*),count(*) FILTER(WHERE status<>'confirmed') FROM outbound_operations WHERE publication_id=$1 AND daily_id=$2", pid, j.DailyID).Scan(&count, &incomplete); e != nil {
			return e
		}
		if count == 0 || incomplete != 0 {
			return domain.Fail(domain.Conflict)
		}
		if _, e := tx.Exec(ctx, "UPDATE problem_publications SET status='completed',completed_at=clock_timestamp() WHERE id=$1 AND guild_id=$2", pid, j.GuildID); e != nil {
			return e
		}
		if _, e := tx.Exec(ctx, "DELETE FROM problem_reservations WHERE publication_id=$1 AND guild_id=$2", pid, j.GuildID); e != nil {
			return e
		}
		_, e := tx.Exec(ctx, "UPDATE guild_runtime SET resume_job_id=NULL WHERE guild_id=$1 AND lease_owner=$2 AND lease_epoch=$3", j.GuildID, j.Owner, j.Epoch)
		return e
	})
}
func (s *Store) RefreshDaily(ctx context.Context, guild, id string) error {
	_, e := s.Pool.Exec(ctx, `UPDATE daily_publications d SET status=CASE WHEN (SELECT count(*) FROM problem_publications p WHERE p.daily_id=d.id AND p.kind='daily' AND p.status='completed')=d.target_count THEN 'completed' WHEN EXISTS(SELECT 1 FROM problem_publications p WHERE p.daily_id=d.id AND p.kind='daily' AND p.first_published_at IS NOT NULL) THEN 'partial' ELSE 'pending' END WHERE d.id=$1 AND d.guild_id=$2 AND d.status NOT IN ('expired','blocked','needs_reconciliation')`, id, guild)
	return e
}
func (s *Store) PrepareOverview(ctx context.Context, j JobLease, payload render.Payload, message string) (Operation, error) {
	var result Operation
	e := s.transaction(ctx, func(tx pgx.Tx) error {
		if e := fence(ctx, tx, j); e != nil {
			return e
		}
		previous, e := scanOperation(tx.QueryRow(ctx, "SELECT "+opCols+" FROM outbound_operations WHERE daily_id=$1 AND kind IN ('forum','overview') ORDER BY sequence_no DESC LIMIT 1", j.DailyID))
		if e != nil {
			return e
		}
		if previous.Status != "confirmed" {
			result = previous
			return nil
		}
		if previous.Payload.Content == payload.Content {
			result = previous
			return nil
		}
		var sequence int
		if e = tx.QueryRow(ctx, "SELECT COALESCE(max(sequence_no),0)+1 FROM outbound_operations WHERE daily_id=$1", j.DailyID).Scan(&sequence); e != nil {
			return e
		}
		raw, _ := json.Marshal(payload)
		id := domain.NewID()
		before := domain.Hash([]byte(previous.Payload.Content))
		if _, e = tx.Exec(ctx, "INSERT INTO outbound_operations(id,daily_id,sequence_no,kind,payload,payload_hash,renderer_version,expected_before_hash,marker,status,discord_id) VALUES($1,$2,$3,'overview',$4,$5,$6,$7,$8,'prepared',$9)", id, j.DailyID, sequence, raw, domain.Hash(raw), render.Version, before, j.DailyID+":"+stringID(sequence), message); e != nil {
			return e
		}
		result = Operation{ID: id, DailyID: j.DailyID, Sequence: sequence, Kind: "overview", Status: "prepared", DiscordID: message, BeforeHash: before, Payload: payload}
		return nil
	})
	return result, e
}
func (s *Store) OverviewText(ctx context.Context, guild, daily string) (string, error) {
	d, e := s.Daily(ctx, guild, daily)
	if e != nil {
		return "", e
	}
	rows, e := s.Pool.Query(ctx, `SELECT p.kind,p.slot_no,p.status,COALESCE(r.platform||' '||r.external_id,''),COALESCE((SELECT discord_id FROM outbound_operations o WHERE o.publication_id=p.id AND o.status='confirmed' AND o.kind='message' ORDER BY sequence_no LIMIT 1),'') FROM problem_publications p LEFT JOIN problems r ON r.id=p.problem_id WHERE p.guild_id=$1 AND p.daily_id=$2 ORDER BY p.kind,p.slot_no`, guild, daily)
	if e != nil {
		return "", e
	}
	defer rows.Close()
	text := fmt.Sprintf("每日題目｜%s\n每日原定 %d 題；狀態：%s\nAI 翻譯，請核對原站。\n", d.Date, d.Target, d.Status)
	for rows.Next() {
		var kind, status, name, message string
		var slot int
		if e = rows.Scan(&kind, &slot, &status, &name, &message); e != nil {
			return "", e
		}
		label := fmt.Sprintf("題目 %d", slot)
		if kind == "manual" {
			label = fmt.Sprintf("加題 #%d", slot)
		}
		line := fmt.Sprintf("%s：%s（%s）", label, name, status)
		if message != "" {
			line += " https://discord.com/channels/" + guild + "/" + d.ThreadID + "/" + message
		}
		if render.Units(text+line+"\n") <= 1900 {
			text += line + "\n"
		}
	}
	return text, rows.Err()
}
