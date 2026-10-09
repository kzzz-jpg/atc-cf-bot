package render

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSpoilerEscapesFormattingAndDelimiters(t *testing.T) {
	text := "## 觀察\n**提示**：||x|| 與 `y`\\n\n```text\nz\n```"
	parts, err := SplitSpoiler(text, 1800)
	want := "||## 觀察\n**提示**：\\|\\|x\\|\\| 與 \\`y\\`\\\\n\n\\`\\`\\`text\nz\n\\`\\`\\`||"
	if err != nil || len(parts) != 1 || parts[0] != want {
		t.Fatalf("spoiler=%q error=%v", parts, err)
	}
}

func TestSpoilerContinuationsStayClosedAndPreserveText(t *testing.T) {
	text := strings.Repeat("漢字👩‍💻e\u0301\\||`\n", 400)
	parts, err := SplitSpoiler(text, 83)
	if err != nil || len(parts) < 2 {
		t.Fatalf("count=%d error=%v", len(parts), err)
	}
	var restored strings.Builder
	for _, part := range parts {
		if !utf8.ValidString(part) || Units(part) > 83 || !strings.HasPrefix(part, "||") || !strings.HasSuffix(part, "||") {
			t.Fatalf("unsafe spoiler fragment %q", part)
		}
		body := part[2 : len(part)-2]
		for len(body) > 0 {
			if body[0] == '\\' {
				if len(body) < 2 || !strings.ContainsRune("\\|`", rune(body[1])) {
					t.Fatalf("split escape %q", body)
				}
				restored.WriteByte(body[1])
				body = body[2:]
			} else {
				if body[0] == '|' || body[0] == '`' {
					t.Fatalf("unescaped delimiter %q", body)
				}
				r, size := utf8.DecodeRuneInString(body)
				restored.WriteRune(r)
				body = body[size:]
			}
		}
	}
	if restored.String() != text {
		t.Fatal("spoiler splitting changed answer text")
	}
}
