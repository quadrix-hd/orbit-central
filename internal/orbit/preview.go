package orbit

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// frontendAssets lists every versioned stylesheet and script in load order.
// The static file allowlist, the preview export and index.html follow it.
var frontendAssets = []string{"app.css", "app.js", "vm-workspace.js", "infrastructure.js", "management.js", "live.js", "operations.js", "bootstrap.js"}

func ExportPreview() error {
	return guarded(func() {
		index := string(must(os.ReadFile(filepath.Join("dist", "index.html"))))
		if !strings.Contains(index, `name="orbit-mode" content="demo"`) {
			fail("Preview benötigt ausdrücklich Demomodus.")
		}
		for _, asset := range frontendAssets {
			sum := sha256.Sum256(must(os.ReadFile(filepath.Join("dist", asset))))
			pattern := regexp.MustCompile(`"/` + regexp.QuoteMeta(asset) + `(?:\?[^"]*)?"`)
			index = pattern.ReplaceAllString(index, `"/`+asset+`?v=`+hex.EncodeToString(sum[:6])+`"`)
		}
		if e := os.WriteFile(filepath.Join("dist", "index.html"), []byte(index), 0644); e != nil {
			panic(e)
		}
		out := filepath.Join("docs", "preview")
		if e := os.MkdirAll(out, 0755); e != nil {
			panic(e)
		}
		for _, name := range append(append([]string{"index.html"}, frontendAssets...), "orbit-sans-regular.woff", "orbit-sans-bold.woff") {
			raw := must(os.ReadFile(filepath.Join("dist", name)))
			if name == "index.html" {
				text := string(raw)
				if !strings.Contains(text, `name="orbit-mode" content="demo"`) {
					fail("Preview benötigt ausdrücklich Demomodus.")
				}
				for _, asset := range frontendAssets {
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
