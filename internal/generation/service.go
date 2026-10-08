package generation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/kzzz/atc-cf-bot/internal/domain"
	"github.com/kzzz/atc-cf-bot/internal/llm"
	"github.com/kzzz/atc-cf-bot/internal/render"
	"github.com/kzzz/atc-cf-bot/internal/source"
	"github.com/kzzz/atc-cf-bot/prompts"
)

type Service struct {
	Client      *llm.Client
	Coordinator *Coordinator
	BlockLimit  int
}

func (s *Service) Generate(ctx context.Context, guild, sid, kind, language string, statement domain.Statement) (json.RawMessage, string, error) {
	system := prompts.Get(kind)
	original := source.Readable(statement)
	req := llm.Request{Kind: kind, Messages: []llm.Message{{Role: "system", Content: system}, {Role: "user", Content: "程式語言：" + language + "\n完整原文：\n" + original}}}
	fp := s.Client.Fingerprint(req, domain.JSONHash(statement), statement.ParserVersion, language)
	fp["statement_id"] = sid
	if kind == "translation" {
		fp["translation_batch_bytes"] = s.BlockLimit
		fp["translation_batch_policy"] = "context-v1"
	}
	key := domain.JSONHash(fp)
	result, e := s.Coordinator.Get(ctx, guild, key, sid, kind, fp, kind == "translation", func(shared context.Context) (json.RawMessage, error) {
		if kind != "translation" {
			r, e := s.Client.Complete(shared, req)
			if e != nil {
				return nil, e
			}
			if e = validateAnswer(kind, language, r.Text); e != nil {
				return nil, e
			}
			return json.Marshal(r)
		}
		background, e := translationBackground(statement, s.BlockLimit/2)
		if e != nil {
			return nil, e
		}
		t := domain.Translation{Blocks: map[string]string{}}
		for _, block := range statement.Blocks {
			if block.SampleIndex != nil {
				t.Blocks[block.ID] = block.Text
				continue
			}
			translated, e := s.translate(shared, block, system, background, 0)
			if e != nil {
				return nil, e
			}
			if _, e = source.Restore(block, translated); e != nil {
				return nil, e
			}
			t.Blocks[block.ID] = translated
		}
		if e := t.Validate(statement); e != nil {
			return nil, e
		}
		return json.Marshal(t)
	})
	return result, key, e
}
func (s *Service) translate(ctx context.Context, b domain.Block, system, background string, depth int) (string, error) {
	// Split before a request if the configured byte bound is exceeded; atom tokens
	// stay intact. The entire translation uses the coordinator's single deadline.
	user := background + fmt.Sprintf("\nBlock %s，章節 %s：\n%s", b.ID, b.Kind, b.Text)
	if len(user)+len(system) > s.BlockLimit {
		return s.translateParts(ctx, b, system, background, depth)
	}
	req := llm.Request{Kind: "translation", Messages: []llm.Message{{Role: "system", Content: system}, {Role: "user", Content: user}}}
	r, e := s.Client.Complete(ctx, req)
	if e != nil {
		var de *domain.Error
		if errors.As(e, &de) && (de.Code == domain.Truncated || de.Code == domain.ContextTooLong) {
			return s.translateParts(ctx, b, system, background, depth)
		}
		return "", e
	}
	return r.Text, nil
}
func (s *Service) translateParts(ctx context.Context, b domain.Block, system, background string, depth int) (string, error) {
	if depth >= 2 {
		return "", domain.Fail(domain.Unsupported)
	}
	// UTF-16 bound is deliberately stricter than the input byte bound for CJK.
	budget := (s.BlockLimit - len(system) - len(background) - len(b.ID) - len(b.Kind) - 256) / 4
	if budget <= 0 {
		return "", domain.Fail(domain.Unsupported)
	}
	if depth > 0 {
		budget /= 1 << depth
	}
	if render.Units(b.Text) <= budget {
		budget = render.Units(b.Text) / 2
	}
	tokens := make([]string, 0, len(b.Atoms))
	for _, a := range b.Atoms {
		tokens = append(tokens, a.Token)
	}
	parts, e := render.SplitTokens(b.Text, budget, tokens)
	if e != nil || len(parts) < 2 {
		return "", domain.Fail(domain.Unsupported)
	}
	var out strings.Builder
	for i, p := range parts {
		if strings.Count(p, "⟪ATOM_") != strings.Count(p, "⟫") {
			return "", domain.Fail(domain.Unsupported)
		}
		sub := b
		sub.ID = fmt.Sprintf("%s.%d", b.ID, i)
		sub.Text = p
		v, e := s.translate(ctx, sub, system, background, depth+1)
		if e != nil {
			return "", e
		}
		out.WriteString(v)
	}
	return out.String(), nil
}

func translationBackground(statement domain.Statement, limit int) (string, error) {
	var out strings.Builder
	out.WriteString("題目背景（只供術語與變數一致性參考，不翻譯此區）：\n" + statement.Title + "\n")
	seen := map[string]bool{}
	for _, b := range statement.Blocks {
		if b.SampleIndex != nil {
			continue
		}
		// Carry the complete input/constraint definitions between batches. A
		// background that cannot fit is rejected rather than silently truncated.
		if b.Kind == "input" || b.Kind == "constraints" {
			text, e := source.Restore(b, b.Text)
			if e != nil {
				return "", e
			}
			out.WriteString(b.Kind + "\n" + text + "\n")
		}
		for _, atom := range b.Atoms {
			if !seen[atom.Text] {
				seen[atom.Text] = true
				out.WriteString("原文變數／數值／公式：" + atom.Text + "\n")
			}
		}
		if out.Len() > limit {
			return "", domain.Fail(domain.Unsupported)
		}
	}
	if out.Len() > limit {
		return "", domain.Fail(domain.Unsupported)
	}
	return out.String(), nil
}
