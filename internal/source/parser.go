package source

import (
	"bytes"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/kzzz/atc-cf-bot/internal/domain"
	"golang.org/x/net/html"
)

const ParserVersion = "html-v1"

var number = regexp.MustCompile(`(?:\b\d+(?:\.\d+)?\b)`)
var special = regexp.MustCompile(`(?i)(interactive problem|interactive task|output.only|output-only|communication with|flush (?:the |your )?output|this is an interactive|対話型|インタラクティブ|出力のみ)`)

func attr(n *html.Node, k string) string {
	for _, a := range n.Attr {
		if a.Key == k {
			return a.Val
		}
	}
	return ""
}
func class(n *html.Node, c string) bool {
	for _, s := range strings.Fields(attr(n, "class")) {
		if s == c {
			return true
		}
	}
	return false
}
func find(n *html.Node, p func(*html.Node) bool) *html.Node {
	if p(n) {
		return n
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if v := find(c, p); v != nil {
			return v
		}
	}
	return nil
}
func all(n *html.Node, p func(*html.Node) bool) []*html.Node {
	var out []*html.Node
	var walk func(*html.Node)
	walk = func(v *html.Node) {
		if p(v) {
			out = append(out, v)
			return
		}
		for c := v.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return out
}
func plain(n *html.Node) string {
	if n == nil {
		return ""
	}
	if n.Type == html.TextNode {
		return n.Data
	}
	var b strings.Builder
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Data == "br" {
			b.WriteByte('\n')
		} else {
			b.WriteString(plain(c))
		}
	}
	return b.String()
}
func sampleText(n *html.Node) string {
	if n == nil {
		return ""
	}
	if n.Type == html.TextNode {
		return strings.ReplaceAll(n.Data, "\r\n", "\n")
	}
	var b strings.Builder
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Data == "br" {
			b.WriteByte('\n')
		} else if c.Data == "div" {
			b.WriteString(sampleText(c))
			if !strings.HasSuffix(b.String(), "\n") {
				b.WriteByte('\n')
			}
		} else {
			b.WriteString(sampleText(c))
		}
	}
	return b.String()
}
func kind(heading string) string {
	h := strings.ToLower(heading)
	switch {
	case strings.Contains(h, "sample input") || strings.Contains(h, "入力例"):
		return "sample_input"
	case strings.Contains(h, "sample output") || strings.Contains(h, "出力例"):
		return "sample_output"
	case h == "input" || h == "入力":
		return "input"
	case h == "output" || h == "出力":
		return "output"
	case strings.Contains(h, "constraint") || h == "制約":
		return "constraints"
	case h == "problem statement" || h == "問題文":
		return "statement"
	case strings.Contains(h, "note") || strings.Contains(h, "explanation") || strings.Contains(h, "解説"):
		return "explanation"
	default:
		return "other"
	}
}

// Parse only accepts recognized statement containers and mandatory input/output
// sections. Unknown markup carrying formulas fails closed.
func Parse(platform domain.Platform, raw []byte, sourceURL string, maxBytes int) (domain.Statement, error) {
	if bytes.Contains(raw, []byte("⟪ATOM_")) {
		return domain.Statement{}, domain.Fail(domain.Unsupported)
	}
	doc, e := html.Parse(bytes.NewReader(raw))
	if e != nil {
		return domain.Statement{}, domain.Fail(domain.Unsupported)
	}
	s := domain.Statement{SchemaVersion: "1", ParserVersion: ParserVersion, Language: "en", URL: sourceURL}
	var root *html.Node
	if platform == domain.AtCoder {
		root = find(doc, func(n *html.Node) bool { return attr(n, "id") == "task-statement" })
		if root == nil {
			return s, domain.Fail(domain.Transient)
		}
		english := find(root, func(n *html.Node) bool { return class(n, "lang-en") })
		if english != nil {
			root = english
		} else {
			jp := find(root, func(n *html.Node) bool { return class(n, "lang-ja") })
			if jp != nil {
				root = jp
				s.Language = "ja"
			}
		}
		title := find(doc, func(n *html.Node) bool { return class(n, "h2") })
		s.Title = strings.TrimSpace(titleText(title))
		limits := find(doc, func(n *html.Node) bool {
			return n.Data == "p" && strings.Contains(plain(n), "Time Limit:") && strings.Contains(plain(n), "Memory Limit:")
		})
		if limits != nil {
			values := strings.SplitN(plain(limits), "/", 2)
			s.TimeLimit = strings.TrimSpace(strings.TrimPrefix(values[0], "Time Limit:"))
			s.MemoryLimit = strings.TrimSpace(strings.TrimPrefix(values[1], "Memory Limit:"))
		}
		if s.Title == "" {
			s.Title = strings.TrimSpace(plain(find(doc, func(n *html.Node) bool { return n.Data == "title" })))
		}
	} else if platform == domain.Codeforces {
		root = find(doc, func(n *html.Node) bool { return class(n, "problem-statement") })
		if root == nil {
			return s, domain.Fail(domain.Transient)
		}
		s.Title = strings.TrimSpace(plain(find(root, func(n *html.Node) bool { return class(n, "title") })))
		s.TimeLimit = limitText(find(root, func(n *html.Node) bool { return class(n, "time-limit") }))
		s.MemoryLimit = limitText(find(root, func(n *html.Node) bool { return class(n, "memory-limit") }))
	} else {
		return s, domain.Fail(domain.Unsupported)
	}
	if special.MatchString(plain(root)) {
		return s, domain.Fail(domain.Unsupported)
	}
	add := func(k string, n *html.Node) error {
		b := domain.Block{ID: fmt.Sprintf("b%03d", len(s.Blocks)+1), Kind: k}
		var err error
		b.Text, err = nodeText(n, &b, &s)
		if err != nil {
			return err
		}
		b.Text = strings.TrimSpace(b.Text)
		if b.Text != "" {
			protectNumbers(&b)
			s.Blocks = append(s.Blocks, b)
		}
		return nil
	}
	if platform == domain.AtCoder {
		sections := all(root, func(n *html.Node) bool { return n.Data == "section" })
		if len(sections) == 0 {
			return s, domain.Fail(domain.Unsupported)
		}
		var ins, outs []string
		for _, sec := range sections {
			heading := find(sec, func(n *html.Node) bool { return n.Data == "h3" })
			k := kind(strings.TrimSpace(plain(heading)))
			if k == "sample_input" || k == "sample_output" {
				pre := find(sec, func(n *html.Node) bool { return n.Data == "pre" })
				if pre == nil {
					return s, domain.Fail(domain.Unsupported)
				}
				var index int
				var text string
				if k == "sample_input" {
					index = len(ins)
					text = sampleText(pre)
					ins = append(ins, text)
				} else {
					index = len(outs)
					text = sampleText(pre)
					outs = append(outs, text)
				}
				s.Blocks = append(s.Blocks, domain.Block{ID: fmt.Sprintf("b%03d", len(s.Blocks)+1), Kind: k, Text: text, SampleIndex: &index})
				// Keep any prose after the sample, outside its heading/pre.
				for c := sec.FirstChild; c != nil; c = c.NextSibling {
					if c != heading && c != pre && strings.TrimSpace(plain(c)) != "" {
						if e = add("explanation", c); e != nil {
							return s, e
						}
					}
				}
			} else {
				if e = add(k, sec); e != nil {
					return s, e
				}
			}
		}
		if len(ins) != len(outs) {
			return s, domain.Fail(domain.Unsupported)
		}
		for i := range ins {
			s.Samples = append(s.Samples, domain.Sample{Input: ins[i], Output: outs[i]})
		}
	} else {
		for c := root.FirstChild; c != nil; c = c.NextSibling {
			if c.Type != html.ElementNode || class(c, "header") {
				continue
			}
			k := "statement"
			switch {
			case class(c, "input-specification"):
				k = "input"
			case class(c, "output-specification"):
				k = "output"
			case class(c, "note"):
				k = "explanation"
			case class(c, "sample-tests"):
				inputs := all(c, func(n *html.Node) bool { return class(n, "input") })
				outputs := all(c, func(n *html.Node) bool { return class(n, "output") })
				if len(inputs) != len(outputs) {
					return s, domain.Fail(domain.Unsupported)
				}
				for i := range inputs {
					ip := find(inputs[i], func(n *html.Node) bool { return n.Data == "pre" })
					op := find(outputs[i], func(n *html.Node) bool { return n.Data == "pre" })
					if ip == nil || op == nil {
						return s, domain.Fail(domain.Unsupported)
					}
					index := len(s.Samples)
					input, output := sampleText(ip), sampleText(op)
					s.Samples = append(s.Samples, domain.Sample{Input: input, Output: output})
					s.Blocks = append(s.Blocks, domain.Block{ID: fmt.Sprintf("b%03d", len(s.Blocks)+1), Kind: "sample_input", Text: input, SampleIndex: &index})
					s.Blocks = append(s.Blocks, domain.Block{ID: fmt.Sprintf("b%03d", len(s.Blocks)+1), Kind: "sample_output", Text: output, SampleIndex: &index})
				}
				continue
			}
			if e = add(k, c); e != nil {
				return s, e
			}
		}
	}
	for _, b := range s.Blocks {
		for _, a := range b.Atoms {
			if _, e = MathText(a.Text); e != nil {
				return s, e
			}
		}
	}
	if e = s.Validate(maxBytes); e != nil {
		return s, e
	}
	return s, nil
}
func limitText(n *html.Node) string {
	if n == nil {
		return ""
	}
	var b strings.Builder
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if !class(c, "property-title") {
			b.WriteString(plain(c))
		}
	}
	return strings.TrimSpace(b.String())
}
func nodeText(n *html.Node, b *domain.Block, s *domain.Statement) (string, error) {
	if n.Type == html.TextNode {
		text := n.Data
		// Codeforces represents formulas using $$$...$$$; preserve each whole atom.
		var out strings.Builder
		for {
			start := strings.Index(text, "$$$")
			if start < 0 {
				out.WriteString(text)
				break
			}
			out.WriteString(text[:start])
			rest := text[start+3:]
			end := strings.Index(rest, "$$$")
			if end < 0 {
				return "", domain.Fail(domain.Unsupported)
			}
			out.WriteString(atom(b, rest[:end]))
			text = rest[end+3:]
		}
		return out.String(), nil
	}
	if n.Data == "script" && strings.HasPrefix(attr(n, "type"), "math/tex") {
		return atom(b, plain(n)), nil
	}
	if n.Data == "script" || n.Data == "style" {
		return "", nil
	}
	if n.Data == "math" || n.Data == "svg" || n.Data == "iframe" || n.Data == "object" {
		return "", domain.Fail(domain.Unsupported)
	}
	if n.Data == "var" || class(n, "tex-span") || class(n, "math") {
		return atom(b, plain(n)), nil
	}
	if n.Data == "img" {
		u, e := url.Parse(attr(n, "src"))
		if e != nil {
			return "", domain.Fail(domain.Unsupported)
		}
		base, _ := url.Parse(s.URL)
		u = base.ResolveReference(u)
		a := domain.Asset{ID: domain.NewID(), URL: u.String()}
		s.Assets = append(s.Assets, a)
		return atom(b, fmt.Sprintf("[必要圖片 %d]", len(s.Assets))), nil
	}
	if n.Data == "br" {
		return "\n", nil
	}
	var out strings.Builder
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		t, e := nodeText(c, b, s)
		if e != nil {
			return "", e
		}
		out.WriteString(t)
	}
	text := out.String()
	switch n.Data {
	case "p", "div", "section", "h3", "h4", "ul", "ol":
		text += "\n\n"
	case "li":
		text = "- " + text + "\n"
	case "pre":
		text = "\n```text\n" + text + "\n```\n"
	case "code":
		if !strings.Contains(text, "⟪ATOM_") {
			text = atom(b, text)
		}
	case "sup":
		text = "^(" + text + ")"
	case "sub":
		text = "_(" + text + ")"
	}
	return text, nil
}
func atom(b *domain.Block, text string) string {
	token := fmt.Sprintf("⟪ATOM_%s_%d⟫", b.ID, len(b.Atoms))
	b.Atoms = append(b.Atoms, domain.Atom{Token: token, Text: text})
	return token
}
func protectNumbers(b *domain.Block) {
	// Skip existing placeholders when protecting numeric constraints and operators.
	text := b.Text
	var out strings.Builder
	for len(text) > 0 {
		if strings.HasPrefix(text, "⟪ATOM_") {
			end := strings.Index(text, "⟫")
			if end >= 0 {
				end += len("⟫")
				out.WriteString(text[:end])
				text = text[end:]
				continue
			}
		}
		next := strings.Index(text, "⟪ATOM_")
		segment := text
		if next >= 0 {
			segment = text[:next]
		}
		segment = number.ReplaceAllStringFunc(segment, func(n string) string { return atom(b, n) })
		out.WriteString(segment)
		if next < 0 {
			break
		}
		text = text[next:]
	}
	b.Text = out.String()
}
func Restore(b domain.Block, text string) (string, error) {
	for _, a := range b.Atoms {
		if strings.Count(text, a.Token) != strings.Count(b.Text, a.Token) {
			return "", domain.Fail(domain.Unsupported)
		}
	}
	for _, a := range b.Atoms {
		text = strings.ReplaceAll(text, a.Token, a.Text)
	}
	if strings.Contains(text, "⟪ATOM_") {
		return "", domain.Fail(domain.Unsupported)
	}
	return text, nil
}
func Readable(s domain.Statement) string {
	var b strings.Builder
	b.WriteString(s.Title + "\n")
	ordered := false
	for _, block := range s.Blocks {
		if block.SampleIndex != nil {
			ordered = true
			i := *block.SampleIndex
			label := "input"
			if block.Kind == "sample_output" {
				label = "output"
			}
			fmt.Fprintf(&b, "Sample %d %s:\n%s\n", i+1, label, block.Text)
			continue
		}
		text, _ := Restore(block, block.Text)
		b.WriteString(text + "\n")
	}
	if !ordered {
		for i, sample := range s.Samples {
			fmt.Fprintf(&b, "Sample %d input:\n%s\nOutput:\n%s\n", i+1, sample.Input, sample.Output)
		}
	}
	return b.String()
}

func titleText(n *html.Node) string {
	if n == nil {
		return ""
	}
	if n.Data == "a" || class(n, "btn") {
		return ""
	}
	if n.Type == html.TextNode {
		return n.Data
	}
	var b strings.Builder
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		b.WriteString(titleText(c))
	}
	return b.String()
}
