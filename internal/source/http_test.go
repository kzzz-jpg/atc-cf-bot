package source

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestSourceBoundedBodyAndNoAuthorization(t *testing.T) {
	f := NewFetcher()
	f.Client = &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Authorization") != "" {
			t.Fatal("LLM key forwarded")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(strings.Repeat("x", 101))), Header: http.Header{}}, nil
	})}
	if _, _, e := f.Get(context.Background(), "https://atcoder.jp/contests/abc001/tasks/a", 100); e == nil {
		t.Fatal("body bound ignored")
	}
}
func TestURLAndRedirectSafety(t *testing.T) {
	f := NewFetcher()
	for _, raw := range []string{"http://atcoder.jp/a", "https://user:secret@atcoder.jp/a", "https://127.0.0.1/a", "https://atcoder.jp:8080/a", "https://atcoder.jp.evil.example/a", "https://169.254.169.254/latest/meta-data"} {
		u, _ := url.Parse(raw)
		if f.ValidateURL(u) == nil {
			t.Fatal("unsafe URL accepted", raw)
		}
	}
	for _, raw := range []string{"https://localhost/a", "http://atcoder.jp/a", "https://10.0.0.1/a"} {
		u, _ := url.Parse(raw)
		if f.Client.CheckRedirect(&http.Request{URL: u}, []*http.Request{{}}) == nil {
			t.Fatal("unsafe redirect accepted")
		}
	}
}
func TestImageMIMEAndPixelBounds(t *testing.T) {
	var b bytes.Buffer
	if e := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 20, 20))); e != nil {
		t.Fatal(e)
	}
	if e := ValidateImage(b.Bytes(), "image/png", 400); e != nil {
		t.Fatal(e)
	}
	for _, tc := range []struct {
		body   []byte
		mime   string
		pixels int64
	}{{b.Bytes(), "image/jpeg", 400}, {b.Bytes(), "image/png", 399}, {[]byte("<svg/>"), "image/svg+xml", 400}, {[]byte("broken"), "image/png", 400}} {
		if ValidateImage(tc.body, tc.mime, tc.pixels) == nil {
			t.Fatal("bad image accepted")
		}
	}
}
func TestJSONCompleteDocument(t *testing.T) {
	for _, raw := range []string{`{"a":1} garbage`, `{"a":1}{"b":2}`, `<html>rate limit</html>`, `{"a":`} {
		var result map[string]any
		if DecodeJSON([]byte(raw), &result) == nil {
			t.Fatal("bad JSON accepted")
		}
	}
}
