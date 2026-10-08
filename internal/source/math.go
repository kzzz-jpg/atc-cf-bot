package source

import (
	"regexp"
	"strings"

	"github.com/kzzz/atc-cf-bot/internal/domain"
)

var texCommand = regexp.MustCompile(`\\[A-Za-z]+`)
var texSymbols = map[string]string{
	`\leq`: "≤", `\le`: "≤", `\geq`: "≥", `\ge`: "≥", `\neq`: "≠", `\ne`: "≠", `\times`: "×", `\cdot`: "·", `\ldots`: "…", `\cdots`: "…", `\dots`: "…", `\infty`: "∞", `\in`: "∈", `\notin`: "∉", `\subseteq`: "⊆", `\subset`: "⊂", `\cup`: "∪", `\cap`: "∩", `\emptyset`: "∅", `\forall`: "∀", `\exists`: "∃", `\land`: "∧", `\lor`: "∨", `\neg`: "¬", `\to`: "→", `\rightarrow`: "→", `\leftarrow`: "←", `\Rightarrow`: "⇒", `\Leftrightarrow`: "⇔", `\equiv`: "≡", `\approx`: "≈", `\pm`: "±", `\sum`: "Σ", `\prod`: "Π", `\lfloor`: "⌊", `\rfloor`: "⌋", `\lceil`: "⌈", `\rceil`: "⌉", `\langle`: "⟨", `\rangle`: "⟩", `\vert`: "|", `\mid`: "|", `\alpha`: "α", `\beta`: "β", `\gamma`: "γ", `\delta`: "δ", `\epsilon`: "ε", `\theta`: "θ", `\lambda`: "λ", `\mu`: "μ", `\pi`: "π", `\sigma`: "σ", `\omega`: "ω", `\Delta`: "Δ", `\log`: "log", `\ln`: "ln", `\max`: "max", `\min`: "min", `\gcd`: "gcd", `\bmod`: "mod", `\pmod`: "mod", `\mod`: "mod", `\sin`: "sin", `\cos`: "cos", `\tan`: "tan", `\left`: "", `\right`: "", `\displaystyle`: "", `\textstyle`: "", `\quad`: " ", `\qquad`: "  ",
}

// MathText preserves the stored TeX while deriving a readable text form. Unknown
// commands fail closed instead of dropping a potentially essential formula.
func MathText(raw string) (string, error) {
	text := raw
	for _, cmd := range []string{`\mathrm`, `\textrm`, `\text`, `\mathbf`, `\mathit`, `\operatorname`, `\overline`, `\sqrt`, `\frac`, `\dfrac`, `\tfrac`} {
		for {
			at := strings.Index(text, cmd)
			if at < 0 {
				break
			}
			start := at + len(cmd)
			for start < len(text) && text[start] == ' ' {
				start++
			}
			arg, end, ok := group(text, start)
			if !ok {
				return "", domain.Fail(domain.Unsupported)
			}
			replacement := arg
			switch cmd {
			case `\overline`:
				replacement = "overline(" + arg + ")"
			case `\sqrt`:
				replacement = "sqrt(" + arg + ")"
			case `\frac`, `\dfrac`, `\tfrac`:
				second := end
				for second < len(text) && text[second] == ' ' {
					second++
				}
				den, after, ok := group(text, second)
				if !ok {
					return "", domain.Fail(domain.Unsupported)
				}
				replacement = "(" + arg + ")/(" + den + ")"
				end = after
			}
			text = text[:at] + replacement + text[end:]
		}
	}
	unsupported := false
	text = texCommand.ReplaceAllStringFunc(text, func(cmd string) string {
		if v, ok := texSymbols[cmd]; ok {
			return v
		}
		unsupported = true
		return cmd
	})
	if unsupported {
		return "", domain.Fail(domain.Unsupported)
	}
	for _, p := range [][2]string{{`\,`, " "}, {`\;`, " "}, {`\!`, ""}, {`\%`, "%"}, {`\_`, "_"}, {`\{`, "{"}, {`\}`, "}"}, {`\ `, " "}} {
		text = strings.ReplaceAll(text, p[0], p[1])
	}
	return text, nil
}
func group(s string, at int) (string, int, bool) {
	if at >= len(s) || s[at] != '{' {
		return "", at, false
	}
	depth := 0
	for i := at; i < len(s); i++ {
		if s[i] == '{' {
			depth++
		}
		if s[i] == '}' {
			depth--
			if depth == 0 {
				return s[at+1 : i], i + 1, true
			}
		}
	}
	return "", at, false
}
func DisplayBlock(b domain.Block, text string) (string, []string, error) {
	for _, a := range b.Atoms {
		if strings.Count(text, a.Token) != strings.Count(b.Text, a.Token) {
			return "", nil, domain.Fail(domain.Unsupported)
		}
	}
	var atoms []string
	for _, a := range b.Atoms {
		readable, e := MathText(a.Text)
		if e != nil {
			return "", nil, e
		}
		if !strings.Contains(readable, "`") && !strings.Contains(readable, "\n") && !strings.HasPrefix(readable, "[必要圖片") {
			readable = "`" + readable + "`"
		}
		text = strings.ReplaceAll(text, a.Token, readable)
		atoms = append(atoms, readable)
	}
	if strings.Contains(text, "⟪ATOM_") {
		return "", nil, domain.Fail(domain.Unsupported)
	}
	return text, atoms, nil
}
