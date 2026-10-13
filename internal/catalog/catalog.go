package catalog

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/kzzz/atc-cf-bot/internal/domain"
	"github.com/kzzz/atc-cf-bot/internal/source"
)

type Syncer struct {
	Fetcher  *source.Fetcher
	MaxBytes int64
}

func (s Syncer) Fetch(ctx context.Context, p domain.Platform) (domain.CatalogSnapshot, error) {
	if p == domain.Codeforces {
		return s.cf(ctx)
	}
	if p == domain.AtCoder {
		return s.ac(ctx)
	}
	return domain.CatalogSnapshot{}, domain.Fail(domain.Invalid)
}
func (s Syncer) get(ctx context.Context, u string, v any) ([]byte, error) {
	b, _, e := s.Fetcher.Get(ctx, u, s.MaxBytes)
	if e != nil {
		return nil, e
	}
	return b, source.DecodeJSON(b, v)
}
func (s Syncer) cf(ctx context.Context) (domain.CatalogSnapshot, error) {
	var problems struct {
		Status string `json:"status"`
		Result struct {
			Problems []struct {
				ContestID int    `json:"contestId"`
				Index     string `json:"index"`
				Name      string `json:"name"`
				Type      string `json:"type"`
				Rating    *int   `json:"rating"`
			} `json:"problems"`
		} `json:"result"`
	}
	b, e := s.get(ctx, "https://codeforces.com/api/problemset.problems", &problems)
	if e != nil {
		return domain.CatalogSnapshot{}, e
	}
	var contests struct {
		Status string `json:"status"`
		Result []struct {
			ID       int    `json:"id"`
			Name     string `json:"name"`
			Phase    string `json:"phase"`
			Start    *int64 `json:"startTimeSeconds"`
			Duration *int64 `json:"durationSeconds"`
		} `json:"result"`
	}
	c, e := s.get(ctx, "https://codeforces.com/api/contest.list?gym=false", &contests)
	if e != nil {
		return domain.CatalogSnapshot{}, e
	}
	if problems.Status != "OK" || contests.Status != "OK" || len(problems.Result.Problems) == 0 || len(contests.Result) == 0 {
		return domain.CatalogSnapshot{}, domain.Fail(domain.Unsupported)
	}
	finished := map[int]time.Time{}
	for _, ct := range contests.Result {
		if ct.ID > 0 && ct.ID < 100000 && ct.Phase == "FINISHED" && ct.Start != nil && ct.Duration != nil && *ct.Duration > 0 && cfOwnContest(ct.Name) {
			finished[ct.ID] = time.Unix(*ct.Start+*ct.Duration, 0)
		}
	}
	snap := domain.CatalogSnapshot{Platform: domain.Codeforces, SourceHash: domain.Hash(append(b, c...))}
	seen := map[string]bool{}
	for _, p := range problems.Result.Problems {
		if p.ContestID < 1 || p.Index == "" || p.Name == "" {
			return snap, domain.Fail(domain.Unsupported)
		}
		end, ok := finished[p.ContestID]
		if !ok || p.Type != "PROGRAMMING" {
			continue
		}
		ext := strconv.Itoa(p.ContestID) + "/" + p.Index
		if seen[ext] {
			return snap, domain.Fail(domain.Unsupported)
		}
		seen[ext] = true
		metadata, _ := json.Marshal(p)
		snap.Entries = append(snap.Entries, domain.ProblemRef{Platform: domain.Codeforces, ExternalID: ext, URL: "https://codeforces.com/problemset/problem/" + ext, ContestID: strconv.Itoa(p.ContestID), Difficulty: p.Rating, ContestEnd: end, Finished: true, Kind: "programming", Metadata: metadata})
	}
	if len(snap.Entries) == 0 {
		return snap, domain.Fail(domain.Unsupported)
	}
	return snap, nil
}

var standardContest = regexp.MustCompile(`^(abc|arc|agc)[0-9]+$`)

// cfOwnContestRe matches contests run by Codeforces itself: any "Codeforces ...
// Round" name (including Educational / Global / Beta Round and sponsor-prefixed
// names such as "Nebius Round 2 (Codeforces Round 1088, Div. 1 + Div. 2)"),
// plus the New Year rounds. Online mirrors of onsite contests — ICPC, UCUP,
// Technocup, VK Cup and similar — are left out so the catalog only carries
// original CF problems.
var cfOwnContestRe = regexp.MustCompile(`Codeforces.*Round|^Hello \d{4}|^Good Bye \d{4}`)

// Marathon-style rounds maximize score over hours and make no sense as a daily
// problem, so they are dropped even though they are Codeforces-branded.
func cfOwnContest(name string) bool {
	return !strings.Contains(name, "Marathon") && cfOwnContestRe.MatchString(name)
}

func (s Syncer) ac(ctx context.Context) (domain.CatalogSnapshot, error) {
	var problems []struct {
		ID        string `json:"id"`
		ContestID string `json:"contest_id"`
		Title     string `json:"title"`
	}
	b, e := s.get(ctx, "https://kenkoooo.com/atcoder/resources/problems.json", &problems)
	if e != nil {
		return domain.CatalogSnapshot{}, e
	}
	var models map[string]struct {
		Difficulty *int `json:"difficulty"`
	}
	m, e := s.get(ctx, "https://kenkoooo.com/atcoder/resources/problem-models.json", &models)
	if e != nil {
		return domain.CatalogSnapshot{}, e
	}
	var contests []struct {
		ID       string `json:"id"`
		Start    *int64 `json:"start_epoch_second"`
		Duration *int64 `json:"duration_second"`
	}
	c, e := s.get(ctx, "https://kenkoooo.com/atcoder/resources/contests.json", &contests)
	if e != nil {
		return domain.CatalogSnapshot{}, e
	}
	if len(problems) == 0 || len(models) == 0 || len(contests) == 0 {
		return domain.CatalogSnapshot{}, domain.Fail(domain.Unsupported)
	}
	ended := map[string]time.Time{}
	for _, ct := range contests {
		if standardContest.MatchString(ct.ID) && ct.Start != nil && ct.Duration != nil && *ct.Duration > 0 {
			ended[ct.ID] = time.Unix(*ct.Start+*ct.Duration, 0)
		}
	}
	snap := domain.CatalogSnapshot{Platform: domain.AtCoder, SourceHash: domain.Hash(append(append(b, m...), c...))}
	seen := map[string]bool{}
	for _, p := range problems {
		if p.ID == "" || p.ContestID == "" || p.Title == "" {
			return snap, domain.Fail(domain.Unsupported)
		}
		end, ok := ended[p.ContestID]
		if !ok || seen[p.ID] {
			continue
		}
		seen[p.ID] = true
		metadata, _ := json.Marshal(p)
		snap.Entries = append(snap.Entries, domain.ProblemRef{Platform: domain.AtCoder, ExternalID: p.ID, URL: fmt.Sprintf("https://atcoder.jp/contests/%s/tasks/%s", p.ContestID, p.ID), ContestID: p.ContestID, Difficulty: models[p.ID].Difficulty, ContestEnd: end, Finished: true, Kind: "programming", Metadata: metadata})
	}
	if len(snap.Entries) == 0 {
		return snap, domain.Fail(domain.Unsupported)
	}
	return snap, nil
}
