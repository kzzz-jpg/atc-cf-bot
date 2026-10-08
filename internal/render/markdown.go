package render

import (
	"regexp"
	"strings"

	"github.com/kzzz/atc-cf-bot/internal/domain"
)

var openingFence = regexp.MustCompile("(?m)^(`{3,})([^\\n]*)\\n")

// SplitMarkdown closes and reopens code blocks around unchanged fragments.
// Plain sections retain protected formulas and whole Unicode graphemes.
func SplitMarkdown(text string, budget int, atoms []string) ([]string, error) {
	var out []string
	for text != "" {
		loc := openingFence.FindStringSubmatchIndex(text)
		if loc == nil {
			parts, e := splitProtected(text, budget, atoms)
			if e != nil {
				return nil, e
			}
			out = append(out, parts...)
			break
		}
		if loc[0] > 0 {
			parts, e := splitProtected(text[:loc[0]], budget, atoms)
			if e != nil {
				return nil, e
			}
			out = append(out, parts...)
		}
		mark := text[loc[2]:loc[3]]
		language := text[loc[4]:loc[5]]
		rest := text[loc[1]:]
		closePattern := regexp.MustCompile("(?m)^" + regexp.QuoteMeta(mark) + "[ \\t]*(?:\\n|$)")
		end := closePattern.FindStringIndex(rest)
		if end == nil {
			return nil, domain.Fail(domain.Unsupported)
		}
		body := rest[:end[0]]
		parts, e := SampleParts(body, budget-Units(mark)*2-Units(language)-3)
		if e != nil {
			return nil, e
		}
		for _, part := range parts {
			suffix := ""
			if !strings.HasSuffix(part, "\n") {
				suffix = "\n"
			}
			piece := mark + language + "\n" + part + suffix + mark
			if Units(piece) > budget {
				return nil, domain.Fail(domain.Unsupported)
			}
			out = append(out, piece)
		}
		text = rest[end[1]:]
	}
	return out, nil
}
func SplitTokens(text string, budget int, tokens []string) ([]string, error) {
	return splitProtected(text, budget, tokens)
}
