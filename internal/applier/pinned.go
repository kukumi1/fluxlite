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

// pinnedBinary is a diagnostic tool the panel pushes to machines: one
// release, one digest per architecture, downloaded once and kept on disk, so
// machines never reach out to GitHub themselves. Unlike realm, nothing else
// vouches for these builds, so a download or cached copy that does not match
// its digest is refused.
type pinnedBinary struct {
	name    string
	version string
	// asset is the release file name for an architecture.
	asset func(arch string) string
	sums  map[string]string

	dir    string
	client *http.Client
	base   string

	mu    sync.Mutex
	cache map[string][]byte
}

func newPinnedBinary(name, version, base, dir string, asset func(string) string, sums map[string]string) *pinnedBinary {
	return &pinnedBinary{
		name: name, version: version, asset: asset, sums: sums,
		dir:    dir,
		client: &http.Client{Timeout: 5 * time.Minute},
		base:   base,
		cache:  make(map[string][]byte),
	}
}

func (p *pinnedBinary) Version() string { return p.version }

// SHA256 is the digest a machine's copy must match.
func (p *pinnedBinary) SHA256(arch string) (string, bool) {
	sum, ok := p.sums[arch]
	return sum, ok
}

// Binary returns the executable for arch.
func (p *pinnedBinary) Binary(ctx context.Context, arch string) ([]byte, error) {
	want, ok := p.sums[arch]
	if !ok {
		return nil, fmt.Errorf("no %s build for architecture %q", p.name, arch)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if b, ok := p.cache[arch]; ok {
		return b, nil
	}
	asset := p.asset(arch)
	path := filepath.Join(p.dir, p.version+"-"+asset)
	if b, err := os.ReadFile(path); err == nil && digest(b) == want {
		p.cache[arch] = b
		return b, nil
	}

	var lastErr error
	for _, mirror := range downloadMirrors {
		b, err := p.fetch(ctx, mirror+p.base+asset)
		if err == nil && digest(b) != want {
			err = fmt.Errorf("checksum mismatch from %s", mirror+p.base+asset)
		}
		if err != nil {
			lastErr = err
			continue
		}
		if err := os.MkdirAll(p.dir, 0o700); err != nil {
			return nil, fmt.Errorf("create cache dir: %w", err)
		}
		if err := os.WriteFile(path, b, 0o600); err != nil {
			return nil, fmt.Errorf("cache %s binary: %w", p.name, err)
		}
		p.cache[arch] = b
		return b, nil
	}
	return nil, fmt.Errorf("download %s %s: %w", p.name, asset, lastErr)
}

func (p *pinnedBinary) fetch(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	resp, err := p.client.Do(req)
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
