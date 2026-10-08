package store

import (
	"context"

	"github.com/kzzz/atc-cf-bot/internal/domain"
)

func (s *Store) ButtonStatement(ctx context.Context, guild, thread, message, pub string) (string, error) {
	var sid string
	e := s.Pool.QueryRow(ctx, `SELECT p.statement_id FROM problem_publications p JOIN daily_publications d ON (d.id,d.guild_id)=(p.daily_id,p.guild_id) JOIN outbound_operations o ON (o.publication_id,o.daily_id)=(p.id,p.daily_id)
 WHERE p.id=$1 AND p.guild_id=$2 AND d.thread_id=$3 AND o.discord_id=$4 AND o.status='confirmed' AND p.status IN ('publishing','completed')
 AND NOT EXISTS(SELECT 1 FROM outbound_operations later WHERE later.publication_id=p.id AND later.sequence_no>o.sequence_no)
 AND o.payload->'components'->0->'components'->1->>'custom_id'='v1:hint:'||p.id::text`, pub, guild, thread, message).Scan(&sid)
	if e != nil {
		return "", domain.Fail(domain.Unauthorized)
	}
	return sid, nil
}
