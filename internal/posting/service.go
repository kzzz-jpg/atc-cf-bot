package posting

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/kzzz/atc-cf-bot/internal/config"
	"github.com/kzzz/atc-cf-bot/internal/domain"
	"github.com/kzzz/atc-cf-bot/internal/generation"
	"github.com/kzzz/atc-cf-bot/internal/llm"
	"github.com/kzzz/atc-cf-bot/internal/render"
	"github.com/kzzz/atc-cf-bot/internal/source"
	"github.com/kzzz/atc-cf-bot/internal/store"
)

type Publisher interface {
	Execute(context.Context, store.Operation) (store.DiscordResult, error)
	Reconcile(context.Context, string, store.Operation) (store.DiscordResult, error)
	CheckForum(context.Context, string, domain.Settings) error
	Authorize(context.Context, string, string, string, bool, []string) error
	EnsureThread(context.Context, string, string) error
}
type Service struct {
	Store      *store.Store
	Publisher  Publisher
	Source     *source.Fetcher
	Generation *generation.Service
	Limits     config.Limits
	RNG        domain.RNG
}

func (s *Service) Run(ctx context.Context, j store.JobLease) error {
	d, e := s.Store.Daily(ctx, j.GuildID, j.DailyID)
	if e != nil {
		return e
	}
	if !time.Now().Before(d.EndsAt) {
		return domain.Fail(domain.Conflict)
	}
	if e = s.validate(ctx, j, d); e != nil {
		return e
	}
	if d.ThreadID == "" {
		p := render.NewPayload(fmt.Sprintf("每日題目｜%s\n每日原定 %d 題；正在準備。\n所有題目為 AI 翻譯，請核對原站。", d.Date, d.Target))
		p.ChannelID = d.Config.ForumChannelID
		p.Title = "每日題目｜" + d.Date
		p.Tags = d.Config.ForumTagIDs
		if e = s.Store.PrepareOperations(ctx, j, "", "forum", []render.Payload{p}, ""); e != nil {
			return e
		}
		ops, e := s.Store.Operations(ctx, j.GuildID, j.DailyID, "", "forum")
		if e != nil {
			return e
		}
		for _, o := range ops {
			if e = s.execute(ctx, j, d, o); e != nil {
				return e
			}
		}
		d, e = s.Store.Daily(ctx, j.GuildID, j.DailyID)
		if e != nil {
			return e
		}
	}
	if e = s.Publisher.EnsureThread(ctx, j.GuildID, d.ThreadID); e != nil {
		return e
	}
	pubs, e := s.Store.Publications(ctx, j)
	if e != nil {
		return e
	}
	for _, p := range pubs {
		if p.Status == "completed" {
			continue
		}
		ops, e := s.Store.Operations(ctx, j.GuildID, j.DailyID, p.ID, "message")
		if e != nil {
			return e
		}
		if len(ops) == 0 {
			statement, key, translation, e := s.prepare(ctx, j, d, &p)
			if e != nil {
				return e
			}
			label := fmt.Sprintf("題目 %d", p.Slot)
			if p.Kind == "manual" {
				label = fmt.Sprintf("加題 #%d", p.Slot)
			}
			payloads, e := render.Problem(statement, translation, p.Ref, p.ID, d.Date, label, s.Limits.MessagesPerProblem)
			if e != nil {
				return e
			}
			for i := range payloads {
				payloads[i].ChannelID = d.ThreadID
			}
			if e = s.Store.ReadyPublication(ctx, j, p.ID, p.StatementID, key); e != nil {
				return e
			}
			if e = s.Store.PrepareOperations(ctx, j, p.ID, "message", payloads, ""); e != nil {
				return e
			}
			ops, e = s.Store.Operations(ctx, j.GuildID, j.DailyID, p.ID, "message")
			if e != nil {
				return e
			}
		}
		for _, o := range ops {
			if e = s.execute(ctx, j, d, o); e != nil {
				return e
			}
		}
		if e = s.Store.CompletePublication(ctx, j, p.ID); e != nil {
			return e
		}
		if e = s.Store.RefreshDaily(ctx, j.GuildID, j.DailyID); e != nil {
			return e
		}
		if e = s.updateOverview(ctx, j, d); e != nil {
			return e
		}
	}
	if e = s.Store.RefreshDaily(ctx, j.GuildID, j.DailyID); e != nil {
		return e
	}
	// A resumed job can have completed all problems before its last overview
	// failed. Finish that durable operation even when no problem needs sending.
	return s.updateOverview(ctx, j, d)
}
func (s *Service) updateOverview(ctx context.Context, j store.JobLease, d store.Daily) error {
	text, e := s.Store.OverviewText(ctx, j.GuildID, j.DailyID)
	if e != nil {
		return e
	}
	payload := render.NewPayload(text)
	payload.ChannelID = d.ThreadID
	overview, e := s.Store.PrepareOverview(ctx, j, payload, d.StarterID)
	if e != nil {
		return e
	}
	return s.execute(ctx, j, d, overview)
}
func (s *Service) validate(ctx context.Context, j store.JobLease, d store.Daily) error {
	if e := s.Store.CheckLease(ctx, j); e != nil {
		return e
	}
	if e := s.Publisher.CheckForum(ctx, j.GuildID, d.Config); e != nil {
		return e
	}
	if j.UserID != "" {
		latest, e := s.Store.Config(ctx, j.GuildID)
		if e != nil {
			return e
		}
		if e = s.Publisher.Authorize(ctx, j.GuildID, j.UserID, d.Config.ForumChannelID, j.Kind == "daily", latest.Settings.ManualRoleIDs); e != nil {
			return domain.Fail(domain.ForbiddenActor)
		}
		return nil
	}
	return nil
}
func (s *Service) prepare(ctx context.Context, j store.JobLease, d store.Daily, p *store.Publication) (domain.Statement, string, domain.Translation, error) {
	var statement domain.Statement
	var translation domain.Translation
	for p.Attempts < 5 || p.Ref.ID != "" {
		if p.Ref.ID == "" {
			candidates, e := s.Store.Candidates(ctx, j, *p, d.Config.DedupDays)
			if e != nil {
				return statement, "", translation, e
			}
			if len(candidates) == 0 {
				return statement, "", translation, domain.Fail(domain.NoCandidate)
			}
			conflicts := 0
			for {
				if conflicts >= 20 {
					return statement, "", translation, domain.Fail(domain.Conflict)
				}
				r := candidates[s.RNG.Intn(len(candidates))]
				e = s.Store.Reserve(ctx, j, *p, r, d.Config.DedupDays)
				if e == nil {
					p.Ref = r
					p.Attempts++
					break
				}
				var de *domain.Error
				if !errors.As(e, &de) || de.Code != domain.Conflict {
					return statement, "", translation, e
				}
				conflicts++
				candidates, e = s.Store.Candidates(ctx, j, *p, d.Config.DedupDays)
				if e != nil {
					return statement, "", translation, e
				}
				if len(candidates) == 0 {
					return statement, "", translation, domain.Fail(domain.NoCandidate)
				}
			}
		}
		var raw []byte
		var e error
		if p.StatementID != "" {
			statement, e = s.Store.Statement(ctx, p.StatementID)
		} else {
			fetchURL := p.Ref.URL
			if p.Source == domain.Codeforces {
				fetchURL += "?locale=en"
			}
			raw, _, e = s.Source.Get(ctx, fetchURL, s.Limits.HTMLBytes)
			if e == nil {
				statement, e = source.Parse(p.Source, raw, p.Ref.URL, s.Limits.StatementBytes)
			}
			if e == nil {
				e = s.assets(ctx, &statement)
			}
			if e == nil {
				p.StatementID, e = s.Store.SaveStatement(ctx, p.Ref.ID, statement, string(raw))
				if e == nil {
					statement, e = s.Store.Statement(ctx, p.StatementID)
				}
			}
		}
		if e != nil {
			var de *domain.Error
			if !errors.As(e, &de) || de.Code != domain.Unsupported {
				return statement, "", translation, e
			}
			if e = s.Store.RejectCandidate(ctx, j, *p, true); e != nil {
				return statement, "", translation, e
			}
			p.Ref = domain.ProblemRef{}
			p.StatementID = ""
			continue
		}
		result, key, e := s.Generation.Generate(ctx, j.GuildID, p.StatementID, "translation", d.Config.SolutionLanguage, statement)
		if e != nil {
			var de *domain.Error
			if !errors.As(e, &de) || de.Code != domain.Unsupported {
				return statement, "", translation, e
			}
			if e = s.Store.RejectCandidate(ctx, j, *p, false); e != nil {
				return statement, "", translation, e
			}
			p.Ref = domain.ProblemRef{}
			p.StatementID = ""
			continue
		}
		if e = json.Unmarshal(result, &translation); e != nil {
			return statement, "", translation, domain.Fail(domain.Unsupported)
		}
		// Validate the entire render before committing this candidate's public intent.
		if _, e = render.Problem(statement, translation, p.Ref, p.ID, d.Date, "題目", s.Limits.MessagesPerProblem); e != nil {
			if e = s.Store.RejectCandidate(ctx, j, *p, true); e != nil {
				return statement, "", translation, e
			}
			p.Ref = domain.ProblemRef{}
			p.StatementID = ""
			continue
		}
		return statement, key, translation, nil
	}
	return statement, "", translation, domain.Fail(domain.NoCandidate)
}
func (s *Service) assets(ctx context.Context, statement *domain.Statement) error {
	var total int64
	for i := range statement.Assets {
		a := &statement.Assets[i]
		body, mime, e := s.Source.Get(ctx, a.URL, s.Limits.ImageBytes)
		if e != nil {
			return e
		}
		total += int64(len(body))
		if total > s.Limits.ImagesTotalBytes {
			return domain.Fail(domain.Unsupported)
		}
		if e = source.ValidateImage(body, mime, s.Limits.ImagePixels); e != nil {
			return e
		}
		a.Body = body
		a.SHA256 = domain.Hash(body)
		a.MIME = strings.Split(mime, ";")[0]
	}
	return nil
}
func (s *Service) execute(ctx context.Context, j store.JobLease, d store.Daily, o store.Operation) error {
	if o.Status == "confirmed" {
		return nil
	}
	if o.Status == "sending" || o.Status == "ambiguous" || o.Status == "needs_reconciliation" {
		r, e := s.Publisher.Reconcile(ctx, j.GuildID, o)
		if e != nil {
			return domain.Fail(domain.Ambiguous)
		}
		return s.Store.RecordResult(ctx, j, o, r)
	}
	for {
		if e := s.validate(ctx, j, d); e != nil {
			return e
		}
		if d.ThreadID != "" {
			if e := s.Publisher.EnsureThread(ctx, j.GuildID, d.ThreadID); e != nil {
				return e
			}
		}
		if e := s.Store.MarkSending(ctx, j, &o); e != nil {
			return e
		}
		r, e := s.Publisher.Execute(ctx, o)
		if e == nil {
			if e = s.Store.RecordResult(ctx, j, o, r); e == nil {
				return nil
			}
			clean, c := context.WithTimeout(context.Background(), 5*time.Second)
			_ = s.Store.MarkAmbiguous(clean, j.GuildID, o)
			c()
			return domain.Fail(domain.Ambiguous)
		}
		var de *domain.Error
		if errors.As(e, &de) && de.Code != domain.Ambiguous {
			if mark := s.Store.MarkRejected(ctx, j, o); mark != nil {
				return mark
			}
			if de.Code == domain.RateLimited {
				if e = llm.Wait(ctx, de.RetryAfter); e != nil {
					return domain.Fail(domain.Transient)
				}
				o.Status = "failed"
				continue
			}
			return e
		}
		clean, c := context.WithTimeout(context.Background(), 5*time.Second)
		_ = s.Store.MarkAmbiguous(clean, j.GuildID, o)
		c()
		o.Status = "ambiguous"
		r, e = s.Publisher.Reconcile(ctx, j.GuildID, o)
		if e == nil {
			return s.Store.RecordResult(ctx, j, o, r)
		}
		return domain.Fail(domain.Ambiguous)
	}
}
