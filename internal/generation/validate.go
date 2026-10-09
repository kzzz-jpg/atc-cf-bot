package generation

import (
	"strings"

	"github.com/kzzz/atc-cf-bot/internal/domain"
	"github.com/kzzz/atc-cf-bot/internal/render"
)

func validateAnswer(kind, _ string, text string) error {
	if strings.TrimSpace(text) == "" {
		return domain.Fail(domain.Unsupported)
	}
	if kind == "hint" {
		if strings.Contains(text, "```") {
			return domain.Fail(domain.Unsupported)
		}
		return nil
	}
	if kind != "solution" {
		return domain.Fail(domain.Invalid)
	}
	if strings.Contains(text, "```") || strings.Contains(text, "~~~") || strings.Contains(text, "## 參考程式碼") {
		return domain.Fail(domain.Unsupported)
	}
	headings := []string{"## 題意與關鍵觀察", "## 演算法", "## 正確性", "## 複雜度", "## 邊界與範例"}
	previous := -1
	for i, h := range headings {
		at := strings.Index(text, h)
		if at <= previous {
			return domain.Fail(domain.Unsupported)
		}
		start := at + len(h)
		end := len(text)
		if i+1 < len(headings) {
			if n := strings.Index(text[start:], headings[i+1]); n >= 0 {
				end = start + n
			}
		}
		if strings.TrimSpace(text[start:end]) == "" {
			return domain.Fail(domain.Unsupported)
		}
		previous = at
	}
	if !strings.Contains(text, "時間") || !strings.Contains(text, "空間") {
		return domain.Fail(domain.Unsupported)
	}
	if _, e := render.SplitMarkdown(text, 1800, nil); e != nil {
		return e
	}
	return nil
}
