package store

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"
	"github.com/kzzz/atc-cf-bot/internal/domain"
	"github.com/kzzz/atc-cf-bot/internal/source"
)

func (s *Store) SaveCatalog(ctx context.Context, snap domain.CatalogSnapshot) error {
	if len(snap.Entries) == 0 {
		return domain.Fail(domain.Unsupported)
	}
	id := domain.NewID()
	return s.transaction(ctx, func(tx pgx.Tx) error {
		if _, e := tx.Exec(ctx, "INSERT INTO catalog_snapshots(id,platform,status,source_hash,parser_version) VALUES($1,$2,'building',$3,$4)", id, snap.Platform, snap.SourceHash, source.ParserVersion); e != nil {
			return e
		}
		batch := &pgx.Batch{}
		for _, p := range snap.Entries {
			if p.Platform != snap.Platform || p.ExternalID == "" || p.URL == "" || !p.Finished || p.ContestEnd.IsZero() {
				return domain.Fail(domain.Unsupported)
			}
			batch.Queue(`WITH p AS (INSERT INTO problems(id,platform,external_id,canonical_url) VALUES($1,$2,$3,$4) ON CONFLICT(platform,external_id) DO UPDATE SET canonical_url=EXCLUDED.canonical_url RETURNING id) INSERT INTO catalog_entries(snapshot_id,problem_id,platform,contest_id,difficulty,contest_end_at,contest_finished,kind,metadata) SELECT $5,p.id,$2,$6,$7,$8,$9,$10,$11 FROM p`, domain.NewID(), p.Platform, p.ExternalID, p.URL, id, p.ContestID, p.Difficulty, p.ContestEnd, p.Finished, p.Kind, p.Metadata)
		}
		br := tx.SendBatch(ctx, batch)
		if e := br.Close(); e != nil {
			return e
		}
		if _, e := tx.Exec(ctx, "UPDATE catalog_snapshots SET status='ready' WHERE id=$1", id); e != nil {
			return e
		}
		_, e := tx.Exec(ctx, "INSERT INTO catalog_heads(platform,snapshot_id) VALUES($1,$2) ON CONFLICT(platform) DO UPDATE SET snapshot_id=EXCLUDED.snapshot_id,updated_at=clock_timestamp()", snap.Platform, id)
		return e
	})
}
func (s *Store) SaveStatement(ctx context.Context, problemID string, statement domain.Statement, raw string) (string, error) {
	id := domain.NewID()
	e := s.transaction(ctx, func(tx pgx.Tx) error {
		// Share immutable assets by hash and rewrite AST references before hashing.
		for i := range statement.Assets {
			a := &statement.Assets[i]
			old := a.ID
			if len(a.Body) == 0 {
				return domain.Fail(domain.Unsupported)
			}
			e := tx.QueryRow(ctx, "INSERT INTO statement_assets(id,sha256,mime_type,byte_size,body,source_url) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(sha256) DO UPDATE SET sha256=EXCLUDED.sha256 RETURNING id", a.ID, a.SHA256, a.MIME, len(a.Body), a.Body, a.URL).Scan(&a.ID)
			if e != nil {
				return e
			}
			if old != a.ID {
				for j := range statement.Blocks {
					for k := range statement.Blocks[j].Atoms {
						if statement.Blocks[j].Atoms[k].Text == "[必要圖片 "+old+"]" {
							statement.Blocks[j].Atoms[k].Text = "[必要圖片 " + a.ID + "]"
						}
					}
				}
			}
		}
		ast, e := json.Marshal(statement)
		if e != nil {
			return e
		}
		hash := domain.Hash(ast)
		e = tx.QueryRow(ctx, "INSERT INTO statement_versions(id,problem_id,language,content_hash,parser_version,ast,raw_html) VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT(problem_id,content_hash,parser_version,language) DO NOTHING RETURNING id", id, problemID, statement.Language, hash, statement.ParserVersion, ast, raw).Scan(&id)
		if e == pgx.ErrNoRows {
			e = tx.QueryRow(ctx, "SELECT id FROM statement_versions WHERE problem_id=$1 AND content_hash=$2 AND parser_version=$3 AND language=$4", problemID, hash, statement.ParserVersion, statement.Language).Scan(&id)
		}
		if e != nil {
			return e
		}
		for _, a := range statement.Assets {
			if _, e = tx.Exec(ctx, "INSERT INTO statement_asset_links(statement_id,asset_id) VALUES($1,$2) ON CONFLICT DO NOTHING", id, a.ID); e != nil {
				return e
			}
		}
		return nil
	})
	return id, e
}
func (s *Store) Statement(ctx context.Context, id string) (domain.Statement, error) {
	var raw []byte
	var result domain.Statement
	e := s.Pool.QueryRow(ctx, "SELECT ast FROM statement_versions WHERE id=$1", id).Scan(&raw)
	if e != nil {
		return result, e
	}
	if e = json.Unmarshal(raw, &result); e != nil {
		return result, e
	}
	for i := range result.Assets {
		e = s.Pool.QueryRow(ctx, "SELECT body FROM statement_assets a JOIN statement_asset_links l ON a.id=l.asset_id WHERE l.statement_id=$1 AND a.id=$2", id, result.Assets[i].ID).Scan(&result.Assets[i].Body)
		if e != nil {
			return result, e
		}
	}
	return result, nil
}
func (s *Store) Asset(ctx context.Context, id string) ([]byte, string, error) {
	var b []byte
	var mime string
	e := s.Pool.QueryRow(ctx, "SELECT body,mime_type FROM statement_assets WHERE id=$1", id).Scan(&b, &mime)
	return b, mime, e
}
