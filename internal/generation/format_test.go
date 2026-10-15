package generation

import (
	"strings"
	"testing"
)

func TestNormalizeAnswerDiscordFormatting(t *testing.T) {
	for _, formula := range []string{`\(\frac{a}{b}\)`, `$\frac{a}{b}$`, "`\\frac{a}{b}`"} {
		text := "## 觀察\n\n考慮 " + formula + "，變數為 `N`。"
		got := normalizeAnswer(text)
		if !strings.Contains(got, "`(a)/(b)`") || !strings.Contains(got, "`N`") {
			t.Fatalf("formula or variable not converted to Discord inline code: %q", got)
		}
		if strings.Contains(got, "```latex") {
			t.Fatalf("unexpected LaTeX fence: %q", got)
		}
		if err := validateAnswer("hint", "cpp17", got); err != nil {
			t.Fatal(err)
		}
		if normalizeAnswer(got) != got {
			t.Fatalf("normalization is not idempotent:\nfirst: %q\nsecond: %q", got, normalizeAnswer(got))
		}
	}

	// Display math converts to Discord blockquote
	display := "## 觀察\n\n$$\\sum_{i=1}^{N} a_i = S$$\n"
	gotDisplay := normalizeAnswer(display)
	if !strings.Contains(gotDisplay, "> Σ_{i=1}^{N} a_i = S") {
		t.Fatalf("display math not converted to blockquote: %q", gotDisplay)
	}

	// LaTeX fence converts to Discord blockquote
	fenced := "## 觀察\n```LaTeX\n\\sum_{i=1}^{N} a_i = S\n```\n"
	gotFenced := normalizeAnswer(fenced)
	if !strings.Contains(gotFenced, "> Σ_{i=1}^{N} a_i = S") {
		t.Fatalf("fenced LaTeX not converted to blockquote: %q", gotFenced)
	}

	// Spoilers are stripped
	spoilerText := "||## 觀察\n**提示**：考慮 `N`。||"
	gotSpoiler := normalizeAnswer(spoilerText)
	if strings.Contains(gotSpoiler, "||") || !strings.Contains(gotSpoiler, "考慮 `N`。") {
		t.Fatalf("spoiler not stripped: %q", gotSpoiler)
	}
}

func TestComplexityInDiscordFormatIsAccepted(t *testing.T) {
	text := "-# ⚠️ 本題解由 AI 產生，未經原站評測，僅供學習參考\n\n## 題意與關鍵觀察\n觀察陣列 `A`。\n\n> 核心公式：`S = ∑ A_i`\n\n## 演算法\n加總。\n\n## 正確性\n每個項目一次。\n\n## 複雜度\n- **時間複雜度**：`O(N)`\n- **空間複雜度**：`O(1)`\n\n## 邊界與範例\n單一項目。"
	if err := validateAnswer("solution", "cpp17", text); err != nil {
		t.Fatal(err)
	}
}
