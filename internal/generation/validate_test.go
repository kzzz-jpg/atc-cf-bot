package generation

import (
	"strings"
	"testing"
)

func TestSolutionCompleteness(t *testing.T) {
	valid := "AI 產生，未經原站評測\n## 題意與關鍵觀察\n觀察。\n## 演算法\n加總。\n## 正確性\n逐項加總正確。\n## 複雜度\n時間 O(N)、空間 O(1)。\n## 邊界與範例\n單一元素。"
	for _, language := range []string{"cpp17", "python3"} {
		if e := validateAnswer("solution", language, valid); e != nil {
			t.Fatal(e)
		}
	}
	for _, text := range []string{"OK", strings.Replace(valid, "## 正確性", "## 別的", 1), strings.Replace(valid, "空間", "", 1), valid + "\n```cpp\nint main() {}\n```", valid + "\n~~~python\nprint(1)\n~~~", valid + "\n## 參考程式碼\nint main() {}"} {
		if validateAnswer("solution", "cpp17", text) == nil {
			t.Fatal("incomplete solution cached")
		}
	}
	if validateAnswer("hint", "cpp17", "```cpp\nint main(){}\n```") == nil {
		t.Fatal("full code hint accepted")
	}
}

func TestDiscordAnswersFormatting(t *testing.T) {
	hint := "-# 💡 提示由 AI 產生，僅供思考參考\n\n## 💡 切入方向\n- **重點**：考慮 `N` 的總和。\n- 限制式為 `1 ≤ N ≤ 10^5`。\n\n> 核心公式：`S = ∑ a_i`"
	solution := "-# ⚠️ 本題解由 AI 產生，未經原站評測，僅供學習參考\n\n## 題意與關鍵觀察\n總和。\n\n> 核心公式：`S = ∑ a_i`\n\n## 演算法\n逐項加總。\n\n## 正確性\n每項恰好加一次。\n\n## 複雜度\n- **時間複雜度**：`O(N)`\n- **空間複雜度**：`O(1)`\n\n## 邊界與範例\n單一元素。"
	for _, kind := range []string{"hint", "solution"} {
		t.Run(kind, func(t *testing.T) {
			valid := hint
			if kind == "solution" {
				valid = solution
			}
			if err := validateAnswer(kind, "cpp17", valid); err != nil {
				t.Fatalf("valid Discord answer rejected: %v", err)
			}
			for _, invalid := range []string{
				valid + "\n```cpp\nint main() {}\n```",
				valid + "\n```python\nprint(1)\n```",
				valid + "\n```go\nfunc main() {}\n```",
				valid + "\n```\nx",
			} {
				if validateAnswer(kind, "cpp17", invalid) == nil {
					t.Fatalf("invalid answer accepted: %q", invalid)
				}
			}
		})
	}
	// Section headings hidden inside a code block must not satisfy completeness.
	if validateAnswer("solution", "cpp17", "```text\n"+solution+"\n```") == nil {
		t.Fatal("solution sections inside code block accepted")
	}
}
