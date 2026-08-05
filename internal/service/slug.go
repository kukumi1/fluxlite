package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"unicode"

	"github.com/kukumi1/fluxlite/internal/store"
)

const (
	// maxSlugLen keeps systemd instance names and file paths comfortably short.
	maxSlugLen = 32
	// minSlugBaseLen is the shortest ASCII stem considered recognisable on its
	// own; below it a digest of the display name is appended.
	minSlugBaseLen = 3
)

// makeSlug derives an ASCII-safe identifier from a display name.
//
// The display name may be Chinese, contain spaces or punctuation, or be pure
// emoji. None of that can go into a systemd instance name or an init script
// filename, so anything unusable is dropped and a random suffix guarantees a
// non-empty, unique result.
func makeSlug(ctx context.Context, st *store.Store, displayName string) (string, error) {
	base := slugBase(displayName)

	// A name like "香港中转" reduces to nothing usable, and two different
	// Chinese names would collide on the same base, so uniqueness is resolved
	// against the database rather than assumed.
	candidate := base
	for attempt := 0; attempt < 8; attempt++ {
		free, err := slugAvailable(ctx, st, candidate)
		if err != nil {
			return "", err
		}
		if free {
			return candidate, nil
		}
		suffix, err := randomSuffix()
		if err != nil {
			return "", err
		}
		trimmed := base
		if len(trimmed) > maxSlugLen-len(suffix)-1 {
			trimmed = strings.Trim(trimmed[:maxSlugLen-len(suffix)-1], "-")
		}
		if trimmed == "" {
			trimmed = "route"
		}
		candidate = trimmed + "-" + suffix
	}
	return "", fmt.Errorf("无法为名称 %q 生成唯一标识，请换一个名称", displayName)
}

// slugBase reduces a display name to ASCII letters, digits and hyphens.
// Anything else — Chinese, emoji, spaces, punctuation — becomes a separator,
// because none of it can appear in a systemd instance name.
func slugBase(displayName string) string {
	var b strings.Builder
	lastDash := false
	for _, r := range strings.ToLower(strings.TrimSpace(displayName)) {
		switch {
		case r < unicode.MaxASCII && (unicode.IsLetter(r) || unicode.IsDigit(r)):
			b.WriteRune(r)
			lastDash = false
		case r == '-' || r == '_':
			if !lastDash && b.Len() > 0 {
				b.WriteByte('-')
				lastDash = true
			}
		default:
			// Non-ASCII runes carry no usable ASCII form; treat them as a
			// separator rather than transliterating badly.
			if !lastDash && b.Len() > 0 {
				b.WriteByte('-')
				lastDash = true
			}
		}
		if b.Len() >= maxSlugLen {
			break
		}
	}

	base := strings.Trim(b.String(), "-")
	if len(base) > maxSlugLen {
		base = strings.Trim(base[:maxSlugLen], "-")
	}

	// A mostly non-ASCII name leaves too little to identify anything: "香港中转"
	// reduces to nothing and "日本→台湾 线路A" to a bare "a". A unit called
	// fluxlite-relay@a tells an operator nothing when they are on the box
	// trying to work out which route is broken. Fall back to a digest of the
	// full name, which is stable across renames of other routes and unique
	// enough to be recognisable.
	if len(base) < minSlugBaseLen {
		sum := sha256.Sum256([]byte(strings.TrimSpace(displayName)))
		digest := hex.EncodeToString(sum[:4])
		if base == "" {
			return "route-" + digest
		}
		return base + "-" + digest
	}
	return base
}

func slugAvailable(ctx context.Context, st *store.Store, slug string) (bool, error) {
	_, err := st.RouteBySlug(ctx, slug)
	if err == nil {
		return false, nil
	}
	if errors.Is(err, store.ErrNotFound) {
		return true, nil
	}
	return false, err
}

func randomSuffix() (string, error) {
	buf := make([]byte, 3)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("生成标识后缀失败: %w", err)
	}
	return hex.EncodeToString(buf), nil
}
