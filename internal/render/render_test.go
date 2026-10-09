package render

import (
	"github.com/kzzz/atc-cf-bot/internal/domain"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestUTF16AndGraphemeRoundtrip(t *testing.T) {
	text := strings.Repeat("漢字👩‍💻e\u0301\n", 400)
	parts, e := SplitPlain(text, 1800)
	if e != nil {
		t.Fatal(e)
	}
	if strings.Join(parts, "") != text {
		t.Fatal("roundtrip lost bytes")
	}
	for _, p := range parts {
		if !utf8.ValidString(p) || Units(p) > 1800 || strings.HasPrefix(p, "\u0301") {
			t.Fatal("unsafe split")
		}
	}
	if Units("😀中") != 3 {
		t.Fatal("not UTF-16 count")
	}
}
func TestSamplesExact(t *testing.T) {
	for _, text := range []string{"1 2  \n3\n", strings.Repeat("9", 4000) + "  \n", strings.Repeat("中", 2000), ""} {
		parts, e := SampleParts(text, 1650)
		if e != nil {
			t.Fatal(e)
		}
		if strings.Join(parts, "") != text {
			t.Fatal("sample modified")
		}
		for _, p := range parts {
			if Units(p) > 1650 {
				t.Fatal("sample too long")
			}
		}
	}
}
func TestFormulaNeverSplit(t *testing.T) {
	a := strings.Repeat("x", 100)
	parts, e := splitProtected(strings.Repeat("字", 100)+a+" tail", 120, []string{a})
	if e != nil {
		t.Fatal(e)
	}
	found := false
	for _, p := range parts {
		if strings.Contains(p, a) {
			found = true
		}
	}
	if !found {
		t.Fatal("formula split")
	}
	if _, e = splitProtected(a, 50, []string{a}); e == nil {
		t.Fatal("oversized formula accepted")
	}
}
func TestPayloadLimitAndButtons(t *testing.T) {
	s := domain.Statement{Title: "題目", URL: "https://atcoder.jp/contests/abc001/tasks/a", Blocks: []domain.Block{{ID: "b1", Text: strings.Repeat("中文😀", 2000)}}, Samples: []domain.Sample{{Input: "1\n", Output: "2\n"}}}
	tr := domain.Translation{Blocks: map[string]string{"b1": s.Blocks[0].Text}}
	ps, e := Problem(s, tr, domain.ProblemRef{Platform: domain.AtCoder, ExternalID: "a"}, domain.NewID(), "2026-10-08", "題目 1", 64)
	if e != nil {
		t.Fatal(e)
	}
	for _, p := range ps {
		if Units(p.Content) > 2000 || len(p.AllowedMentions.Parse) != 0 {
			t.Fatal("unsafe payload")
		}
	}
	if len(ps[len(ps)-1].Components[0].Components) != 3 {
		t.Fatal("missing buttons")
	}
	if _, e = Problem(s, tr, domain.ProblemRef{}, domain.NewID(), "2026-10-08", "題目 1", 1); e == nil {
		t.Fatal("partial render accepted")
	}
}
func TestMarkdownCodeContinuations(t *testing.T) {
	text := "Explanation\n\n```cpp\n" + strings.Repeat("cout << 1;\n", 400) + "```\nConclusion"
	parts, e := SplitMarkdown(text, 1800, nil)
	if e != nil {
		t.Fatal(e)
	}
	for _, p := range parts {
		if Units(p) > 1800 || strings.Count(p, "```")%2 != 0 {
			t.Fatal("broken code block")
		}
	}
	if _, e = SplitMarkdown("```cpp\nmissing fence", 1800, nil); e == nil {
		t.Fatal("unclosed fence accepted")
	}
}
func TestSamplesComeBeforeExplanation(t *testing.T) {
	idx := 0
	s := domain.Statement{Title: "A", URL: "https://atcoder.jp/a", Blocks: []domain.Block{{ID: "a", Text: "Original"}, {ID: "b", Kind: "sample_input", Text: "1\n", SampleIndex: &idx}, {ID: "c", Kind: "sample_output", Text: "2\n", SampleIndex: &idx}, {ID: "d", Kind: "explanation", Text: "Explanation"}}, Samples: []domain.Sample{{Input: "1\n", Output: "2\n"}}}
	tr := domain.Translation{Blocks: map[string]string{"a": "原文", "b": "WRONG INPUT", "c": "WRONG OUTPUT", "d": "說明"}}
	payloads, e := Problem(s, tr, domain.ProblemRef{}, domain.NewID(), "2026-10-09", "題目 1", 64)
	if e != nil {
		t.Fatal(e)
	}
	var all string
	for _, p := range payloads {
		all += p.Content
	}
	if strings.Contains(all, "WRONG") || strings.Index(all, "範例 1 輸出") > strings.Index(all, "說明") {
		t.Fatal("sample transformed or reordered")
	}
}

func TestShortProblemCombinesSectionsAndSamples(t *testing.T) {
	s := domain.Statement{
		Title: "加總", URL: "https://atcoder.jp/contests/abc001/tasks/a",
		Blocks:  []domain.Block{{ID: "statement", Text: "計算兩數之和。"}, {ID: "input", Text: "輸入兩個整數。"}, {ID: "output", Text: "輸出總和。"}, {ID: "constraints", Text: "整數介於 1 和 100。"}},
		Samples: []domain.Sample{{Input: "1 2\n", Output: "3\n"}, {Input: "4 5\n", Output: "9\n"}},
	}
	tr := domain.Translation{Blocks: map[string]string{}}
	for _, b := range s.Blocks {
		tr.Blocks[b.ID] = b.Text
	}
	payloads, e := Problem(s, tr, domain.ProblemRef{Platform: domain.AtCoder, ExternalID: "a"}, domain.NewID(), "2026-10-09", "加題 #1", 1)
	if e != nil || len(payloads) != 1 {
		t.Fatalf("short problem split: count=%d error=%v", len(payloads), e)
	}
	text := payloads[0].Content
	for _, unwanted := range []string{"【", "段落", "加題 #1", "2026-10-09", "連續片段"} {
		if strings.Contains(text, unwanted) {
			t.Fatalf("unwanted segment label %q", unwanted)
		}
	}
	for _, expected := range []string{"計算兩數之和。", "輸入兩個整數。", "輸出總和。", "整數介於 1 和 100。", "```text\n1 2\n```", "```text\n9\n```"} {
		if !strings.Contains(text, expected) {
			t.Fatalf("missing original section/sample %q", expected)
		}
	}
	if len(payloads[0].Components) != 1 || len(payloads[0].Components[0].Components) != 3 {
		t.Fatal("missing final buttons")
	}
}

func TestParagraphBreakDoesNotSplitFittingText(t *testing.T) {
	text := strings.Repeat("中", 1200) + "\n\n" + strings.Repeat("文", 300)
	parts, e := SplitMarkdown(text, 2000, nil)
	if e != nil || len(parts) != 1 || parts[0] != text {
		t.Fatal("fitting paragraphs split or changed", e, len(parts))
	}
}

func TestImageSharesProblemMessage(t *testing.T) {
	s := domain.Statement{Title: "圖形", URL: "https://atcoder.jp/a", Blocks: []domain.Block{{ID: "a", Text: "請看圖片。"}}, Samples: []domain.Sample{{Input: "1\n", Output: "2\n"}}, Assets: []domain.Asset{{ID: "diagram", MIME: "image/png", Body: []byte("image")}}}
	tr := domain.Translation{Blocks: map[string]string{"a": "請看圖片。"}}
	ps, e := Problem(s, tr, domain.ProblemRef{}, domain.NewID(), "2026-10-09", "題目 1", 1)
	if e != nil || len(ps) != 1 || len(ps[0].Attachments) != 1 || len(ps[0].Embeds) != 1 {
		t.Fatal("image unnecessarily used another message", e)
	}
}
