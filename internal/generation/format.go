package generation

import (
	"regexp"
	"strings"

	"github.com/kzzz/atc-cf-bot/internal/source"
)

var (
	answerFormatting = regexp.MustCompile("(?s)```[a-zA-Z0-9_-]*\n.*?```|~~~[a-zA-Z0-9_-]*\n.*?~~~|`[^`\\n]*`|\\$\\$.*?\\$\\$|\\\\\\[.*?\\\\\\]|\\$[^$\\n]+\\$|\\\\\\(.*?\\\\\\)")
	texBraceCommands = regexp.MustCompile(`\\(mathcal|mathbb|mathbf|mathit|mathrm|textrm|text|operatorname)\{([^{}]+)\}`)
	headingNoNewline = regexp.MustCompile("(?m)([^\n])\n(##+ [^\n]+)")
	multiNewlines    = regexp.MustCompile("\n{3,}")
)

// normalizeAnswer prepares answer text for native Discord Markdown rendering.
// It strips spoiler markers, converts LaTeX equations into clean Unicode math / inline code,
// normalizes equation blocks into Discord blockquotes, and fixes heading spacing.
func normalizeAnswer(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	// Private replies are already ephemeral; spoilers add friction and break Discord code blocks.
	text = strings.ReplaceAll(text, "||", "")

	text = answerFormatting.ReplaceAllStringFunc(text, func(span string) string {
		if strings.HasPrefix(span, "```") || strings.HasPrefix(span, "~~~") {
			// Check if it's a LaTeX fence
			lines := strings.Split(span, "\n")
			tag := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(lines[0], span[:3])))
			if tag == "latex" || tag == "tex" {
				var bodyLines []string
				if len(lines) >= 2 {
					bodyLines = lines[1 : len(lines)-1]
				}
				cleaned := cleanMath(strings.Join(bodyLines, "\n"))
				if cleaned == "" {
					return ""
				}
				var quoted []string
				for _, line := range strings.Split(cleaned, "\n") {
					line = strings.TrimSpace(line)
					if line != "" {
						quoted = append(quoted, "> "+line)
					}
				}
				if len(quoted) == 0 {
					return ""
				}
				return "\n\n" + strings.Join(quoted, "\n") + "\n\n"
			}
			return span
		}
		if strings.HasPrefix(span, "`") {
			inner := span[1 : len(span)-1]
			if strings.HasPrefix(inner, "$") && strings.HasSuffix(inner, "$") && len(inner) >= 2 {
				inner = inner[1 : len(inner)-1]
			}
			if strings.Contains(inner, `\`) {
				return "`" + cleanMath(inner) + "`"
			}
			return span
		}
		if strings.HasPrefix(span, "$$") || strings.HasPrefix(span, `\[`) {
			inner := span[2 : len(span)-2]
			cleaned := cleanMath(inner)
			if cleaned == "" {
				return ""
			}
			var quoted []string
			for _, line := range strings.Split(cleaned, "\n") {
				line = strings.TrimSpace(line)
				if line != "" {
					quoted = append(quoted, "> "+line)
				}
			}
			return "\n\n" + strings.Join(quoted, "\n") + "\n\n"
		}
		// Inline math: $...$ or \(...\)
		var inner string
		if strings.HasPrefix(span, "$") {
			inner = span[1 : len(span)-1]
		} else {
			inner = span[2 : len(span)-2]
		}
		cleaned := cleanMath(inner)
		if cleaned == "" {
			return ""
		}
		return "`" + cleaned + "`"
	})

	// Ensure headings have proper line breaks before them
	text = headingNoNewline.ReplaceAllString(text, "$1\n\n$2")
	text = multiNewlines.ReplaceAllString(text, "\n\n")
	return strings.TrimSpace(text)
}

func cleanMath(raw string) string {
	text := strings.TrimSpace(raw)
	if text == "" {
		return ""
	}
	// Try source.MathText directly for standard LaTeX formulas (e.g. \sum_{i=1}^N a_i = S)
	if res, err := source.MathText(text); err == nil {
		return strings.TrimSpace(res)
	}
	// Simplify common font / styling commands with braces: \mathcal{O} -> O, \text{ans} -> ans
	for i := 0; i < 3; i++ {
		prev := text
		text = texBraceCommands.ReplaceAllString(text, "$2")
		if text == prev {
			break
		}
	}
	if res, err := source.MathText(text); err == nil {
		return strings.TrimSpace(res)
	}
	// Fallback conversion for expressions that source.MathText rejected
	return fallbackMathText(text)
}

func fallbackMathText(text string) string {
	for _, p := range [][2]string{
		{`\leq`, "≤"}, {`\le`, "≤"}, {`\geq`, "≥"}, {`\ge`, "≥"},
		{`\neq`, "≠"}, {`\ne`, "≠"}, {`\times`, "×"}, {`\cdot`, "·"},
		{`\sum`, "Σ"}, {`\prod`, "Π"}, {`\infty`, "∞"}, {`\in`, "∈"},
		{`\notin`, "∉"}, {`\subset`, "⊂"}, {`\subseteq`, "⊆"},
		{`\dots`, "…"}, {`\ldots`, "…"}, {`\cdots`, "…"},
		{`\log`, "log"}, {`\ln`, "ln"}, {`\min`, "min"}, {`\max`, "max"},
		{`\approx`, "≈"}, {`\pm`, "±"}, {`\lfloor`, "⌊"}, {`\rfloor`, "⌋"},
		{`\lceil`, "⌈"}, {`\rceil`, "⌉"}, {`\to`, "→"}, {`\rightarrow`, "→"},
		{`\left`, ""}, {`\right`, ""}, {`\{`, "{"}, {`\}`, "}"},
		{`\_`, "_"}, {`\%`, "%"}, {`\,`, " "}, {`\;`, " "}, {`\!`, ""},
	} {
		text = strings.ReplaceAll(text, p[0], p[1])
	}
	// Remove remaining backslashes before English letters: \alpha -> alpha, etc.
	text = regexp.MustCompile(`\\([A-Za-z]+)`).ReplaceAllString(text, "$1")
	return strings.TrimSpace(text)
}
