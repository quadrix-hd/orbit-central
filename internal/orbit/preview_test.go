package orbit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPreviewVersionsAssetsAndKeepsRelativeDemoURLs(t *testing.T) {
	t.Chdir(t.TempDir())
	_ = os.MkdirAll("dist", 0700)
	for _, name := range append(append([]string{}, frontendAssets...), "orbit-sans-regular.woff", "orbit-sans-bold.woff", "inter-var.woff2") {
		_ = os.WriteFile(filepath.Join("dist", name), []byte(name), 0600)
	}
	_ = os.WriteFile("dist/index.html", []byte(`<meta name="orbit-mode" content="demo"><link href="/app.css?v=old"><script src="/app.js"></script>`), 0600)
	if e := ExportPreview(); e != nil {
		t.Fatal(e)
	}
	first := string(must(os.ReadFile("dist/index.html")))
	preview := string(must(os.ReadFile("docs/preview/index.html")))
	if strings.Contains(preview, `"/app`) || !strings.Contains(preview, `"./app.css?v=`) || strings.Contains(first, "v=old") {
		t.Fatal(preview)
	}
	_ = os.WriteFile("dist/app.css", []byte("changed"), 0600)
	if e := ExportPreview(); e != nil {
		t.Fatal(e)
	}
	if first == string(must(os.ReadFile("dist/index.html"))) {
		t.Fatal("asset version did not change")
	}
}
