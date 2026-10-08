package source

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/kzzz/atc-cf-bot/internal/domain"
)

func TestLiveOfficialStatements(t *testing.T) {
	if os.Getenv("LIVE_SOURCE_TEST") != "1" {
		t.Skip("set LIVE_SOURCE_TEST=1 to fetch official sites")
	}
	f := NewFetcher()
	defer f.Client.CloseIdleConnections()
	for _, tc := range []struct {
		p   domain.Platform
		url string
	}{{domain.AtCoder, "https://atcoder.jp/contests/abc100/tasks/abc100_a"}, {domain.Codeforces, "https://codeforces.com/problemset/problem/4/A?locale=en"}} {
		t.Run(string(tc.p), func(t *testing.T) {
			ctx, c := context.WithTimeout(context.Background(), 60*time.Second)
			defer c()
			raw, _, e := f.Get(ctx, tc.url, 5<<20)
			if e != nil {
				t.Fatal(e)
			}
			statement, e := Parse(tc.p, raw, tc.url, 64<<10)
			if e != nil {
				t.Fatal(e)
			}
			for _, a := range statement.Assets {
				body, mime, e := f.Get(ctx, a.URL, 2<<20)
				if e != nil {
					t.Fatal(e)
				}
				if e = ValidateImage(body, mime, 16e6); e != nil {
					t.Fatal(e)
				}
			}
			if dir := os.Getenv("LIVE_SOURCE_OUTPUT_DIR"); dir != "" {
				if e = os.MkdirAll(dir, 0755); e != nil {
					t.Fatal(e)
				}
				if e = os.WriteFile(dir+"/"+string(tc.p)+"-official.html", raw, 0644); e != nil {
					t.Fatal(e)
				}
			}
			t.Logf("parsed %s: blocks=%d samples=%d assets=%d", statement.Title, len(statement.Blocks), len(statement.Samples), len(statement.Assets))
		})
	}
}
