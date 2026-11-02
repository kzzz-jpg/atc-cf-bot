package catalog

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/kzzz/atc-cf-bot/internal/domain"
	"github.com/kzzz/atc-cf-bot/internal/source"
)

type transport func(*http.Request) (*http.Response, error)

func (f transport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestAtCoderDifficultyAndEndedContests(t *testing.T) {
	f := source.NewFetcher()
	f.Client = &http.Client{Transport: transport(func(r *http.Request) (*http.Response, error) {
		var body string
		switch {
		case strings.Contains(r.URL.Path, "problem-models"):
			body = `{"abc001_1":{"difficulty":-25},"abc001_2":{},"abc001_3":{"difficulty":0}}`
		case strings.Contains(r.URL.Path, "contests"):
			body = `[{"id":"abc001","start_epoch_second":1,"duration_second":100}]`
		default:
			body = `[{"id":"abc001_1","contest_id":"abc001","title":"A"},{"id":"abc001_2","contest_id":"abc001","title":"B"},{"id":"abc001_3","contest_id":"abc001","title":"C"},{"id":"abc001_1","contest_id":"abc001","title":"A"}]`
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	s := Syncer{Fetcher: f, MaxBytes: 1 << 20}
	snapshot, e := s.Fetch(context.Background(), domain.AtCoder)
	if e != nil {
		t.Fatal(e)
	}
	if len(snapshot.Entries) != 3 || snapshot.Entries[0].Difficulty == nil || *snapshot.Entries[0].Difficulty != -25 || snapshot.Entries[1].Difficulty != nil || snapshot.Entries[2].Difficulty == nil || *snapshot.Entries[2].Difficulty != 0 {
		t.Fatalf("difficulty conflated: %#v", snapshot.Entries)
	}
}
func TestCFOnlyFinishedKnownContests(t *testing.T) {
	f := source.NewFetcher()
	f.Client = &http.Client{Transport: transport(func(r *http.Request) (*http.Response, error) {
		body := `{"status":"OK","result":{"problems":[{"contestId":1,"index":"A","name":"A","type":"PROGRAMMING","rating":800},{"contestId":2,"index":"A","name":"B","type":"PROGRAMMING","rating":800}]}}`
		if strings.Contains(r.URL.Path, "contest.list") {
			body = `{"status":"OK","result":[{"id":1,"name":"Codeforces Round 1 (Div. 2)","phase":"FINISHED","startTimeSeconds":1,"durationSeconds":100},{"id":2,"name":"Codeforces Round 2 (Div. 2)","phase":"CODING","startTimeSeconds":1,"durationSeconds":100}]}`
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	snapshot, e := (Syncer{Fetcher: f, MaxBytes: 1 << 20}).Fetch(context.Background(), domain.Codeforces)
	if e != nil || len(snapshot.Entries) != 1 || snapshot.Entries[0].ExternalID != "1/A" {
		t.Fatalf("unfinished problem selected %#v %v", snapshot, e)
	}
}
func TestCFExcludesMirrorContests(t *testing.T) {
	f := source.NewFetcher()
	f.Client = &http.Client{Transport: transport(func(r *http.Request) (*http.Response, error) {
		body := `{"status":"OK","result":{"problems":[{"contestId":1,"index":"A","name":"A","type":"PROGRAMMING","rating":800},{"contestId":2,"index":"B","name":"B","type":"PROGRAMMING","rating":800},{"contestId":3,"index":"C","name":"C","type":"PROGRAMMING","rating":800},{"contestId":4,"index":"D","name":"D","type":"PROGRAMMING","rating":800},{"contestId":5,"index":"E","name":"E","type":"PROGRAMMING","rating":800},{"contestId":6,"index":"F","name":"F","type":"PROGRAMMING","rating":800}]}}`
		if strings.Contains(r.URL.Path, "contest.list") {
			body = `{"status":"OK","result":[` +
				`{"id":1,"name":"Codeforces Round 1 (Div. 2)","phase":"FINISHED","startTimeSeconds":1,"durationSeconds":100},` +
				`{"id":2,"name":"Educational Codeforces Round 2 (Rated for Div. 2)","phase":"FINISHED","startTimeSeconds":1,"durationSeconds":100},` +
				`{"id":3,"name":"2024-2025 ICPC, NERC, Northern Eurasia Finals (Unrated, Online Mirror, ICPC Rules, Teams Preferred)","phase":"FINISHED","startTimeSeconds":1,"durationSeconds":100},` +
				`{"id":4,"name":"UCUP 2024, Stage 5: Warsaw (Unrated, Online Mirror, ICPC Rules, Teams Preferred)","phase":"FINISHED","startTimeSeconds":1,"durationSeconds":100},` +
				`{"id":5,"name":"Nebius Round 2 (Codeforces Round 1088, Div. 1 + Div. 2)","phase":"FINISHED","startTimeSeconds":1,"durationSeconds":100},` +
				`{"id":6,"name":"Codeforces Marathon Round 1","phase":"FINISHED","startTimeSeconds":1,"durationSeconds":100}]}`
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	snapshot, e := (Syncer{Fetcher: f, MaxBytes: 1 << 20}).Fetch(context.Background(), domain.Codeforces)
	if e != nil {
		t.Fatal(e)
	}
	var ids []string
	for _, entry := range snapshot.Entries {
		ids = append(ids, entry.ExternalID)
	}
	if strings.Join(ids, ",") != "1/A,2/B,5/E" {
		t.Fatalf("mirror contests leaked into catalog: %v", ids)
	}
}
