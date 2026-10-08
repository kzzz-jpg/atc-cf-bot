// Package domain contains platform independent publication policies and ASTs.
package domain

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"regexp"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata"
)

type Code string

const (
	ForbiddenActor Code = "forbidden_actor"
	Unauthorized   Code = "unauthorized"
	Unsupported    Code = "unsupported"
	NoCandidate    Code = "no_candidate"
	Transient      Code = "transient"
	RateLimited    Code = "rate_limited"
	Ambiguous      Code = "ambiguous"
	Conflict       Code = "conflict"
	Invalid        Code = "invalid"
	Configuration  Code = "configuration"
	Truncated      Code = "truncated"
	ContextTooLong Code = "context_too_long"
)

type Error struct {
	Code       Code
	Status     int
	RetryAfter time.Duration
}

func (e *Error) Error() string { return string(e.Code) }
func Fail(c Code) error        { return &Error{Code: c} }
func NewID() string {
	var b [16]byte
	if _, e := rand.Read(b[:]); e != nil {
		panic(e)
	}
	b[6] = (b[6] & 15) | 64
	b[8] = (b[8] & 63) | 128
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:])
}
func Hash(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }
func JSONHash(v any) string {
	b, e := json.Marshal(v)
	if e != nil {
		panic(e)
	}
	return Hash(b)
}

var snowflake = regexp.MustCompile(`^[1-9][0-9]{5,19}$`)

func Snowflake(s string) bool { return snowflake.MatchString(s) }

type Platform string

const (
	AtCoder    Platform = "atcoder"
	Codeforces Platform = "codeforces"
)

type Atom struct {
	Token string `json:"token"`
	Text  string `json:"text"`
}
type Block struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"`
	Text        string `json:"text"`
	Atoms       []Atom `json:"atoms,omitempty"`
	SampleIndex *int   `json:"sample_index,omitempty"`
}
type Sample struct {
	Input  string `json:"input"`
	Output string `json:"output"`
}
type Asset struct {
	ID     string `json:"id"`
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
	MIME   string `json:"mime"`
	Body   []byte `json:"-"`
}
type Statement struct {
	SchemaVersion string   `json:"schema_version"`
	ParserVersion string   `json:"parser_version"`
	Language      string   `json:"language"`
	Title         string   `json:"title"`
	URL           string   `json:"url"`
	TimeLimit     string   `json:"time_limit,omitempty"`
	MemoryLimit   string   `json:"memory_limit,omitempty"`
	Blocks        []Block  `json:"blocks"`
	Samples       []Sample `json:"samples"`
	Assets        []Asset  `json:"assets,omitempty"`
}

func (s Statement) Validate(maxBytes int) error {
	if s.SchemaVersion == "" || s.ParserVersion == "" || s.Title == "" || len(s.Blocks) == 0 || len(s.Samples) == 0 {
		return Fail(Unsupported)
	}
	seen := map[string]bool{}
	kinds := map[string]bool{}
	for _, b := range s.Blocks {
		if b.ID == "" || seen[b.ID] || (b.SampleIndex == nil && strings.TrimSpace(b.Text) == "") {
			return Fail(Unsupported)
		}
		seen[b.ID] = true
		kinds[b.Kind] = true
	}
	if !kinds["statement"] || !kinds["input"] || !kinds["output"] {
		return Fail(Unsupported)
	}
	raw, _ := json.Marshal(s)
	if len(raw) > maxBytes {
		return Fail(Unsupported)
	}
	return nil
}

type ProblemRef struct {
	CatalogSnapshotID string
	ID                string
	Platform          Platform
	ExternalID        string
	URL               string
	ContestID         string
	Difficulty        *int
	ContestEnd        time.Time
	Finished          bool
	Kind              string
	Metadata          json.RawMessage
}
type CatalogSnapshot struct {
	Platform   Platform
	SourceHash string
	Entries    []ProblemRef
}
type Translation struct {
	Blocks map[string]string `json:"blocks"`
}

func (t Translation) Validate(s Statement) error {
	if len(t.Blocks) != len(s.Blocks) {
		return Fail(Unsupported)
	}
	for _, b := range s.Blocks {
		if strings.TrimSpace(t.Blocks[b.ID]) == "" {
			return Fail(Unsupported)
		}
	}
	return nil
}

type Settings struct {
	ForumChannelID     string   `json:"forum_channel_id"`
	ForumTagIDs        []string `json:"forum_tag_ids"`
	Timezone           string   `json:"timezone"`
	PublishTime        string   `json:"publish_time"`
	DailyCount         int      `json:"daily_count"`
	SourceMode         string   `json:"source_mode"`
	AtCoderWeight      int      `json:"atcoder_weight"`
	CodeforcesWeight   int      `json:"codeforces_weight"`
	AtCoderMin         int      `json:"atcoder_min_difficulty"`
	AtCoderMax         int      `json:"atcoder_max_difficulty"`
	CFMin              int      `json:"cf_min_rating"`
	CFMax              int      `json:"cf_max_rating"`
	DedupDays          int      `json:"dedup_days"`
	SolutionLanguage   string   `json:"solution_language"`
	GenerationCooldown int      `json:"generation_cooldown_seconds"`
	ManualCooldown     int      `json:"manual_post_cooldown_seconds"`
	ManualRoleIDs      []string `json:"manual_post_role_ids"`
	AdminLogChannelID  string   `json:"admin_log_channel_id"`
}

func DefaultSettings() Settings {
	return Settings{Timezone: "Asia/Taipei", PublishTime: "09:00", DailyCount: 2, SourceMode: "balanced", AtCoderWeight: 1, CodeforcesWeight: 1, AtCoderMin: 400, AtCoderMax: 1000, CFMin: 800, CFMax: 1400, DedupDays: 90, SolutionLanguage: "cpp17", GenerationCooldown: 30, ManualCooldown: 60, ForumTagIDs: []string{}, ManualRoleIDs: []string{}}
}
func (s Settings) Validate() error {
	if _, e := time.LoadLocation(s.Timezone); e != nil {
		return Fail(Invalid)
	}
	if len(s.PublishTime) != 5 {
		return Fail(Invalid)
	}
	if _, e := time.Parse("15:04", s.PublishTime); e != nil {
		return Fail(Invalid)
	}
	if s.DailyCount < 1 || s.DailyCount > 10 || s.AtCoderMin > s.AtCoderMax || s.CFMin > s.CFMax || s.DedupDays < 0 || s.DedupDays > 36500 || s.GenerationCooldown < 0 || s.ManualCooldown < 0 || s.AtCoderWeight < 1 || s.CodeforcesWeight < 1 || s.AtCoderWeight > 1000000 || s.CodeforcesWeight > 1000000 {
		return Fail(Invalid)
	}
	switch s.SourceMode {
	case "balanced", "atcoder", "codeforces", "weighted":
	default:
		return Fail(Invalid)
	}
	if s.SolutionLanguage != "cpp17" && s.SolutionLanguage != "python3" {
		return Fail(Invalid)
	}
	for _, id := range append(append([]string{}, s.ForumTagIDs...), s.ManualRoleIDs...) {
		if !Snowflake(id) {
			return Fail(Invalid)
		}
	}
	if len(s.ForumTagIDs) > 5 {
		return Fail(Invalid)
	}
	for _, id := range []string{s.ForumChannelID, s.AdminLogChannelID} {
		if id != "" && !Snowflake(id) {
			return Fail(Invalid)
		}
	}
	return nil
}
func (s Settings) Set(key, value string) (Settings, error) {
	b, _ := json.Marshal(s)
	var m map[string]json.RawMessage
	_ = json.Unmarshal(b, &m)
	current, ok := m[key]
	if !ok {
		return s, Fail(Invalid)
	}
	var v any
	switch current[0] {
	case '"':
		v = value
	case '[':
		var ids []string
		if json.Unmarshal([]byte(value), &ids) != nil || ids == nil {
			return s, Fail(Invalid)
		}
		v = ids
	default:
		n, e := strconv.Atoi(value)
		if e != nil {
			return s, Fail(Invalid)
		}
		v = n
	}
	m[key], _ = json.Marshal(v)
	b, _ = json.Marshal(m)
	var next Settings
	if json.Unmarshal(b, &next) != nil {
		return s, Fail(Invalid)
	}
	return next, next.Validate()
}

type RNG interface{ Intn(int) int }
type SecureRNG struct{}

func (SecureRNG) Intn(n int) int {
	v, e := rand.Int(rand.Reader, big.NewInt(int64(n)))
	if e != nil {
		panic(e)
	}
	return int(v.Int64())
}
func (s Settings) Source(date string, slot int, manual bool, r RNG) Platform {
	switch s.SourceMode {
	case "atcoder":
		return AtCoder
	case "codeforces":
		return Codeforces
	case "weighted":
		if r.Intn(s.AtCoderWeight+s.CodeforcesWeight) < s.AtCoderWeight {
			return AtCoder
		}
		return Codeforces
	}
	if manual {
		if slot%2 == 1 {
			return AtCoder
		}
		return Codeforces
	}
	extra := AtCoder
	d, _ := time.Parse("2006-01-02", date)
	if (d.Unix()/86400)%2 != 0 {
		extra = Codeforces
	}
	if slot > s.DailyCount/2*2 {
		return extra
	}
	if slot%2 == 1 {
		return AtCoder
	}
	return Codeforces
}
func (s Settings) Bounds(p Platform) (int, int) {
	if p == AtCoder {
		return s.AtCoderMin, s.AtCoderMax
	}
	return s.CFMin, s.CFMax
}

type Cycle struct {
	Date            string
	Start, End, Due time.Time
}

// CycleAt searches the actual local day, so missing DST times run at the next
// valid instant and repeated local times use the first occurrence.
func CycleAt(now time.Time, s Settings) Cycle {
	loc, _ := time.LoadLocation(s.Timezone)
	local := now.In(loc)
	y, m, d := local.Date()
	start := time.Date(y, m, d, 0, 0, 0, 0, loc)
	end := start.AddDate(0, 0, 1)
	due := end
	for t := start; t.Before(end); t = t.Add(time.Minute) {
		if t.In(loc).Format("15:04") >= s.PublishTime {
			due = t
			break
		}
	}
	return Cycle{Date: local.Format("2006-01-02"), Start: start, End: end, Due: due}
}
