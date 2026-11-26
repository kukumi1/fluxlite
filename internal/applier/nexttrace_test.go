package applier

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestNextTraceRejectsWrongDigest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("not the pinned build"))
	}))
	defer srv.Close()

	dir := t.TempDir()
	src := NewCachedNextTraceSource(dir)
	src.base = srv.URL + "/"
	saved := downloadMirrors
	downloadMirrors = []string{""}
	defer func() { downloadMirrors = saved }()

	if _, err := src.Binary(context.Background(), "amd64"); err == nil {
		t.Fatal("accepted a binary that does not match the pinned digest")
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("cached a rejected binary: %v", entries)
	}
	if _, err := src.Binary(context.Background(), "mips"); err == nil {
		t.Fatal("served an architecture with no pinned build")
	}
}

func TestNextTraceIgnoresTamperedCache(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, NextTraceVersion+"-nexttrace-tiny_linux_amd64")
	if err := os.WriteFile(path, []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "gone", http.StatusNotFound)
	}))
	defer srv.Close()
	src := NewCachedNextTraceSource(dir)
	src.base = srv.URL + "/"
	saved := downloadMirrors
	downloadMirrors = []string{""}
	defer func() { downloadMirrors = saved }()

	if _, err := src.Binary(context.Background(), "amd64"); err == nil {
		t.Fatal("served a cached file that fails the digest")
	}
}
