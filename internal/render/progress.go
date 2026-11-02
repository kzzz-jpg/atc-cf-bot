package render

import (
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/kzzz/atc-cf-bot/internal/domain"
)

// Solve tiles form a cross-colour ramp: Discord emoji have no single-hue
// shades, so increasing counts step through different colours.
func SolveTile(count int) string {
	switch {
	case count <= 0:
		return "⬜"
	case count == 1:
		return "🟩"
	case count == 2:
		return "🟨"
	case count <= 4:
		return "🟧"
	default:
		return "🟥"
	}
}

const solveFutureTile = "⬛"

func parseDate(day string) (time.Time, error) {
	t, e := time.Parse("2006-01-02", day)
	if e != nil {
		return t, domain.Fail(domain.Invalid)
	}
	return t, nil
}

// Heatmap renders one row per week (columns Monday→Sunday), covering the weeks
// ending with today's week. Future days of the running week use a dark tile.
func Heatmap(counts map[string]int, today string, weeks int) (string, error) {
	end, e := parseDate(today)
	if e != nil {
		return "", e
	}
	if weeks < 1 || weeks > 52 {
		return "", domain.Fail(domain.Invalid)
	}
	offset := (int(end.Weekday()) + 6) % 7
	start := end.AddDate(0, 0, -(offset + 7*(weeks-1)))
	var b strings.Builder
	for w := 0; w < weeks; w++ {
		if w > 0 {
			b.WriteString("\n")
		}
		for d := 0; d < 7; d++ {
			day := start.AddDate(0, 0, 7*w+d)
			if day.After(end) {
				b.WriteString(solveFutureTile)
				continue
			}
			b.WriteString(SolveTile(counts[day.Format("2006-01-02")]))
		}
	}
	return b.String(), nil
}

// Streaks returns the current streak (today, or yesterday when today has no
// solve yet) and the longest streak. Counts map entries all mean ≥1 solve.
func Streaks(counts map[string]int, today string) (current, longest int, e error) {
	end, e := parseDate(today)
	if e != nil {
		return 0, 0, e
	}
	days := make([]string, 0, len(counts))
	for day := range counts {
		if day <= today {
			days = append(days, day)
		}
	}
	sort.Strings(days)
	previous, run := time.Time{}, 0
	for _, day := range days {
		d, _ := parseDate(day)
		if !previous.IsZero() && d.Sub(previous) == 24*time.Hour {
			run++
		} else {
			run = 1
		}
		if run > longest {
			longest = run
		}
		previous = d
	}
	cursor := end
	if counts[cursor.Format("2006-01-02")] == 0 {
		cursor = cursor.AddDate(0, 0, -1)
	}
	for counts[cursor.Format("2006-01-02")] > 0 {
		current++
		cursor = cursor.AddDate(0, 0, -1)
	}
	return current, longest, nil
}

func weekCount(counts map[string]int, today string, weeksBack int) (int, error) {
	end, e := parseDate(today)
	if e != nil {
		return 0, e
	}
	offset := (int(end.Weekday()) + 6) % 7
	start := end.AddDate(0, 0, -(offset + 7*weeksBack))
	total := 0
	for d := 0; d < 7; d++ {
		total += counts[start.AddDate(0, 0, d).Format("2006-01-02")]
	}
	return total, nil
}

// ProgressDescription builds the full ephemeral/public progress embed body:
// tile grid, legend, streaks and per-platform totals.
func ProgressDescription(counts map[string]int, total, atcoder, codeforces int, today string, weeks int) (string, error) {
	tiles, e := Heatmap(counts, today, weeks)
	if e != nil {
		return "", e
	}
	current, longest, e := Streaks(counts, today)
	if e != nil {
		return "", e
	}
	thisWeek, e := weekCount(counts, today, 0)
	if e != nil {
		return "", e
	}
	lastWeek, e := weekCount(counts, today, 1)
	if e != nil {
		return "", e
	}
	return strings.Join([]string{
		"近 " + strconv.Itoa(weeks) + " 週解題紀錄（每列一週，左至右為一～日）",
		tiles,
		"圖例：⬜ 0 題・🟩 1 題・🟨 2 題・🟧 3–4 題・🟥 5 題以上・" + solveFutureTile + " 尚未到來",
		"總題數 " + strconv.Itoa(total) + "・目前連續 " + strconv.Itoa(current) + " 天・最長連續 " + strconv.Itoa(longest) + " 天",
		"本週 " + strconv.Itoa(thisWeek) + " 題・上週 " + strconv.Itoa(lastWeek) + " 題",
		"AtCoder " + strconv.Itoa(atcoder) + " 題・Codeforces " + strconv.Itoa(codeforces) + " 題",
	}, "\n"), nil
}
