package service

import (
	"strings"
	"testing"
	"time"

	"github.com/kukumi1/fluxlite/internal/model"
)

func quotaSubject(level int) levelSubject {
	return levelSubject{key: "sbq:1", kind: levelQuota, level: level, texts: map[int]string{1: "90%", 2: "用完"}}
}

func TestLevelTrackerReportsEachClimbOnce(t *testing.T) {
	var tr levelTracker
	if ev := tr.observe([]levelSubject{quotaSubject(1)}); len(ev) != 0 {
		t.Fatal("first pass reported a known state")
	}
	if ev := tr.observe([]levelSubject{quotaSubject(1)}); len(ev) != 0 {
		t.Fatal("same level reported again")
	}
	if ev := tr.observe([]levelSubject{quotaSubject(2)}); len(ev) != 1 || ev[0].text != "用完" {
		t.Fatalf("%+v", ev)
	}
	// A new period: silent going down, reported again on the way up.
	if ev := tr.observe([]levelSubject{quotaSubject(0)}); len(ev) != 0 {
		t.Fatal("reset reported")
	}
	if ev := tr.observe([]levelSubject{quotaSubject(1)}); len(ev) != 1 || ev[0].text != "90%" {
		t.Fatalf("%+v", ev)
	}
}

func TestSingBoxLevels(t *testing.T) {
	now := time.Date(2026, 11, 26, 0, 0, 0, 0, time.UTC)
	base := func() *model.SingBoxUser {
		return &model.SingBoxUser{ID: 1, Name: "TG", Enabled: true, BaseQuotaBytes: 100 << 30}
	}
	level := func(subs []levelSubject, kind levelKind) int {
		for _, s := range subs {
			if s.kind == kind {
				return s.level
			}
		}
		return -1
	}

	u := base()
	u.UsedOut = 91 << 30
	if l := level(singBoxLevels(u, "DMIT-JP", now), levelQuota); l != 1 {
		t.Fatalf("91%% used: level %d", l)
	}
	u.QuotaPausedAt = &now
	u.Enabled = false
	if l := level(singBoxLevels(u, "DMIT-JP", now), levelQuota); l != 2 {
		t.Fatalf("paused: level %d", l)
	}

	u = base()
	u.ExpiresAt = now.Add(48 * time.Hour)
	subs := singBoxLevels(u, "DMIT-JP", now)
	if l := level(subs, levelExpiry); l != 1 {
		t.Fatalf("two days left: level %d", l)
	}
	if !strings.Contains(subs[1].texts[1], "TG（DMIT-JP）") {
		t.Fatalf("%q", subs[1].texts[1])
	}

	u = base()
	u.Enabled = false
	if subs := singBoxLevels(u, "", now); subs != nil {
		t.Fatal("a node stopped by hand reported")
	}
}

func TestDailySummaryOnlyInItsHour(t *testing.T) {
	cfg := defaultAlertSettings()
	s := &Service{}
	at := time.Date(2026, 11, 26, 3, 0, 0, 0, time.UTC) // 11:00 UTC+8
	if text, err := s.dailySummary(t.Context(), at, cfg); err != nil || text != "" {
		t.Fatalf("sent outside its hour: %q %v", text, err)
	}
}
