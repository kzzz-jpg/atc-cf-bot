package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/kzzz/atc-cf-bot/internal/domain"
)

type Daily struct {
	ID, GuildID, Date, ThreadID, StarterID, Status, BlockReason string
	StartsAt, EndsAt                                            time.Time
	Config                                                      domain.Settings
	Target                                                      int
}
type JobRef struct{ ID, DailyID, GuildID, Status string }
type AcceptRequest struct {
	GuildID, UserID, InteractionID string
	Manual                         bool
	Scheduled                      bool
	Source                         domain.Platform
	Min, Max                       *int
	AllowResume                    bool
	GuildLimit, GlobalLimit        int
}

var ErrNotDue = errors.New("schedule not due")

func scanDaily(row pgx.Row) (Daily, error) {
	var d Daily
	var raw []byte
	e := row.Scan(&d.ID, &d.GuildID, &d.Date, &d.StartsAt, &d.EndsAt, &raw, &d.Target, &d.ThreadID, &d.StarterID, &d.Status, &d.BlockReason)
	if e == nil {
		e = json.Unmarshal(raw, &d.Config)
	}
	return d, e
}

const dailyColumns = "id,guild_id,to_char(local_date,'YYYY-MM-DD'),starts_at,ends_at,config_snapshot,target_count,COALESCE(thread_id,''),COALESCE(starter_message_id,''),status,COALESCE((SELECT block_reason FROM guild_runtime WHERE guild_runtime.guild_id=daily_publications.guild_id),'')"

func (s *Store) Today(ctx context.Context, guild string) (Daily, error) {
	cfg, e := s.Config(ctx, guild)
	if e != nil {
		return Daily{}, e
	}
	now, e := s.Now(ctx)
	if e != nil {
		return Daily{}, e
	}
	cycle := domain.CycleAt(now, cfg.Settings)
	return scanDaily(s.Pool.QueryRow(ctx, "SELECT "+dailyColumns+" FROM daily_publications WHERE guild_id=$1 AND local_date=$2::date", guild, cycle.Date))
}
func (s *Store) Daily(ctx context.Context, guild, id string) (Daily, error) {
	return scanDaily(s.Pool.QueryRow(ctx, "SELECT "+dailyColumns+" FROM daily_publications WHERE guild_id=$1 AND id=$2", guild, id))
}
func (s *Store) Accept(ctx context.Context, r AcceptRequest) (JobRef, error) {
	var result JobRef
	e := s.transaction(ctx, func(tx pgx.Tx) error {
		if _, e := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(71832290)"); e != nil {
			return e
		}
		// Long lived job identity remains authoritative after receipt cleanup.
		if r.InteractionID != "" {
			e := tx.QueryRow(ctx, "SELECT id,daily_id,guild_id,status FROM posting_jobs WHERE guild_id=$1 AND interaction_id=$2", r.GuildID, r.InteractionID).Scan(&result.ID, &result.DailyID, &result.GuildID, &result.Status)
			if e == nil {
				return nil
			}
			if e != pgx.ErrNoRows {
				return e
			}
		}
		var block *string
		if e := tx.QueryRow(ctx, "SELECT block_reason FROM guild_runtime WHERE guild_id=$1 FOR UPDATE", r.GuildID).Scan(&block); e != nil {
			return e
		}
		if block != nil {
			if !r.AllowResume || *block == "needs_reconciliation" {
				return domain.Fail(domain.Conflict)
			}
			if _, e := tx.Exec(ctx, "UPDATE guild_runtime SET block_reason=NULL WHERE guild_id=$1", r.GuildID); e != nil {
				return e
			}
			if _, e := tx.Exec(ctx, `UPDATE posting_jobs j SET status='queued',available_at=clock_timestamp(),deadline_at=d.ends_at FROM daily_publications d WHERE j.guild_id=$1 AND j.daily_id=d.id AND j.status='blocked' AND d.ends_at>clock_timestamp()`, r.GuildID); e != nil {
				return e
			}
		}

		var raw []byte
		var now time.Time
		if e := tx.QueryRow(ctx, "SELECT settings,clock_timestamp() FROM guild_configs WHERE guild_id=$1 FOR UPDATE", r.GuildID).Scan(&raw, &now); e != nil {
			return e
		}
		var cfg domain.Settings
		if json.Unmarshal(raw, &cfg) != nil {
			return domain.Fail(domain.Configuration)
		}
		if cfg.ForumChannelID == "" {
			if r.Scheduled {
				return ErrNotDue
			}
			return domain.Fail(domain.Configuration)
		}
		cycle := domain.CycleAt(now, cfg)
		daily, e := scanDaily(tx.QueryRow(ctx, "SELECT "+dailyColumns+" FROM daily_publications WHERE guild_id=$1 AND local_date=$2::date", r.GuildID, cycle.Date))
		if e != nil && e != pgx.ErrNoRows {
			return e
		}
		if r.Scheduled {
			if r.Manual || r.AllowResume {
				return domain.Fail(domain.Invalid)
			}
			schedule := cfg
			if daily.ID != "" {
				schedule = daily.Config
			}
			if now.Before(domain.CycleAt(now, schedule).Due) {
				return ErrNotDue
			}
		}
		if r.Manual {
			var guildCount, globalCount int
			if e = tx.QueryRow(ctx, "SELECT count(*) FILTER(WHERE guild_id=$1),count(*) FROM posting_jobs WHERE kind='manual' AND status IN ('queued','running','retry_wait')", r.GuildID).Scan(&guildCount, &globalCount); e != nil {
				return e
			}
			if guildCount >= r.GuildLimit || globalCount >= r.GlobalLimit {
				return domain.Fail(domain.RateLimited)
			}
		}
		if e == pgx.ErrNoRows || daily.ID == "" {
			daily = Daily{ID: domain.NewID(), GuildID: r.GuildID, Date: cycle.Date, StartsAt: cycle.Start, EndsAt: cycle.End, Config: cfg, Target: cfg.DailyCount, Status: "pending"}
			if _, e = tx.Exec(ctx, "INSERT INTO daily_publications(id,guild_id,local_date,starts_at,ends_at,timezone,forum_channel_id,config_snapshot,target_count,status) VALUES($1,$2,$3::date,$4,$5,$6,$7,$8,$9,'pending')", daily.ID, r.GuildID, cycle.Date, cycle.Start, cycle.End, cfg.Timezone, cfg.ForumChannelID, raw, cfg.DailyCount); e != nil {
				return e
			}
			for n := 1; n <= cfg.DailyCount; n++ {
				p := cfg.Source(cycle.Date, n, false, domain.SecureRNG{})
				lo, hi := cfg.Bounds(p)
				if _, e = tx.Exec(ctx, "INSERT INTO problem_publications(id,daily_id,guild_id,kind,slot_no,source,min_difficulty,max_difficulty,status) VALUES($1,$2,$3,'daily',$4,$5,$6,$7,'empty')", domain.NewID(), daily.ID, r.GuildID, n, p, lo, hi); e != nil {
					return e
				}
			}
		}
		kind := "daily"
		key := "daily:" + daily.ID
		var publication any
		if r.Manual {
			kind = "manual"
			key = fmt.Sprintf("manual:%s:%s", r.GuildID, r.InteractionID)
			var slot int
			if e = tx.QueryRow(ctx, "SELECT COALESCE(max(slot_no),0)+1 FROM problem_publications WHERE guild_id=$1 AND daily_id=$2 AND kind='manual'", r.GuildID, daily.ID).Scan(&slot); e != nil {
				return e
			}
			p := r.Source
			if p == "" {
				p = daily.Config.Source(daily.Date, slot, true, domain.SecureRNG{})
			}
			if p != domain.AtCoder && p != domain.Codeforces {
				return domain.Fail(domain.Invalid)
			}
			lo, hi := daily.Config.Bounds(p)
			if r.Min != nil || r.Max != nil {
				if r.Source == "" {
					return domain.Fail(domain.Invalid)
				}
				if r.Min != nil {
					if *r.Min < lo {
						return domain.Fail(domain.Invalid)
					}
					lo = *r.Min
				}
				if r.Max != nil {
					if *r.Max > hi {
						return domain.Fail(domain.Invalid)
					}
					hi = *r.Max
				}
				if lo > hi {
					return domain.Fail(domain.Invalid)
				}
			}
			pid := domain.NewID()
			publication = pid
			if _, e = tx.Exec(ctx, "INSERT INTO problem_publications(id,daily_id,guild_id,kind,slot_no,source,min_difficulty,max_difficulty,status) VALUES($1,$2,$3,'manual',$4,$5,$6,$7,'empty')", pid, daily.ID, r.GuildID, slot, p, lo, hi); e != nil {
				return e
			}
		}
		result = JobRef{ID: domain.NewID(), DailyID: daily.ID, GuildID: r.GuildID, Status: "queued"}
		var user any
		if r.UserID != "" {
			user = r.UserID
		}
		var interaction any
		if r.InteractionID != "" {
			interaction = r.InteractionID
		}
		e = tx.QueryRow(ctx, "INSERT INTO posting_jobs(id,guild_id,daily_id,publication_id,requested_by,interaction_id,dedup_key,kind,status,deadline_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,'queued',$9) ON CONFLICT(dedup_key) DO NOTHING RETURNING id,daily_id,guild_id,status", result.ID, r.GuildID, daily.ID, publication, user, interaction, key, kind, daily.EndsAt).Scan(&result.ID, &result.DailyID, &result.GuildID, &result.Status)
		if e == pgx.ErrNoRows {
			e = tx.QueryRow(ctx, "SELECT id,daily_id,guild_id,status FROM posting_jobs WHERE guild_id=$1 AND dedup_key=$2", r.GuildID, key).Scan(&result.ID, &result.DailyID, &result.GuildID, &result.Status)
			if e != nil {
				return e
			}
			if r.AllowResume && result.Status == "failed" {
				_, e = tx.Exec(ctx, "UPDATE posting_jobs SET status='queued',available_at=clock_timestamp(),deadline_at=$2,last_error_code=NULL WHERE id=$1 AND guild_id=$3", result.ID, daily.EndsAt, r.GuildID)
				if e != nil {
					return e
				}
				result.Status = "queued"
			}
		}
		if e != nil {
			return e
		}
		if r.InteractionID != "" {
			_, e = tx.Exec(ctx, "INSERT INTO interaction_receipts(interaction_id,guild_id,kind,posting_job_id) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING", r.InteractionID, r.GuildID, kind, result.ID)
		}
		return e
	})
	return result, e
}
