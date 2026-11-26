package applier

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// NextTraceVersion is the NextTrace release used to trace return routes. The
// tiny build is the traceroute alone, without the DNS client, web UI and
// geolocation front ends the panel never asks for.
const NextTraceVersion = "1.7.6"

// nextTraceSHA256 pins each build. Unlike realm, this binary is fetched for
// diagnostics on every machine, and nothing else vouches for it.
var nextTraceSHA256 = map[string]string{
	"amd64": "9f219ba91318a3e1534bd28c661ef8019da42825ed48257035c508962d007271",
	"arm64": "aeab456e2f8aebd62cd1dd2d6458d2ec57b38f5d707e196bab41f999ff898810",
}

// CachedNextTraceSource downloads NextTrace once per architecture, checks it
// against the pinned digest and keeps it on disk, so machines never reach
// out to GitHub themselves.
type CachedNextTraceSource struct {
	dir    string
	client *http.Client
	base   string

	mu    sync.Mutex
	cache map[string][]byte
}

func NewCachedNextTraceSource(dir string) *CachedNextTraceSource {
	return &CachedNextTraceSource{
		dir:    dir,
		client: &http.Client{Timeout: 5 * time.Minute},
		base:   "https://github.com/nxtrace/NTrace-core/releases/download/v" + NextTraceVersion + "/",
		cache:  make(map[string][]byte),
	}
}

func (s *CachedNextTraceSource) Version() string { return NextTraceVersion }

// SHA256 is the digest a machine's copy must match.
func (s *CachedNextTraceSource) SHA256(arch string) (string, bool) {
	sum, ok := nextTraceSHA256[arch]
	return sum, ok
}

// Binary returns the NextTrace executable for arch.
func (s *CachedNextTraceSource) Binary(ctx context.Context, arch string) ([]byte, error) {
	want, ok := nextTraceSHA256[arch]
	if !ok {
		return nil, fmt.Errorf("no nexttrace build for architecture %q", arch)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if b, ok := s.cache[arch]; ok {
		return b, nil
	}
	asset := "nexttrace-tiny_linux_" + arch
	path := filepath.Join(s.dir, NextTraceVersion+"-"+asset)
	if b, err := os.ReadFile(path); err == nil && digest(b) == want {
		s.cache[arch] = b
		return b, nil
	}

	var lastErr error
	for _, mirror := range downloadMirrors {
		b, err := s.fetch(ctx, mirror+s.base+asset)
		if err == nil && digest(b) != want {
			err = fmt.Errorf("checksum mismatch from %s", mirror+s.base+asset)
		}
		if err != nil {
			lastErr = err
			continue
		}
		if err := os.MkdirAll(s.dir, 0o700); err != nil {
			return nil, fmt.Errorf("create cache dir: %w", err)
		}
		if err := os.WriteFile(path, b, 0o600); err != nil {
			return nil, fmt.Errorf("cache nexttrace binary: %w", err)
		}
		s.cache[arch] = b
		return b, nil
	}
	return nil, fmt.Errorf("download nexttrace %s: %w", asset, lastErr)
}

func (s *CachedNextTraceSource) fetch(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("get %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("get %s: status %d", url, resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, maxBinarySize))
}

func digest(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
