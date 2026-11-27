package komari

import (
	"archive/zip"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func pack(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(body))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestInstallTheme(t *testing.T) {
	site, _, _ := fixture(t)
	ctx := context.Background()
	good := map[string]string{
		"LuminaPlus-1.7/komari-theme.json":    `{"short":"LuminaPlus","version":"1.7.0"}`,
		"LuminaPlus-1.7/dist/index.html":      `<html>new</html>`,
		"LuminaPlus-1.7/dist/assets/app-2.js": `console.log(2)`,
	}
	version, err := site.InstallTheme(ctx, pack(t, good))
	if err != nil || version != "1.7.0" {
		t.Fatal(version, err)
	}
	index, _ := os.ReadFile(filepath.Join(site.themes, "LuminaPlus", "dist", "index.html"))
	if string(index) != "<html>new</html>" {
		t.Fatalf("a package in one folder is unpacked from inside it: %s", index)
	}
	if _, err := os.Stat(filepath.Join(site.themes, "LuminaPlus", "dist", "assets", "app.js")); err == nil {
		t.Fatal("the old version's files are replaced, not mixed in")
	}

	for name, files := range map[string]map[string]string{
		"another theme": {"komari-theme.json": `{"short":"Mikus"}`, "dist/index.html": "x"},
		"no index":      {"komari-theme.json": `{"short":"LuminaPlus"}`},
		"escaping path": {"komari-theme.json": `{"short":"LuminaPlus"}`, "dist/index.html": "x", "../../evil.txt": "x"},
		"not a theme":   {"index.html": "x"},
	} {
		if _, err := site.InstallTheme(ctx, pack(t, files)); err == nil {
			t.Errorf("%s was installed", name)
		}
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(site.themes), "evil.txt")); err == nil {
		t.Fatal("a path out of the theme was written")
	}
	if _, err := site.InstallTheme(ctx, []byte("not a zip")); err == nil || !strings.Contains(err.Error(), "zip") {
		t.Fatal(err)
	}
	index, _ = os.ReadFile(filepath.Join(site.themes, "LuminaPlus", "dist", "index.html"))
	if string(index) != "<html>new</html>" {
		t.Fatal("a refused package leaves the installed theme alone")
	}
}
