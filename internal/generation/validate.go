package generation

import (
	"fmt"
	"strings"

	"github.com/kzzz/atc-cf-bot/internal/domain"
	"github.com/kzzz/atc-cf-bot/internal/render"
)

var forbiddenCodeTags = map[string]bool{
	"cpp": true, "c++": true, "c": true, "python": true, "py": true, "python3": true,
	"java": true, "go": true, "rust": true, "rs": true, "pascal": true, "pas": true,
	"javascript": true, "js": true, "typescript": true, "ts": true, "csharp": true,
	"cs": true, "kotlin": true, "kt": true, "swift": true, "ruby": true, "rb": true,
}

func validateAnswer(kind, _ string, text string) error {
	if strings.TrimSpace(text) == "" {
		return invalidAnswer("empty answer")
	}
	prose, e := answerProse(text)
	if e != nil {
		return e
	}
	if _, e := render.SplitMarkdown(text, 1800, nil); e != nil {
		return e
	}
	if kind == "hint" {
		return nil
	}
	if kind != "solution" {
		return domain.Fail(domain.Invalid)
	}
	if strings.Contains(prose, "## 參考程式碼") {
		return invalidAnswer("reference code section")
	}
	headings := []string{"## 題意與關鍵觀察", "## 演算法", "## 正確性", "## 複雜度", "## 邊界與範例"}
	previous := -1
	for i, h := range headings {
		at := strings.Index(prose, h)
		if at <= previous {
			return invalidAnswer("missing or unordered section: " + h)
		}
		start := at + len(h)
		end := len(prose)
		if i+1 < len(headings) {
			if n := strings.Index(prose[start:], headings[i+1]); n >= 0 {
				end = start + n
			}
		}
		if strings.TrimSpace(prose[start:end]) == "" {
			return invalidAnswer("empty section: " + h)
		}
		previous = at
	}
	if !strings.Contains(text, "時間") || !strings.Contains(text, "空間") {
		return invalidAnswer("missing time or space complexity")
	}
	return nil
}

func invalidAnswer(reason string) error {
	return fmt.Errorf("%w: %s", domain.Fail(domain.Unsupported), reason)
}

// answerProse verifies code fences and masks their contents so code or formulas
// cannot masquerade as required section headings.
// Programming language code blocks (e.g. cpp, python) are rejected.
func answerProse(text string) (string, error) {
	var prose strings.Builder
	inFence := false
	fenceMarker := ""
	for _, line := range strings.SplitAfter(text, "\n") {
		raw := line
		trimmed := strings.TrimSpace(strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r"))
		if !inFence {
			if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
				marker := trimmed[:3]
				tag := strings.ToLower(strings.TrimSpace(trimmed[3:]))
				if forbiddenCodeTags[tag] {
					return "", invalidAnswer("programming code blocks are not permitted: " + tag)
				}
				inFence = true
				fenceMarker = marker
				prose.WriteString(strings.Repeat(" ", len(raw)))
				continue
			}
			prose.WriteString(raw)
		} else {
			if strings.HasPrefix(trimmed, fenceMarker) && strings.Trim(trimmed, fenceMarker[:1]) == "" {
				inFence = false
				fenceMarker = ""
				prose.WriteString(strings.Repeat(" ", len(raw)))
				continue
			}
			prose.WriteString(strings.Repeat(" ", len(raw)))
		}
	}
	if inFence {
		return "", invalidAnswer("unclosed code block")
	}
	return prose.String(), nil
}
