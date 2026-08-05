package applier

import (
	"strings"
	"testing"
)

// Nodes run every libc under the sun, and Alpine is the common case for the
// NAT boxes this panel exists to drive. Only the musl artifacts are statically
// linked and therefore portable; a gnu build fails on Alpine with a bare "not
// found", which reads as a CPU architecture mismatch and is not one.
func TestReleaseAssetsAreStaticMuslBuilds(t *testing.T) {
	for arch, asset := range releaseAsset {
		if !strings.Contains(asset, "-musl") {
			t.Errorf("%s uses %q; only musl builds run on both Alpine and glibc hosts", arch, asset)
		}
		if strings.Contains(asset, "-slim-") {
			t.Errorf("%s uses the slim build %q, which drops the transport feature", arch, asset)
		}
	}
	for _, arch := range []string{"amd64", "arm64"} {
		if _, ok := releaseAsset[arch]; !ok {
			t.Errorf("no release asset mapped for %s", arch)
		}
	}
}

// Switching a version's artifact must invalidate what the panel already
// cached, or upgraded panels keep handing nodes the previous build.
func TestCachePathDistinguishesAssets(t *testing.T) {
	s := NewCachedRealmSource(t.TempDir())

	gnu := s.cachePath("realm-x86_64-unknown-linux-gnu.tar.gz")
	musl := s.cachePath("realm-x86_64-unknown-linux-musl.tar.gz")
	if gnu == musl {
		t.Fatalf("gnu and musl builds share cache path %q", gnu)
	}
	if strings.HasSuffix(musl, ".tar.gz") {
		t.Errorf("cache path %q keeps the archive suffix", musl)
	}
	if !strings.Contains(musl, RealmVersion) {
		t.Errorf("cache path %q does not carry the version", musl)
	}
}
