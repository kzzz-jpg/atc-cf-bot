package source

import (
	"github.com/kzzz/atc-cf-bot/internal/domain"
	"net/netip"
	"os"
	"strings"
	"testing"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, e := os.ReadFile("../../testdata/" + name)
	if e != nil {
		t.Fatal(e)
	}
	return b
}
func TestSavedSamplesAndAtoms(t *testing.T) {
	for _, tc := range []struct {
		p         domain.Platform
		file, url string
	}{{domain.AtCoder, "atcoder.html", "https://atcoder.jp/contests/abc001/tasks/abc001_1"}, {domain.Codeforces, "codeforces.html", "https://codeforces.com/problemset/problem/1/A"}} {
		t.Run(string(tc.p), func(t *testing.T) {
			s, e := Parse(tc.p, fixture(t, tc.file), tc.url, 64<<10)
			if e != nil {
				t.Fatal(e)
			}
			if len(s.Samples) != 1 || s.Samples[0].Input != "3\n1 2 3  \n" || s.Samples[0].Output != "6\n" {
				t.Fatalf("samples changed: %#v", s.Samples)
			}
			for _, b := range s.Blocks {
				if _, e = Restore(b, b.Text); e != nil {
					t.Fatal(e)
				}
				for _, a := range b.Atoms {
					if _, e = Restore(b, strings.Replace(b.Text, a.Token, "", 1)); e == nil {
						t.Fatal("missing atom accepted")
					}
					if _, e = Restore(b, b.Text+a.Token); e == nil {
						t.Fatal("duplicate atom accepted")
					}
				}
			}
		})
	}
}
func TestInteractiveAndIncompleteRejected(t *testing.T) {
	raw := string(fixture(t, "codeforces.html"))
	for _, text := range []string{strings.Replace(raw, "Given $$$n$$$ numbers", "This is an interactive problem", 1), strings.ReplaceAll(raw, "output-specification", "unknown-output"), "<html>Cloudflare challenge</html>"} {
		if _, e := Parse(domain.Codeforces, []byte(text), "https://codeforces.com/problemset/problem/1/A", 64<<10); e == nil {
			t.Fatal("unsupported page accepted")
		}
	}
}
func TestPublicIP(t *testing.T) {
	for _, v := range []string{"127.0.0.1", "10.0.0.1", "169.254.169.254", "::1", "fc00::1", "::ffff:192.168.0.1", "100.64.0.1"} {
		if PublicIP(netip.MustParseAddr(v)) {
			t.Errorf("private IP accepted %s", v)
		}
	}
	if !PublicIP(netip.MustParseAddr("1.1.1.1")) {
		t.Fatal("public IP rejected")
	}
}
func TestReadableMath(t *testing.T) {
	for _, tc := range [][2]string{{`1 \leq N \leq 100`, "1 ≤ N ≤ 100"}, {`\frac{a+b}{c}`, "(a+b)/(c)"}, {`\sum_{i=1}^{N} a_i`, "Σ_{i=1}^{N} a_i"}} {
		v, e := MathText(tc[0])
		if e != nil || v != tc[1] {
			t.Fatalf("math %s -> %s (%v)", tc[0], v, e)
		}
	}
	if _, e := MathText(`\unrecognized{critical}`); e == nil {
		t.Fatal("unknown math accepted")
	}
}
func TestSavedOfficialHTMLRegression(t *testing.T) {
	for _, tc := range []struct {
		platform         domain.Platform
		name, url, title string
		samples, assets  int
	}{{domain.AtCoder, "atcoder-official.html", "https://atcoder.jp/contests/abc100/tasks/abc100_a", "A - Happy Birthday!", 3, 2}, {domain.Codeforces, "codeforces-official.html", "https://codeforces.com/problemset/problem/4/A", "A. Watermelon", 1, 0}} {
		s, e := Parse(tc.platform, fixture(t, tc.name), tc.url, 64<<10)
		if e != nil {
			t.Fatal(e)
		}
		if s.Title != tc.title || len(s.Samples) != tc.samples || len(s.Assets) != tc.assets {
			t.Fatalf("official page changed shape: title=%s samples=%d assets=%d", s.Title, len(s.Samples), len(s.Assets))
		}
		for _, b := range s.Blocks {
			if b.SampleIndex == nil {
				if _, _, e = DisplayBlock(b, b.Text); e != nil {
					t.Fatal("formula cannot be displayed", e)
				}
			}
		}
	}
}
