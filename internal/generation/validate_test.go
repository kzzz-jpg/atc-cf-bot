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
