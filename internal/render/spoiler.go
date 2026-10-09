package render

import "strings"

// SplitSpoiler closes each message's spoiler independently. Escape existing
// delimiters and code formatting, which Discord cannot hide inside spoilers.
func SplitSpoiler(text string, budget int) ([]string, error) {
	text = strings.NewReplacer("\\", "\\\\", "|", "\\|", "`", "\\`").Replace(text)
	parts, err := splitProtected(text, budget-4, []string{"\\\\", "\\|", "\\`"})
	if err != nil {
		return nil, err
	}
	for i := range parts {
		parts[i] = "||" + parts[i] + "||"
	}
	return parts, nil
}
