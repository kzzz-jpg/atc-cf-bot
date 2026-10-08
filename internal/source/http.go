package source

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/kzzz/atc-cf-bot/internal/domain"
	"github.com/kzzz/atc-cf-bot/internal/llm"
)

// Fetcher validates redirects AND resolved dial addresses, avoiding DNS rebinding.
type Fetcher struct {
	Client *http.Client
	Hosts  map[string]bool
	mu     sync.Mutex
	next   map[string]time.Time
}

func NewFetcher() *Fetcher {
	f := &Fetcher{Hosts: map[string]bool{"atcoder.jp": true, "codeforces.com": true, "kenkoooo.com": true, "img.atcoder.jp": true, "assets.codeforces.com": true}, next: map[string]time.Time{}}
	tr := &http.Transport{Proxy: nil, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}, ResponseHeaderTimeout: 30 * time.Second}
	tr.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, e := net.SplitHostPort(address)
		if e != nil || port != "443" || !f.Hosts[host] {
			return nil, domain.Fail(domain.Unsupported)
		}
		ips, e := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		if e != nil || len(ips) == 0 {
			return nil, domain.Fail(domain.Transient)
		}
		for _, ip := range ips {
			if !PublicIP(ip) {
				return nil, domain.Fail(domain.Unsupported)
			}
		}
		var last error
		for _, ip := range ips {
			c, e := (&net.Dialer{Timeout: 15 * time.Second}).DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
			if e == nil {
				return c, nil
			}
			last = e
		}
		return nil, last
	}
	f.Client = &http.Client{Transport: tr, Timeout: 60 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) > 3 {
			return domain.Fail(domain.Unsupported)
		}
		return f.ValidateURL(req.URL)
	}}
	return f
}
func PublicIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	return ip.IsValid() && ip.IsGlobalUnicast() && !ip.IsPrivate() && !ip.IsLoopback() && !ip.IsLinkLocalUnicast() && !ip.IsUnspecified() && !netip.MustParsePrefix("100.64.0.0/10").Contains(ip) && !netip.MustParsePrefix("192.0.0.0/24").Contains(ip) && !netip.MustParsePrefix("198.18.0.0/15").Contains(ip)
}
func (f *Fetcher) ValidateURL(u *url.URL) error {
	if u.Scheme != "https" || u.User != nil || !f.Hosts[u.Hostname()] || (u.Port() != "" && u.Port() != "443") {
		return domain.Fail(domain.Unsupported)
	}
	return nil
}
func (f *Fetcher) Get(ctx context.Context, raw string, limit int64) ([]byte, string, error) {
	u, e := url.Parse(raw)
	if e != nil {
		return nil, "", domain.Fail(domain.Unsupported)
	}
	if e = f.ValidateURL(u); e != nil {
		return nil, "", e
	}
	interval := 1100 * time.Millisecond
	if u.Hostname() == "codeforces.com" {
		interval = 2100 * time.Millisecond
	}
	f.mu.Lock()
	when := f.next[u.Hostname()]
	now := time.Now()
	if when.Before(now) {
		when = now
	}
	f.next[u.Hostname()] = when.Add(interval)
	f.mu.Unlock()
	if e = llm.Wait(ctx, time.Until(when)); e != nil {
		return nil, "", domain.Fail(domain.Transient)
	}
	req, e := http.NewRequestWithContext(ctx, "GET", raw, nil)
	if e != nil {
		return nil, "", domain.Fail(domain.Unsupported)
	}
	req.Header.Set("Accept-Language", "en")
	req.Header.Set("User-Agent", "atc-cf-bot/1.0 (public programming statement reader)")
	res, e := f.Client.Do(req)
	if e != nil {
		return nil, "", domain.Fail(domain.Transient)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		de := &domain.Error{Code: domain.Transient, Status: res.StatusCode}
		if res.StatusCode == 429 {
			de.Code = domain.RateLimited
			de.RetryAfter = llm.RetryAfter(res.Header.Get("Retry-After"), time.Now())
			f.mu.Lock()
			until := time.Now().Add(de.RetryAfter)
			if until.After(f.next[u.Hostname()]) {
				f.next[u.Hostname()] = until
			}
			f.mu.Unlock()
		}
		return nil, "", de
	}
	body, e := io.ReadAll(io.LimitReader(res.Body, limit+1))
	if e != nil {
		return nil, "", domain.Fail(domain.Transient)
	}
	if int64(len(body)) > limit {
		return nil, "", domain.Fail(domain.Unsupported)
	}
	return body, res.Header.Get("Content-Type"), nil
}
func DecodeJSON(b []byte, v any) error {
	d := json.NewDecoder(bytes.NewReader(b))
	if d.Decode(v) != nil {
		return domain.Fail(domain.Unsupported)
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return domain.Fail(domain.Unsupported)
	}
	return nil
}
func ValidateImage(b []byte, mime string, maxPixels int64) error {
	mime = strings.TrimSpace(strings.Split(mime, ";")[0])
	if mime != "image/png" && mime != "image/jpeg" && mime != "image/gif" {
		return domain.Fail(domain.Unsupported)
	}
	cfg, format, e := image.DecodeConfig(bytes.NewReader(b))
	if e != nil || int64(cfg.Width)*int64(cfg.Height) > maxPixels || cfg.Width <= 0 || cfg.Height <= 0 {
		return domain.Fail(domain.Unsupported)
	}
	want := map[string]string{"png": "image/png", "jpeg": "image/jpeg", "gif": "image/gif"}[format]
	if want != mime {
		return domain.Fail(domain.Unsupported)
	}
	if _, _, e = image.Decode(bytes.NewReader(b)); e != nil {
		return domain.Fail(domain.Unsupported)
	}
	return nil
}
