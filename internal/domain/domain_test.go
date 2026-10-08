package domain

import (
	"testing"
	"time"
)

type zeroRNG struct{}

func (zeroRNG) Intn(int) int { return 0 }
func TestSourceQuotas(t *testing.T) {
	s := DefaultSettings()
	for _, count := range []int{1, 2, 3, 10} {
		s.DailyCount = count
		for _, date := range []string{"1970-01-01", "1970-01-02"} {
			a, c := 0, 0
			for i := 1; i <= count; i++ {
				if s.Source(date, i, false, zeroRNG{}) == AtCoder {
					a++
				} else {
					c++
				}
			}
			if count%2 == 0 && a != c {
				t.Fatal("unbalanced quota")
			}
			if count%2 == 1 && date == "1970-01-01" && a != c+1 {
				t.Fatal("wrong extra source")
			}
		}
	}
	if s.Source("2026-10-08", 1, true, zeroRNG{}) != AtCoder || s.Source("2026-10-08", 2, true, zeroRNG{}) != Codeforces {
		t.Fatal("manual alternating source")
	}
}
func TestDSTAndCycles(t *testing.T) {
	s := DefaultSettings()
	s.Timezone = "America/New_York"
	s.PublishTime = "02:30"
	now := time.Date(2026, 3, 8, 12, 0, 0, 0, time.UTC)
	c := CycleAt(now, s)
	if c.End.Sub(c.Start) != 23*time.Hour || c.Due.In(mustLocation(s.Timezone)).Format("15:04") != "03:00" {
		t.Fatalf("missing DST time: %#v", c)
	}
	s.PublishTime = "01:30"
	c = CycleAt(time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC), s)
	if c.End.Sub(c.Start) != 25*time.Hour || c.Due.UTC().Hour() != 5 {
		t.Fatalf("repeated DST time: %#v", c)
	}
}
func mustLocation(s string) *time.Location { l, _ := time.LoadLocation(s); return l }
func TestSettingsRejectAndPreserve(t *testing.T) {
	s := DefaultSettings()
	for _, tc := range [][2]string{{"daily_count", "0"}, {"timezone", "nowhere"}, {"forum_channel_id", "no"}, {"cf_max_rating", "100"}, {"manual_post_role_ids", "null"}, {"llm_base_url", "https://evil.example"}} {
		if _, e := s.Set(tc[0], tc[1]); e == nil {
			t.Fatalf("invalid %v accepted", tc)
		}
	}
	s.AtCoderMin = -1000
	if e := s.Validate(); e != nil {
		t.Fatal("negative raw AtCoder difficulty must be valid")
	}
}
