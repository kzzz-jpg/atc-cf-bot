package prompts

import "embed"

//go:embed *.txt
var FS embed.FS

func Get(kind string) string {
	b, e := FS.ReadFile(kind + ".txt")
	if e != nil {
		panic(e)
	}
	return string(b)
}
