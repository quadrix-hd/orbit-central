package orbit

import (
	"os"
	"path/filepath"
	"strings"
)

func ExportPreview() error {
	return guarded(func() {
		out := filepath.Join("docs", "preview")
		if e := os.MkdirAll(out, 0755); e != nil {
			panic(e)
		}
		for _, name := range []string{"index.html", "app.css", "app.js", "vm-workspace.js", "bootstrap.js", "orbit-sans-regular.woff", "orbit-sans-bold.woff"} {
			raw := must(os.ReadFile(filepath.Join("dist", name)))
			if name == "index.html" {
				text := string(raw)
				if !strings.Contains(text, `name="orbit-mode" content="demo"`) {
					fail("Preview benötigt ausdrücklich Demomodus.")
				}
				for _, asset := range []string{"app.css", "app.js", "vm-workspace.js", "bootstrap.js"} {
					text = strings.ReplaceAll(text, `"/`+asset, `"./`+asset)
				}
				raw = []byte(text)
			}
			if name == "app.css" {
				raw = []byte(strings.ReplaceAll(string(raw), `url("/orbit-sans-`, `url("./orbit-sans-`))
			}
			if e := os.WriteFile(filepath.Join(out, name), raw, 0644); e != nil {
				panic(e)
			}
		}
	})
}
