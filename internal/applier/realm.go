package applier

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// RealmVersion is the realm release fluxlite deploys. Pinning it keeps every
// node on a known build instead of whatever upstream published today.
const RealmVersion = "2.9.4"

// maxBinarySize caps what will be read from a release download, so a wrong
// URL cannot exhaust memory.
const maxBinarySize = 64 << 20

// releaseAsset maps a Go architecture to realm's release artifact name.
var releaseAsset = map[string]string{
	"amd64": "realm-x86_64-unknown-linux-gnu.tar.gz",
	"arm64": "realm-aarch64-unknown-linux-gnu.tar.gz",
}

// downloadMirrors are tried in order. The bare GitHub URL usually fails from
// mainland China hosts, so a proxy prefix is attempted next.
var downloadMirrors = []string{
	"",
	"https://ghfast.top/",
	"https://gh-proxy.com/",
	"https://ghproxy.net/",
}

// RealmSource supplies the realm binary for a target architecture.
type RealmSource interface {
	Binary(ctx context.Context, arch string) ([]byte, error)
	Version() string
}

// CachedRealmSource downloads realm once per architecture and caches it on
// disk. Nodes never contact GitHub themselves, which matters because the
// nodes most likely to need a relay are the ones with the worst connectivity.
type CachedRealmSource struct {
	dir     string
	client  *http.Client
	version string

	mu    sync.Mutex
	cache map[string][]byte
}

func NewCachedRealmSource(dir string) *CachedRealmSource {
	return &CachedRealmSource{
		dir:     dir,
		client:  &http.Client{Timeout: 5 * time.Minute},
		version: RealmVersion,
		cache:   make(map[string][]byte),
	}
}

func (s *CachedRealmSource) Version() string { return s.version }

// Binary returns the realm executable for arch, fetching it if absent.
func (s *CachedRealmSource) Binary(ctx context.Context, arch string) ([]byte, error) {
	asset, ok := releaseAsset[arch]
	if !ok {
		return nil, fmt.Errorf("no realm release for architecture %q", arch)
	}

	s.mu.Lock()
	if b, ok := s.cache[arch]; ok {
		s.mu.Unlock()
		return b, nil
	}
	s.mu.Unlock()

	path := filepath.Join(s.dir, fmt.Sprintf("realm-%s-%s", s.version, arch))
	if b, err := os.ReadFile(path); err == nil && len(b) > 0 {
		s.store(arch, b)
		return b, nil
	}

	bin, err := s.download(ctx, asset)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return nil, fmt.Errorf("create cache dir: %w", err)
	}
	if err := os.WriteFile(path, bin, 0o600); err != nil {
		return nil, fmt.Errorf("cache realm binary: %w", err)
	}
	s.store(arch, bin)
	return bin, nil
}

func (s *CachedRealmSource) store(arch string, b []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cache[arch] = b
}

func (s *CachedRealmSource) download(ctx context.Context, asset string) ([]byte, error) {
	base := fmt.Sprintf("https://github.com/zhboner/realm/releases/download/v%s/%s", s.version, asset)

	var lastErr error
	for _, mirror := range downloadMirrors {
		bin, err := s.tryDownload(ctx, mirror+base)
		if err == nil {
			return bin, nil
		}
		lastErr = err
	}
	return nil, fmt.Errorf("download realm %s: %w", asset, lastErr)
}

func (s *CachedRealmSource) tryDownload(ctx context.Context, url string) ([]byte, error) {
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
	return extractRealm(io.LimitReader(resp.Body, maxBinarySize))
}

// extractRealm pulls the realm executable out of the release tarball.
func extractRealm(r io.Reader) ([]byte, error) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return nil, fmt.Errorf("gzip: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil, fmt.Errorf("realm executable not found in archive")
		}
		if err != nil {
			return nil, fmt.Errorf("read archive: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg || filepath.Base(hdr.Name) != "realm" {
			continue
		}
		bin, err := io.ReadAll(io.LimitReader(tr, maxBinarySize))
		if err != nil {
			return nil, fmt.Errorf("read realm binary: %w", err)
		}
		if len(bin) == 0 {
			return nil, fmt.Errorf("realm binary in archive is empty")
		}
		return bin, nil
	}
}
