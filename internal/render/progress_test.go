package render

import (
	"strings"
	"testing"
)

func TestSolveTileRamp(t *testing.T) {
	for count, tile := range map[int]string{0: "⬜", 1: "🟩", 2: "🟨", 3: "🟧", 4: "🟧", 5: "🟥", 9: "🟥"} {
		if SolveTile(count) != tile {
			t.Fatalf("tile for %d = %q", count, SolveTile(count))
		}
	}
}

func TestHeatmapWeekRowsAndFutureTile(t *testing.T) {
	// 2026-01-04 is a Sunday, so a one-week grid starts Monday 2025-12-29.
	grid, e := Heatmap(map[string]int{"2026-01-04": 1, "2025-12-30": 2}, "2026-01-04", 1)
	if e != nil {
		t.Fatal(e)
	}
	if grid != "⬜🟨⬜⬜⬜⬜🟩" {
		t.Fatalf("unexpected grid %q", grid)
	}
	grid, e = Heatmap(map[string]int{}, "2026-01-04", 2)
	if e != nil {
		t.Fatal(e)
	}
	if strings.Count(grid, "\n") != 1 || len(strings.Split(grid, "\n")[1]) != len("⬜⬜⬜⬜⬜⬜⬜") {
		t.Fatalf("two-week grid malformed: %q", grid)
	}
	// 2026-01-05 is a Monday: the running week holds six future days.
	grid, e = Heatmap(map[string]int{"2026-01-05": 1}, "2026-01-05", 1)
	if e != nil {
		t.Fatal(e)
	}
	if grid != "🟩⬛⬛⬛⬛⬛⬛" {
		t.Fatalf("future days not dark: %q", grid)
	}
	if _, e = Heatmap(map[string]int{}, "2026-01-04", 0); e == nil {
		t.Fatal("zero weeks accepted")
	}
	if _, e = Heatmap(map[string]int{}, "not-a-date", 1); e == nil {
		t.Fatal("invalid date accepted")
	}
}

func TestStreaks(t *testing.T) {
	// Two runs: 2025-12-25..31 (7 days) and 2026-01-02..04 (3 days).
	counts := map[string]int{}
	for _, day := range []string{"2025-12-25", "2025-12-26", "2025-12-27", "2025-12-28", "2025-12-29", "2025-12-30", "2025-12-31", "2026-01-02", "2026-01-03", "2026-01-04"} {
		counts[day] = 1
	}
	current, longest, e := Streaks(counts, "2026-01-04")
	if e != nil || current != 3 || longest != 7 {
		t.Fatalf("streaks current=%d longest=%d error=%v", current, longest, e)
	}
	// Today still empty: the current streak counts through yesterday.
	current, longest, e = Streaks(map[string]int{"2026-01-03": 1, "2026-01-02": 1}, "2026-01-04")
	if e != nil || current != 2 || longest != 2 {
		t.Fatalf("empty today streaks current=%d longest=%d error=%v", current, longest, e)
	}
	current, longest, e = Streaks(map[string]int{}, "2026-01-04")
	if e != nil || current != 0 || longest != 0 {
		t.Fatalf("empty history streaks current=%d longest=%d error=%v", current, longest, e)
	}
}

func TestProgressDescription(t *testing.T) {
	counts := map[string]int{"2026-11-01": 2, "2026-11-02": 1}
	description, e := ProgressDescription(counts, 3, 2, 1, "2026-11-02", 4)
	if e != nil {
		t.Fatal(e)
	}
	for _, expected := range []string{"近 4 週解題紀錄", "總題數 3", "AtCoder 2 題・Codeforces 1 題", "本週 1 題・上週 2 題"} {
		if !strings.Contains(description, expected) {
			t.Fatalf("missing %q in %q", expected, description)
		}
	}
	if Units(description) > 4096 {
		t.Fatal("description exceeds embed limit")
	}
	if _, e = ProgressDescription(counts, 3, 2, 1, "2026-11-02", 53); e == nil {
		t.Fatal("oversized heatmap accepted")
	}
}
