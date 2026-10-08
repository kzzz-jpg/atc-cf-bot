package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/kzzz/atc-cf-bot/internal/domain"
)

type JobLease struct {
	ID, GuildID, DailyID, PublicationID, Kind, UserID, Owner string
	Epoch                                                    int64
	Deadline                                                 time.Time
}

func (s *Store) ClaimJob(ctx context.Context, owner string) (JobLease, error) {
	var j JobLease
	j.Owner = owner
	e := s.transaction(ctx, func(tx pgx.Tx) error {
		e := tx.QueryRow(ctx, `SELECT j.id,j.guild_id,j.daily_id,COALESCE(j.publication_id::text,''),j.kind,COALESCE(j.requested_by,'') FROM posting_jobs j JOIN guild_runtime g ON g.guild_id=j.guild_id
   WHERE j.status IN ('queued','retry_wait') AND j.available_at<=clock_timestamp() AND j.deadline_at>clock_timestamp() AND g.block_reason IS NULL
   AND (g.resume_job_id IS NULL OR g.resume_job_id=j.id) AND (g.lease_until IS NULL OR g.lease_until<=clock_timestamp())
   AND NOT EXISTS(SELECT 1 FROM posting_jobs prev WHERE prev.guild_id=j.guild_id AND prev.created_at<j.created_at AND prev.status IN ('queued','running','retry_wait'))
   ORDER BY j.available_at,j.id LIMIT 1 FOR UPDATE OF g SKIP LOCKED`).Scan(&j.ID, &j.GuildID, &j.DailyID, &j.PublicationID, &j.Kind, &j.UserID)
		if e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, "SELECT id FROM posting_jobs WHERE id=$1 FOR UPDATE", j.ID); e != nil {
			return e
		}
		e = tx.QueryRow(ctx, "UPDATE guild_runtime SET lease_owner=$2,lease_until=clock_timestamp()+make_interval(secs=>$3),lease_epoch=lease_epoch+1 WHERE guild_id=$1 RETURNING lease_epoch", j.GuildID, owner, s.LeaseSeconds).Scan(&j.Epoch)
		if e != nil {
			return e
		}
		return tx.QueryRow(ctx, `UPDATE posting_jobs j SET status='running',lease_owner=$2,lease_until=clock_timestamp()+make_interval(secs=>$3),lease_epoch=$4,attempts=attempts+1,deadline_at=LEAST(clock_timestamp()+interval '30 minutes',d.ends_at) FROM daily_publications d WHERE j.id=$1 AND d.id=j.daily_id RETURNING j.deadline_at`, j.ID, owner, s.LeaseSeconds, j.Epoch).Scan(&j.Deadline)
	})
	return j, e
}
func fence(ctx context.Context, tx pgx.Tx, j JobLease) error {
	if _, e := tx.Exec(ctx, "SELECT guild_id FROM guild_runtime WHERE guild_id=$1 FOR UPDATE", j.GuildID); e != nil {
		return e
	}
	if _, e := tx.Exec(ctx, "SELECT id FROM posting_jobs WHERE id=$1 AND guild_id=$2 FOR UPDATE", j.ID, j.GuildID); e != nil {
		return e
	}
	var ok bool
	e := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM guild_runtime g JOIN posting_jobs j ON j.guild_id=g.guild_id WHERE j.id=$1 AND j.guild_id=$2 AND g.lease_owner=$3 AND j.lease_owner=$3 AND g.lease_epoch=$4 AND j.lease_epoch=$4 AND g.lease_until>clock_timestamp() AND j.lease_until>clock_timestamp() AND j.status='running' AND j.deadline_at>clock_timestamp())`, j.ID, j.GuildID, j.Owner, j.Epoch).Scan(&ok)
	if e != nil {
		return e
	}
	if !ok {
		return domain.Fail(domain.Conflict)
	}
	return nil
}
func (s *Store) CheckLease(ctx context.Context, j JobLease) error {
	return s.transaction(ctx, func(tx pgx.Tx) error { return fence(ctx, tx, j) })
}
func (s *Store) Heartbeat(ctx context.Context, j JobLease) error {
	return s.transaction(ctx, func(tx pgx.Tx) error {
		if e := fence(ctx, tx, j); e != nil {
			return e
		}
		if _, e := tx.Exec(ctx, "UPDATE guild_runtime SET lease_until=clock_timestamp()+make_interval(secs=>$2) WHERE guild_id=$1 AND lease_owner=$3 AND lease_epoch=$4", j.GuildID, s.LeaseSeconds, j.Owner, j.Epoch); e != nil {
			return e
		}
		_, e := tx.Exec(ctx, "UPDATE posting_jobs SET lease_until=clock_timestamp()+make_interval(secs=>$2) WHERE id=$1 AND guild_id=$3 AND lease_owner=$4 AND lease_epoch=$5", j.ID, s.LeaseSeconds, j.GuildID, j.Owner, j.Epoch)
		return e
	})
}
func (s *Store) FinishJob(ctx context.Context, j JobLease, status, code string, retryable bool) error {
	return s.transaction(ctx, func(tx pgx.Tx) error {
		// Finishing cancellation must still be fenced by owner/epoch, but may run just
		// after the execution deadline. It cannot overwrite a new owner's progress.
		if _, e := tx.Exec(ctx, "SELECT guild_id FROM guild_runtime WHERE guild_id=$1 FOR UPDATE", j.GuildID); e != nil {
			return e
		}
		var ok bool
		e := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM guild_runtime WHERE guild_id=$1 AND lease_owner=$2 AND lease_epoch=$3 AND lease_until>clock_timestamp())", j.GuildID, j.Owner, j.Epoch).Scan(&ok)
		if e != nil {
			return e
		}
		if !ok {
			return domain.Fail(domain.Conflict)
		}
		if status == "retry_wait" {
			e = tx.QueryRow(ctx, "UPDATE posting_jobs j SET status=CASE WHEN auto_retry_count<2 AND d.ends_at>clock_timestamp()+interval '15 minutes' THEN 'retry_wait' ELSE 'failed' END,auto_retry_count=LEAST(auto_retry_count+1,2),available_at=clock_timestamp()+interval '15 minutes',deadline_at=d.ends_at,lease_owner=NULL,lease_until=NULL,last_error_code=$2,retryable=$3 FROM daily_publications d WHERE j.id=$1 AND j.guild_id=$4 AND j.lease_owner=$5 AND j.lease_epoch=$6 AND d.id=j.daily_id RETURNING j.status", j.ID, code, retryable, j.GuildID, j.Owner, j.Epoch).Scan(&status)
		} else if status == "queued" {
			e = tx.QueryRow(ctx, "UPDATE posting_jobs j SET status=CASE WHEN d.ends_at>clock_timestamp() THEN 'queued' ELSE 'expired' END,available_at=clock_timestamp(),deadline_at=d.ends_at,lease_owner=NULL,lease_until=NULL,last_error_code=$2,retryable=false FROM daily_publications d WHERE j.id=$1 AND j.guild_id=$3 AND j.lease_owner=$4 AND j.lease_epoch=$5 AND d.id=j.daily_id RETURNING j.status", j.ID, code, j.GuildID, j.Owner, j.Epoch).Scan(&status)
		} else {
			e = tx.QueryRow(ctx, "UPDATE posting_jobs SET status=$2,lease_owner=NULL,lease_until=NULL,last_error_code=$3,retryable=$4 WHERE id=$1 AND guild_id=$5 AND lease_owner=$6 AND lease_epoch=$7 RETURNING status", j.ID, status, code, retryable, j.GuildID, j.Owner, j.Epoch).Scan(&status)
		}
		if e != nil {
			return e
		}
		if status != "succeeded" && status != "queued" {
			if _, e = tx.Exec(ctx, `UPDATE problem_publications SET status=CASE WHEN $3='expired' THEN 'expired' WHEN first_published_at IS NULL THEN 'failed' ELSE 'publishing' END WHERE daily_id=$1 AND guild_id=$2 AND status<>'completed' AND (($4='daily' AND kind='daily') OR id::text=$5)`, j.DailyID, j.GuildID, status, j.Kind, j.PublicationID); e != nil {
				return e
			}
		}
		if j.Kind == "daily" && status != "succeeded" {
			dailyStatus := status
			if status == "retry_wait" {
				dailyStatus = "failed"
			} else if status == "queued" {
				dailyStatus = "pending"
			}
			if _, e = tx.Exec(ctx, `UPDATE daily_publications SET status=CASE WHEN EXISTS(SELECT 1 FROM problem_publications WHERE daily_id=$1 AND kind='daily' AND first_published_at IS NOT NULL) AND $3 NOT IN ('needs_reconciliation','blocked','expired') THEN 'partial' ELSE $3 END WHERE id=$1 AND guild_id=$2 AND status<>'completed'`, j.DailyID, j.GuildID, dailyStatus); e != nil {
				return e
			}
		}
		_, e = tx.Exec(ctx, `UPDATE guild_runtime SET lease_owner=NULL,lease_until=NULL,block_reason=CASE WHEN $4 IN ('blocked','needs_reconciliation') THEN $5 ELSE block_reason END,resume_job_id=CASE WHEN $4 IN ('succeeded','failed','expired','blocked') THEN NULL ELSE resume_job_id END WHERE guild_id=$1 AND lease_owner=$2 AND lease_epoch=$3`, j.GuildID, j.Owner, j.Epoch, status, code)
		return e
	})
}
func (s *Store) Recover(ctx context.Context) error {
	return s.transaction(ctx, func(tx pgx.Tx) error {
		if _, e := tx.Exec(ctx, "UPDATE generation_entries SET status='failed',owner=NULL,lease_until=NULL WHERE status IN ('pending','running')"); e != nil {
			return e
		}
		if _, e := tx.Exec(ctx, "UPDATE outbound_operations SET status='ambiguous' WHERE status='sending'"); e != nil {
			return e
		}
		if _, e := tx.Exec(ctx, `UPDATE guild_runtime g SET lease_owner=NULL,lease_until=NULL,block_reason=CASE WHEN EXISTS(SELECT 1 FROM outbound_operations o JOIN daily_publications d ON d.id=o.daily_id WHERE d.guild_id=g.guild_id AND o.status IN ('ambiguous','needs_reconciliation')) THEN 'needs_reconciliation' ELSE block_reason END`); e != nil {
			return e
		}
		_, e := tx.Exec(ctx, `UPDATE posting_jobs j SET status=CASE WHEN g.block_reason='needs_reconciliation' THEN 'needs_reconciliation' WHEN d.ends_at<=clock_timestamp() THEN 'expired' ELSE 'queued' END,lease_owner=NULL,lease_until=NULL,deadline_at=d.ends_at FROM guild_runtime g,daily_publications d WHERE j.guild_id=g.guild_id AND j.daily_id=d.id AND j.status='running'`)
		return e
	})
}
func (s *Store) Expire(ctx context.Context) error {
	return s.transaction(ctx, func(tx pgx.Tx) error {
		if _, e := tx.Exec(ctx, "DELETE FROM interaction_receipts WHERE expires_at<=clock_timestamp()"); e != nil {
			return e
		}
		if _, e := tx.Exec(ctx, "UPDATE posting_jobs j SET status='expired' FROM daily_publications d WHERE d.id=j.daily_id AND d.ends_at<=clock_timestamp() AND j.status IN ('queued','retry_wait')"); e != nil {
			return e
		}
		if _, e := tx.Exec(ctx, "UPDATE daily_publications SET status='expired' WHERE ends_at<=clock_timestamp() AND status NOT IN ('completed','needs_reconciliation','blocked','expired')"); e != nil {
			return e
		}
		if _, e := tx.Exec(ctx, `DELETE FROM problem_reservations r USING problem_publications p,daily_publications d WHERE r.publication_id=p.id AND p.daily_id=d.id AND d.ends_at<=clock_timestamp() AND NOT EXISTS(SELECT 1 FROM outbound_operations o WHERE o.publication_id=p.id AND o.status IN ('sending','ambiguous','needs_reconciliation'))`); e != nil {
			return e
		}
		_, e := tx.Exec(ctx, "UPDATE guild_runtime g SET resume_job_id=NULL WHERE resume_job_id IN (SELECT id FROM posting_jobs WHERE status='expired') AND block_reason IS NULL")
		return e
	})
}

type Publication struct {
	ID, DailyID, GuildID, Kind, Status, StatementID, GenerationKey string
	Slot, Min, Max, Attempts                                       int
	Source                                                         domain.Platform
	Ref                                                            domain.ProblemRef
}

func (s *Store) Publications(ctx context.Context, j JobLease) ([]Publication, error) {
	rows, e := s.Pool.Query(ctx, `SELECT p.id,p.daily_id,p.guild_id,p.kind,p.slot_no,p.source,p.min_difficulty,p.max_difficulty,p.candidate_attempts,p.status,COALESCE(p.statement_id::text,''),COALESCE(p.generation_key,''),COALESCE(p.problem_id::text,''),COALESCE(r.external_id,''),COALESCE(r.canonical_url,''),p.catalog_snapshot_id,c.difficulty FROM problem_publications p LEFT JOIN problems r ON r.id=p.problem_id LEFT JOIN catalog_entries c ON c.problem_id=p.problem_id AND c.snapshot_id=p.catalog_snapshot_id WHERE p.guild_id=$1 AND p.daily_id=$2 AND (($3='daily' AND p.kind='daily') OR p.id::text=$4) ORDER BY p.slot_no`, j.GuildID, j.DailyID, j.Kind, j.PublicationID)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	var out []Publication
	for rows.Next() {
		var p Publication
		var snapshot *string
		if e = rows.Scan(&p.ID, &p.DailyID, &p.GuildID, &p.Kind, &p.Slot, &p.Source, &p.Min, &p.Max, &p.Attempts, &p.Status, &p.StatementID, &p.GenerationKey, &p.Ref.ID, &p.Ref.ExternalID, &p.Ref.URL, &snapshot, &p.Ref.Difficulty); e != nil {
			return nil, e
		}
		if snapshot != nil {
			p.Ref.CatalogSnapshotID = *snapshot
		}
		p.Ref.Platform = p.Source
		out = append(out, p)
	}
	return out, rows.Err()
}
func (s *Store) Candidates(ctx context.Context, j JobLease, p Publication, dedup int) ([]domain.ProblemRef, error) {
	rows, e := s.Pool.Query(ctx, `SELECT h.snapshot_id,r.id,r.platform,r.external_id,r.canonical_url,c.contest_id,c.difficulty,c.contest_end_at,c.contest_finished,c.kind,c.metadata
 FROM catalog_heads h JOIN catalog_snapshots snap ON snap.id=h.snapshot_id AND snap.status='ready' JOIN catalog_entries c ON c.snapshot_id=h.snapshot_id JOIN problems r ON r.id=c.problem_id
 WHERE h.platform=$1 AND c.difficulty BETWEEN $2 AND $3 AND c.kind='programming' AND c.contest_finished AND c.contest_end_at<=clock_timestamp()
 AND NOT EXISTS(SELECT 1 FROM problem_publications pp WHERE pp.guild_id=$4 AND pp.problem_id=r.id AND (pp.daily_id=$5 OR pp.first_published_at>clock_timestamp()-make_interval(days=>$6)))
 AND NOT EXISTS(SELECT 1 FROM problem_reservations pr WHERE pr.guild_id=$4 AND pr.problem_id=r.id)
 AND NOT EXISTS(SELECT 1 FROM problem_publications attempted WHERE attempted.id=$8 AND attempted.guild_id=$4 AND r.id=ANY(attempted.attempted_problem_ids))
 AND NOT EXISTS(SELECT 1 FROM source_rejections sr WHERE sr.problem_id=r.id AND sr.parser_version=$7 AND (sr.recheck_after IS NULL OR sr.recheck_after>clock_timestamp()))`, p.Source, p.Min, p.Max, j.GuildID, j.DailyID, dedup, "html-v1", p.ID)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	var out []domain.ProblemRef
	for rows.Next() {
		var r domain.ProblemRef
		if e = rows.Scan(&r.CatalogSnapshotID, &r.ID, &r.Platform, &r.ExternalID, &r.URL, &r.ContestID, &r.Difficulty, &r.ContestEnd, &r.Finished, &r.Kind, &r.Metadata); e != nil {
			return nil, e
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
func (s *Store) Reserve(ctx context.Context, j JobLease, p Publication, r domain.ProblemRef, dedup int) error {
	return s.transaction(ctx, func(tx pgx.Tx) error {
		if e := fence(ctx, tx, j); e != nil {
			return e
		}
		if _, e := tx.Exec(ctx, "SELECT guild_id FROM guild_runtime WHERE guild_id=$1 FOR UPDATE", j.GuildID); e != nil {
			return e
		}
		var exists bool
		e := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM problem_publications WHERE guild_id=$1 AND problem_id=$2 AND (daily_id=$3 OR first_published_at>clock_timestamp()-make_interval(days=>$4))) OR EXISTS(SELECT 1 FROM problem_reservations WHERE guild_id=$1 AND problem_id=$2)`, j.GuildID, r.ID, j.DailyID, dedup).Scan(&exists)
		if e != nil {
			return e
		}
		if exists {
			return domain.Fail(domain.Conflict)
		}
		var eligible bool
		if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM catalog_entries c JOIN catalog_snapshots snap ON snap.id=c.snapshot_id WHERE c.problem_id=$1 AND c.snapshot_id=$2 AND c.platform=$3 AND c.difficulty BETWEEN $4 AND $5 AND c.contest_finished AND c.contest_end_at<=clock_timestamp() AND c.kind='programming' AND snap.status='ready')`, r.ID, r.CatalogSnapshotID, p.Source, p.Min, p.Max).Scan(&eligible); e != nil {
			return e
		}
		if !eligible {
			return domain.Fail(domain.Conflict)
		}
		tag, e := tx.Exec(ctx, "UPDATE problem_publications SET problem_id=$3,catalog_snapshot_id=$4,candidate_attempts=candidate_attempts+1,attempted_problem_ids=array_append(attempted_problem_ids,$3::uuid),status='reserved' WHERE id=$1 AND guild_id=$2 AND candidate_attempts<5 AND status IN ('empty','failed')", p.ID, j.GuildID, r.ID, r.CatalogSnapshotID)
		if e != nil {
			return e
		}
		if tag.RowsAffected() != 1 {
			return domain.Fail(domain.Conflict)
		}
		_, e = tx.Exec(ctx, "INSERT INTO problem_reservations(guild_id,problem_id,publication_id) VALUES($1,$2,$3)", j.GuildID, r.ID, p.ID)
		return e
	})
}
func (s *Store) RejectCandidate(ctx context.Context, j JobLease, p Publication, permanent bool) error {
	return s.transaction(ctx, func(tx pgx.Tx) error {
		if e := fence(ctx, tx, j); e != nil {
			return e
		}
		if permanent {
			if _, e := tx.Exec(ctx, "INSERT INTO source_rejections(problem_id,parser_version,reason_code) SELECT problem_id,'html-v1','unsupported' FROM problem_publications WHERE id=$1 AND guild_id=$2 ON CONFLICT(problem_id,parser_version) DO NOTHING", p.ID, j.GuildID); e != nil {
				return e
			}
		}
		if _, e := tx.Exec(ctx, "DELETE FROM problem_reservations WHERE publication_id=$1 AND guild_id=$2", p.ID, j.GuildID); e != nil {
			return e
		}
		_, e := tx.Exec(ctx, "UPDATE problem_publications SET problem_id=NULL,statement_id=NULL,generation_key=NULL,status='empty' WHERE id=$1 AND guild_id=$2 AND first_published_at IS NULL", p.ID, j.GuildID)
		return e
	})
}
func (s *Store) ReadyPublication(ctx context.Context, j JobLease, pid, sid, key string) error {
	return s.transaction(ctx, func(tx pgx.Tx) error {
		if e := fence(ctx, tx, j); e != nil {
			return e
		}
		tag, e := tx.Exec(ctx, "UPDATE problem_publications SET statement_id=$3,generation_key=$4,status='ready' WHERE id=$1 AND guild_id=$2 AND EXISTS(SELECT 1 FROM generation_entries WHERE cache_key=$4 AND statement_id=$3 AND status='ready')", pid, j.GuildID, sid, key)
		if e == nil && tag.RowsAffected() != 1 {
			return domain.Fail(domain.Conflict)
		}
		return e
	})
}
func decode[T any](b []byte) (T, error) { var v T; e := json.Unmarshal(b, &v); return v, e }

// RecoverExpired is safe while workers run: it locks the guild before inspecting
// a job and fences the lost owner using an incremented epoch.
func (s *Store) RecoverExpired(ctx context.Context) error {
	return s.transaction(ctx, func(tx pgx.Tx) error {
		var guild string
		e := tx.QueryRow(ctx, "SELECT guild_id FROM guild_runtime WHERE lease_until<=clock_timestamp() ORDER BY guild_id LIMIT 1 FOR UPDATE SKIP LOCKED").Scan(&guild)
		if e == pgx.ErrNoRows {
			return nil
		}
		if e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, "UPDATE outbound_operations o SET status='ambiguous' FROM daily_publications d WHERE o.daily_id=d.id AND d.guild_id=$1 AND o.status='sending'", guild); e != nil {
			return e
		}
		var unresolved bool
		if e = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM outbound_operations o JOIN daily_publications d ON d.id=o.daily_id WHERE d.guild_id=$1 AND o.status IN ('ambiguous','needs_reconciliation'))", guild).Scan(&unresolved); e != nil {
			return e
		}
		status := "queued"
		var block any
		if unresolved {
			status = "needs_reconciliation"
			block = "needs_reconciliation"
		}
		if _, e = tx.Exec(ctx, `UPDATE posting_jobs j SET status=CASE WHEN $2='needs_reconciliation' THEN $2 WHEN d.ends_at<=clock_timestamp() THEN 'expired' ELSE 'queued' END,lease_owner=NULL,lease_until=NULL,deadline_at=d.ends_at FROM daily_publications d WHERE j.guild_id=$1 AND j.daily_id=d.id AND j.status='running'`, guild, status); e != nil {
			return e
		}
		_, e = tx.Exec(ctx, "UPDATE guild_runtime SET lease_owner=NULL,lease_until=NULL,lease_epoch=lease_epoch+1,block_reason=COALESCE($2,block_reason) WHERE guild_id=$1", guild, block)
		return e
	})
}
