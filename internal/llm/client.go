// Package llm implements non-streaming Chat Completions without SDK retries.
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kzzz/atc-cf-bot/internal/config"
	"github.com/kzzz/atc-cf-bot/internal/domain"
)

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}
type Request struct {
	Kind     string
	Messages []Message
}
type Result struct {
	Text  string          `json:"text"`
	Usage json.RawMessage `json:"usage,omitempty"`
}
type Client struct {
	Config      config.LLM
	HTTP        *http.Client
	MaxResponse int64
	mu          sync.Mutex
	disabled    map[string]bool
}

func New(c config.LLM, maxResponse int64) *Client {
	return &Client{Config: c, HTTP: &http.Client{Timeout: time.Duration(c.RequestTimeout) * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, MaxResponse: maxResponse, disabled: map[string]bool{}}
}
func (c *Client) Fingerprint(req Request, statementHash, parser, language string) map[string]any {
	// nil temperature is represented explicitly; no credential enters this map.
	return map[string]any{"schema": "generation-v1", "statement_hash": statementHash, "parser": parser, "ast_schema": "1", "kind": req.Kind, "base_url": strings.TrimRight(c.Config.BaseURL, "/"), "model": c.Config.ModelFor(req.Kind), "messages": req.Messages, "prompt_version": "1", "prompt_hash": domain.JSONHash(req.Messages), "output_language": "zh-TW", "code_language": language, "token_parameter": c.Config.TokenLimitParameter, "token_limit": c.Config.OutputTokenLimit, "temperature": c.Config.Temperature, "allow_missing_finish": c.Config.AllowMissingFinish, "stream": false}
}
func (c *Client) Complete(ctx context.Context, req Request) (Result, error) {
	model := c.Config.ModelFor(req.Kind)
	c.mu.Lock()
	disabled := c.disabled["*"] || c.disabled[model]
	c.mu.Unlock()
	if disabled {
		return Result{}, domain.Fail(domain.Configuration)
	}
	payload := map[string]any{"model": model, "messages": req.Messages, "stream": false, c.Config.TokenLimitParameter: c.Config.OutputTokenLimit}
	if c.Config.Temperature != nil {
		payload["temperature"] = *c.Config.Temperature
	}
	raw, e := json.Marshal(payload)
	if e != nil {
		return Result{}, domain.Fail(domain.Invalid)
	}
	for attempt := 0; attempt <= c.Config.MaxRetries; attempt++ {
		if ctx.Err() != nil {
			return Result{}, domain.Fail(domain.Transient)
		}
		result, err := c.attempt(ctx, raw)
		if err == nil {
			return result, nil
		}
		var de *domain.Error
		if !errors.As(err, &de) {
			return Result{}, domain.Fail(domain.Transient)
		}
		if de.Code == domain.Configuration {
			c.mu.Lock()
			key := model
			if de.Status == 401 || de.Status == 403 {
				key = "*"
			}
			c.disabled[key] = true
			c.mu.Unlock()
			return Result{}, de
		}
		if (de.Code != domain.Transient && de.Code != domain.RateLimited) || attempt == c.Config.MaxRetries {
			return Result{}, de
		}
		delay := time.Duration(250*(1<<attempt))*time.Millisecond + time.Duration(rand.IntN(250))*time.Millisecond
		if de.RetryAfter > delay {
			delay = de.RetryAfter
		}
		if err = Wait(ctx, delay); err != nil {
			return Result{}, domain.Fail(domain.Transient)
		}
	}
	return Result{}, domain.Fail(domain.Transient)
}
func (c *Client) attempt(ctx context.Context, body []byte) (Result, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Duration(c.Config.RequestTimeout)*time.Second)
	defer cancel()
	req, e := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(c.Config.BaseURL, "/")+"/chat/completions", bytes.NewReader(body))
	if e != nil {
		return Result{}, domain.Fail(domain.Configuration)
	}
	req.Header.Set("Authorization", "Bearer "+c.Config.APIKey)
	req.Header.Set("Content-Type", "application/json")
	res, e := c.HTTP.Do(req)
	if e != nil {
		return Result{}, domain.Fail(domain.Transient)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		de := &domain.Error{Status: res.StatusCode, Code: domain.Unsupported}
		switch {
		case res.StatusCode == 400 || res.StatusCode == 401 || res.StatusCode == 403:
			de.Code = domain.Configuration
			if res.StatusCode == 400 && contextLengthExceeded(res.Body, c.MaxResponse) {
				de.Code = domain.ContextTooLong
			}
		case res.StatusCode == 429:
			de.Code = domain.RateLimited
			de.RetryAfter = RetryAfter(res.Header.Get("Retry-After"), time.Now())
		case res.StatusCode >= 500:
			de.Code = domain.Transient
		}
		return Result{}, de
	}
	b, e := io.ReadAll(io.LimitReader(res.Body, c.MaxResponse+1))
	if e != nil || int64(len(b)) > c.MaxResponse {
		return Result{}, domain.Fail(domain.Unsupported)
	}
	var r struct {
		Choices []struct {
			Message struct {
				Content   *string         `json:"content"`
				Refusal   json.RawMessage `json:"refusal"`
				ToolCalls json.RawMessage `json:"tool_calls"`
			} `json:"message"`
			Finish *string `json:"finish_reason"`
		} `json:"choices"`
		Usage json.RawMessage `json:"usage"`
	}
	if e = json.Unmarshal(b, &r); e != nil || len(r.Choices) == 0 {
		return Result{}, domain.Fail(domain.Unsupported)
	}
	choice := r.Choices[0]
	if choice.Message.Content == nil || strings.TrimSpace(*choice.Message.Content) == "" || nonEmpty(choice.Message.Refusal) || nonEmpty(choice.Message.ToolCalls) {
		return Result{}, domain.Fail(domain.Unsupported)
	}
	if choice.Finish == nil {
		if !c.Config.AllowMissingFinish {
			return Result{}, domain.Fail(domain.Unsupported)
		}
	} else if *choice.Finish == "length" {
		return Result{}, domain.Fail(domain.Truncated)
	} else if *choice.Finish != "stop" {
		return Result{}, domain.Fail(domain.Unsupported)
	}
	return Result{Text: *choice.Message.Content, Usage: r.Usage}, nil
}

// Only explicit structured provider evidence permits shrinking translation
// blocks. Arbitrary prose, HTML, or other 400s remain configuration failures.
// The response body is bounded, discarded, and never attached to an error.
func contextLengthExceeded(body io.Reader, maxBytes int64) bool {
	limit := min(maxBytes, int64(16<<10))
	if limit <= 0 {
		return false
	}
	raw, e := io.ReadAll(io.LimitReader(body, limit+1))
	if e != nil || int64(len(raw)) > limit {
		return false
	}
	var response struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	return json.Unmarshal(raw, &response) == nil && response.Error.Code == "context_length_exceeded"
}
func nonEmpty(b json.RawMessage) bool {
	s := strings.TrimSpace(string(b))
	return s != "" && s != "null" && s != "[]" && s != `""`
}
func RetryAfter(v string, now time.Time) time.Duration {
	if n, e := strconv.ParseFloat(v, 64); e == nil && n > 0 && n < 86400 {
		return time.Duration(n * float64(time.Second))
	}
	if t, e := http.ParseTime(v); e == nil && t.After(now) {
		return t.Sub(now)
	}
	return 0
}
func Wait(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// CheckConnectivity performs a read-only model-list request; it never generates
// text or substitutes a different configured model.
func (c *Client) CheckConnectivity(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, time.Duration(c.Config.RequestTimeout)*time.Second)
	defer cancel()
	req, e := http.NewRequestWithContext(ctx, "GET", strings.TrimRight(c.Config.BaseURL, "/")+"/models", nil)
	if e != nil {
		return domain.Fail(domain.Configuration)
	}
	req.Header.Set("Authorization", "Bearer "+c.Config.APIKey)
	res, e := c.HTTP.Do(req)
	if e != nil {
		return domain.Fail(domain.Transient)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return &domain.Error{Code: domain.Configuration, Status: res.StatusCode}
	}
	body, e := io.ReadAll(io.LimitReader(res.Body, c.MaxResponse+1))
	if e != nil || int64(len(body)) > c.MaxResponse {
		return domain.Fail(domain.Unsupported)
	}
	var models struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if json.Unmarshal(body, &models) != nil || len(models.Data) == 0 {
		return domain.Fail(domain.Unsupported)
	}
	available := map[string]bool{}
	for _, m := range models.Data {
		available[m.ID] = true
	}
	for _, kind := range []string{"translation", "hint", "solution"} {
		if !available[c.Config.ModelFor(kind)] {
			return domain.Fail(domain.Configuration)
		}
	}
	return nil
}
func (c *Client) Degraded() bool { c.mu.Lock(); defer c.mu.Unlock(); return len(c.disabled) > 0 }
