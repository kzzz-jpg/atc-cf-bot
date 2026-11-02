package store

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/kzzz/atc-cf-bot/internal/domain"
)

// SolveStats aggregates one user's solve history for the progress view. Days is
// keyed by the guild-local date the solve was recorded on.
type SolveStats struct {
	Days                map[string]int
	Total               int
	AtCoder, Codeforces int
}

// BacklogItem is one published, not-yet-reported problem.
type BacklogItem struct {
	PublicationID, ExternalID, URL, Date, ThreadID, MessageID string
	Platform                                                  domain.Platform
	Difficulty                                                *int
}

// SolveButton identifies the problem a 回報解題 button belongs to.
type SolveButton struct {
	Platform   domain.Platform
	ExternalID string
	Date       string
}

// ButtonSolve verifies a 回報解題 button press against the frozen outbound
// payload, mirroring ButtonStatement's message identity checks.
func (s *Store) ButtonSolve(ctx context.Context, guild, thread, message, pub string) (SolveButton, error) {
	var b SolveButton
	var platform string
	e := s.Pool.QueryRow(ctx, `SELECT r.platform,r.external_id,to_char(d.local_date,'YYYY-MM-DD') FROM problem_publications p
 JOIN daily_publications d ON (d.id,d.guild_id)=(p.daily_id,p.guild_id) JOIN outbound_operations o ON (o.publication_id,o.daily_id)=(p.id,p.daily_id) JOIN problems r ON r.id=p.problem_id
 WHERE p.id=$1 AND p.guild_id=$2 AND p.problem_id IS NOT NULL AND d.thread_id=$3 AND o.discord_id=$4 AND o.status='confirmed' AND p.status IN ('publishing','completed')
 AND NOT EXISTS(SELECT 1 FROM outbound_operations later WHERE later.publication_id=p.id AND later.sequence_no>o.sequence_no)
 AND o.payload->'components'->0->'components'->3->>'custom_id'='v1:solved:'||p.id::text`, pub, guild, thread, message).Scan(&platform, &b.ExternalID, &b.Date)
	if e != nil {
		return SolveButton{}, domain.Fail(domain.Unauthorized)
	}
	b.Platform = domain.Platform(platform)
	return b, nil
}

// ToggleSolve records a solve when none exists for this problem and removes it
// when one does, so repeated presses flip the state. The returned added flag is
// true when the solve was just recorded.
func (s *Store) ToggleSolve(ctx context.Context, guild, user, pub, localDate, source string) (bool, error) {
	added := false
	e := s.transaction(ctx, func(tx pgx.Tx) error {
		tag, e := tx.Exec(ctx, `INSERT INTO user_solves(guild_id,user_id,problem_id,publication_id,solved_on,source)
 SELECT $1,$2,p.problem_id,p.id,$4::date,$5 FROM problem_publications p WHERE p.id=$3 AND p.guild_id=$1 AND p.problem_id IS NOT NULL AND p.status IN ('publishing','completed')
 ON CONFLICT DO NOTHING`, guild, user, pub, localDate, source)
		if e != nil {
			return e
		}
		if tag.RowsAffected() == 1 {
			added = true
			return nil
		}
		tag, e = tx.Exec(ctx, `DELETE FROM user_solves s USING problem_publications p WHERE s.guild_id=$1 AND s.user_id=$2 AND p.id=$3 AND p.guild_id=$1 AND s.problem_id=p.problem_id`, guild, user, pub)
		if e != nil {
			return e
		}
		if tag.RowsAffected() != 1 {
			return domain.Fail(domain.Invalid)
		}
		return nil
	})
	return added, e
}

// MarkSolves records the given publications as solved, skipping problems the
// user already reported. It returns how many new solves were recorded.
func (s *Store) MarkSolves(ctx context.Context, guild, user string, pubs []string, localDate, source string) (int, error) {
	tag, e := s.Pool.Exec(ctx, `INSERT INTO user_solves(guild_id,user_id,problem_id,publication_id,solved_on,source)
 SELECT $1,$2,p.problem_id,p.id,$4::date,$5 FROM problem_publications p
 WHERE p.guild_id=$1 AND p.id=ANY($3::uuid[]) AND p.problem_id IS NOT NULL AND p.status IN ('publishing','completed')
 AND NOT EXISTS(SELECT 1 FROM user_solves s WHERE s.guild_id=p.guild_id AND s.user_id=$2 AND s.problem_id=p.problem_id)
 ON CONFLICT DO NOTHING`, guild, user, pubs, localDate, source)
	if e != nil {
		return 0, e
	}
	return int(tag.RowsAffected()), nil
}

// SolveStats collects every recorded day plus per-platform totals for one user.
func (s *Store) SolveStats(ctx context.Context, guild, user string) (SolveStats, error) {
	var stats SolveStats
	rows, e := s.Pool.Query(ctx, "SELECT to_char(solved_on,'YYYY-MM-DD'),count(*) FROM user_solves WHERE guild_id=$1 AND user_id=$2 GROUP BY solved_on", guild, user)
	if e != nil {
		return stats, e
	}
	defer rows.Close()
	stats.Days = map[string]int{}
	for rows.Next() {
		var day string
		var count int
		if e = rows.Scan(&day, &count); e != nil {
			return stats, e
		}
		stats.Days[day] = count
		stats.Total += count
	}
	if e = rows.Err(); e != nil {
		return stats, e
	}
	rows, e = s.Pool.Query(ctx, `SELECT r.platform,count(*) FROM user_solves s JOIN problems r ON r.id=s.problem_id WHERE s.guild_id=$1 AND s.user_id=$2 GROUP BY r.platform`, guild, user)
	if e != nil {
		return stats, e
	}
	defer rows.Close()
	for rows.Next() {
		var platform domain.Platform
		var count int
		if e = rows.Scan(&platform, &count); e != nil {
			return stats, e
		}
		switch platform {
		case domain.AtCoder:
			stats.AtCoder = count
		case domain.Codeforces:
			stats.Codeforces = count
		}
	}
	return stats, rows.Err()
}

// Backlog lists published problems since the given guild-local date that the
// user has not reported as solved, newest first. Result size is bounded so a
// long absence cannot produce an unbounded reply.
func (s *Store) Backlog(ctx context.Context, guild, user, sinceDate string, limit int) ([]BacklogItem, error) {
	rows, e := s.Pool.Query(ctx, `SELECT p.id::text,r.platform,r.external_id,r.canonical_url,to_char(d.local_date,'YYYY-MM-DD'),COALESCE(d.thread_id,''),COALESCE(msg.discord_id,''),c.difficulty
 FROM problem_publications p
 JOIN daily_publications d ON (d.id,d.guild_id)=(p.daily_id,p.guild_id) JOIN problems r ON r.id=p.problem_id
 LEFT JOIN catalog_entries c ON c.problem_id=p.problem_id AND c.snapshot_id=p.catalog_snapshot_id
 LEFT JOIN LATERAL (SELECT discord_id FROM outbound_operations o WHERE o.publication_id=p.id AND o.kind='message' AND o.status='confirmed' ORDER BY sequence_no LIMIT 1) msg ON true
 WHERE p.guild_id=$1 AND p.problem_id IS NOT NULL AND p.status IN ('publishing','completed') AND d.local_date>=$3::date
 AND NOT EXISTS(SELECT 1 FROM user_solves s WHERE s.guild_id=$1 AND s.user_id=$2 AND s.problem_id=p.problem_id)
 ORDER BY d.local_date DESC,p.slot_no LIMIT $4`, guild, user, sinceDate, limit)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	var out []BacklogItem
	for rows.Next() {
		var item BacklogItem
		var difficulty *int
		if e = rows.Scan(&item.PublicationID, &item.Platform, &item.ExternalID, &item.URL, &item.Date, &item.ThreadID, &item.MessageID, &difficulty); e != nil {
			return nil, e
		}
		item.Difficulty = difficulty
		out = append(out, item)
	}
	return out, rows.Err()
}

// LeaderboardEntry is one user's solve count within a period.
type LeaderboardEntry struct {
	UserID string
	Count  int
}

// Leaderboard returns the top solvers since the given guild-local date.
func (s *Store) Leaderboard(ctx context.Context, guild, sinceDate string) ([]LeaderboardEntry, error) {
	rows, e := s.Pool.Query(ctx, `SELECT user_id,count(*) FROM user_solves WHERE guild_id=$1 AND solved_on>=$2::date GROUP BY user_id ORDER BY count(*) DESC,min(solved_at) LIMIT 10`, guild, sinceDate)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	var out []LeaderboardEntry
	for rows.Next() {
		var entry LeaderboardEntry
		if e = rows.Scan(&entry.UserID, &entry.Count); e != nil {
			return nil, e
		}
		out = append(out, entry)
	}
	return out, rows.Err()
}
