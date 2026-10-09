package render

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/kzzz/atc-cf-bot/internal/domain"
	"github.com/kzzz/atc-cf-bot/internal/source"
	"github.com/rivo/uniseg"
)

const Version = "discord-v2"

func Units(s string) int {
	n := 0
	for _, r := range s {
		n++
		if r > 0xffff {
			n++
		}
	}
	return n
}

type Button struct {
	Type     int    `json:"type"`
	Style    int    `json:"style"`
	Label    string `json:"label"`
	URL      string `json:"url,omitempty"`
	CustomID string `json:"custom_id,omitempty"`
}
type Row struct {
	Type       int      `json:"type"`
	Components []Button `json:"components"`
}
type Mentions struct {
	Parse       []string `json:"parse"`
	RepliedUser bool     `json:"replied_user"`
}
type Attachment struct {
	ID       string `json:"id"`
	Filename string `json:"filename"`
	AssetID  string `json:"asset_id,omitempty"`
}
type Image struct {
	URL string `json:"url"`
}
type Embed struct {
	Image *Image `json:"image,omitempty"`
}
type Payload struct {
	Content         string       `json:"content"`
	AllowedMentions Mentions     `json:"allowed_mentions"`
	Components      []Row        `json:"components"`
	Attachments     []Attachment `json:"attachments"`
	Embeds          []Embed      `json:"embeds"`
	Nonce           string       `json:"nonce,omitempty"`
	EnforceNonce    bool         `json:"enforce_nonce,omitempty"`
	// Channel/thread routing and Forum settings are saved with the frozen payload.
	ChannelID string   `json:"channel_id"`
	Title     string   `json:"title,omitempty"`
	Tags      []string `json:"tags,omitempty"`
}

func NewPayload(text string) Payload {
	return Payload{Content: text, AllowedMentions: Mentions{Parse: []string{}}, Components: []Row{}, Attachments: []Attachment{}, Embeds: []Embed{}}
}

// SplitPlain preserves every byte, preferring newline boundaries. Each grapheme
// (including combining characters and emoji ZWJ sequences) stays intact.
func SplitPlain(text string, budget int) ([]string, error) { return splitProtected(text, budget, nil) }
func splitProtected(text string, budget int, atoms []string) ([]string, error) {
	atoms = append([]string{}, atoms...)
	sort.SliceStable(atoms, func(i, j int) bool { return len(atoms[i]) > len(atoms[j]) })
	if budget < 1 {
		return nil, domain.Fail(domain.Unsupported)
	}
	var chunks []string
	var current strings.Builder
	units := 0
	flush := func() {
		if current.Len() > 0 {
			chunks = append(chunks, current.String())
			current.Reset()
			units = 0
		}
	}
	for len(text) > 0 {
		piece := ""
		for _, a := range atoms {
			if a != "" && strings.HasPrefix(text, a) {
				piece = a
				break
			}
		}
		if piece == "" {
			g := uniseg.NewGraphemes(text)
			if !g.Next() {
				break
			}
			piece = g.Str()
		}
		n := Units(piece)
		if n > budget {
			return nil, domain.Fail(domain.Unsupported)
		}
		if units+n > budget {
			flush()
		}
		current.WriteString(piece)
		units += n
		text = text[len(piece):]
	}
	flush()
	return chunks, nil
}

// SampleParts stores unmodified fragments, so concatenation reconstructs the
// exact input including trailing whitespace and final newline.
func SampleParts(text string, budget int) ([]string, error) {
	var parts []string
	current := ""
	lines := strings.SplitAfter(text, "\n")
	for _, line := range lines {
		if line == "" {
			continue
		}
		if Units(line) > budget {
			if current != "" {
				parts = append(parts, current)
				current = ""
			}
			p, e := SplitPlain(line, budget)
			if e != nil {
				return nil, e
			}
			parts = append(parts, p...)
			continue
		}
		if Units(current)+Units(line) > budget {
			parts = append(parts, current)
			current = ""
		}
		current += line
	}
	if current != "" || len(parts) == 0 {
		parts = append(parts, current)
	}
	return parts, nil
}
func fence(s string) string {
	maxRun, run := 0, 0
	for _, r := range s {
		if r == '`' {
			run++
			if run > maxRun {
				maxRun = run
			}
		} else {
			run = 0
		}
	}
	if maxRun < 3 {
		maxRun = 3
	} else {
		maxRun++
	}
	return strings.Repeat("`", maxRun)
}
func Problem(s domain.Statement, t domain.Translation, ref domain.ProblemRef, pubID, _, _ string, maxMessages int) ([]Payload, error) {
	if e := t.Validate(s); e != nil {
		return nil, e
	}
	var texts []string
	var protected []string
	info := fmt.Sprintf("**%s｜%s %s**\n原題：%s\n", s.Title, ref.Platform, ref.ExternalID, s.URL)
	if ref.Difficulty != nil {
		origin := "Codeforces rating"
		if ref.Platform == domain.AtCoder {
			origin = "AtCoder Problems 原始估計 difficulty"
		}
		info += fmt.Sprintf("難度：%d（%s）\n", *ref.Difficulty, origin)
	}
	if s.TimeLimit != "" {
		info += "時間限制：" + s.TimeLimit + "\n"
	}
	if s.MemoryLimit != "" {
		info += "記憶體限制：" + s.MemoryLimit + "\n"
	}
	info += "AI 翻譯，請以原站題目核對。\n"
	texts = append(texts, info)
	sampleTexts := func(i int, kind, text string) error {
		f := fence(text)
		newline := ""
		if !strings.HasSuffix(text, "\n") {
			newline = "\n"
		}
		texts = append(texts, fmt.Sprintf("**範例 %d %s**\n%stext\n%s%s%s", i+1, kind, f, text, newline, f))
		return nil
	}
	orderedSamples := false
	for _, b := range s.Blocks {
		if b.SampleIndex != nil {
			orderedSamples = true
			i := *b.SampleIndex
			if i < 0 || i >= len(s.Samples) {
				return nil, domain.Fail(domain.Unsupported)
			}
			sample := s.Samples[i]
			kind, text := "輸入", sample.Input
			if b.Kind == "sample_output" {
				kind, text = "輸出", sample.Output
			}
			if e := sampleTexts(i, kind, text); e != nil {
				return nil, e
			}
			continue
		}
		raw := t.Blocks[b.ID]
		restored, atoms, e := source.DisplayBlock(b, raw)
		if e != nil {
			return nil, e
		}
		texts = append(texts, restored)
		protected = append(protected, atoms...)
	}
	if !orderedSamples {
		for i, sample := range s.Samples {
			if e := sampleTexts(i, "輸入", sample.Input); e != nil {
				return nil, e
			}
			if e := sampleTexts(i, "輸出", sample.Output); e != nil {
				return nil, e
			}
		}
	}

	var result []Payload
	parts, e := SplitMarkdown(strings.Join(texts, "\n\n"), 2000, protected)
	if e != nil {
		return nil, e
	}
	for _, text := range parts {
		result = append(result, NewPayload(text))
	}
	for _, a := range s.Assets {
		ext := map[string]string{"image/png": ".png", "image/jpeg": ".jpg", "image/gif": ".gif"}[a.MIME]
		if ext == "" || len(a.Body) == 0 {
			return nil, domain.Fail(domain.Unsupported)
		}
		name := "diagram-" + a.ID + ext
		if len(result[len(result)-1].Attachments) == 10 {
			result = append(result, NewPayload(""))
		}
		p := &result[len(result)-1]
		p.Attachments = append(p.Attachments, Attachment{ID: strconv.Itoa(len(p.Attachments)), Filename: name, AssetID: a.ID})
		p.Embeds = append(p.Embeds, Embed{Image: &Image{URL: "attachment://" + name}})
	}
	if len(result) == 0 || len(result) > maxMessages {
		return nil, domain.Fail(domain.Unsupported)
	}
	for i := range result {
		if Units(result[i].Content) > 2000 {
			return nil, domain.Fail(domain.Unsupported)
		}
	}
	result[len(result)-1].Components = []Row{{Type: 1, Components: []Button{{Type: 2, Style: 5, Label: "前往題目", URL: s.URL}, {Type: 2, Style: 1, Label: "給我提示", CustomID: "v1:hint:" + pubID}, {Type: 2, Style: 2, Label: "給我題解", CustomID: "v1:solution:" + pubID}}}}
	return result, nil
}
