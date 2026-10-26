package service

import (
	"context"
	"testing"
	"time"

	"github.com/kukumi1/fluxlite/internal/model"
)

func TestNodeGoesOfflineOnlyAfterRepeatedMisses(t *testing.T) {
	s := newReinstallService(t)
	ctx := context.Background()
	n := seedReinstallNode(t, s)
	seen := time.Date(2026, 10, 25, 8, 0, 0, 0, time.UTC)
	if err := s.store.SetNodeStatus(ctx, n.ID, model.StatusOnline, &seen); err != nil {
		t.Fatal(err)
	}

	current := func() *model.Node {
		t.Helper()
		got, err := s.store.NodeByID(ctx, n.ID)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}

	for i := 1; i < offlineAfterMisses; i++ {
		s.noteUnreachable(ctx, current())
		if got := current().Status; got != model.StatusOnline {
			t.Fatalf("after %d misses status = %q, want still online", i, got)
		}
	}
	s.noteUnreachable(ctx, current())
	got := current()
	if got.Status != model.StatusOffline {
		t.Fatalf("after %d misses status = %q, want offline", offlineAfterMisses, got.Status)
	}
	if got.LastSeen == nil || !got.LastSeen.Equal(seen) {
		t.Fatalf("last seen = %v, want it kept at %v", got.LastSeen, seen)
	}

	s.noteReachable(ctx, current())
	got = current()
	if got.Status != model.StatusOnline {
		t.Fatalf("status after answering = %q, want online", got.Status)
	}
	if got.LastSeen == nil || !got.LastSeen.After(seen) {
		t.Fatalf("last seen = %v, want refreshed", got.LastSeen)
	}
}

func TestAnswerResetsTheMissCount(t *testing.T) {
	s := newReinstallService(t)
	ctx := context.Background()
	n := seedReinstallNode(t, s)
	if err := s.store.SetNodeStatus(ctx, n.ID, model.StatusOnline, nil); err != nil {
		t.Fatal(err)
	}
	node, err := s.store.NodeByID(ctx, n.ID)
	if err != nil {
		t.Fatal(err)
	}

	// Two misses, an answer, two more: never three in a row.
	s.noteUnreachable(ctx, node)
	s.noteUnreachable(ctx, node)
	s.noteReachable(ctx, node)
	s.noteUnreachable(ctx, node)
	s.noteUnreachable(ctx, node)

	got, err := s.store.NodeByID(ctx, n.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != model.StatusOnline {
		t.Fatalf("status = %q, want online", got.Status)
	}
}

func TestOfflineIsRecordedAfterTheSampleBudgetIsSpent(t *testing.T) {
	s := newReinstallService(t)
	n := seedReinstallNode(t, s)
	if err := s.store.SetNodeStatus(context.Background(), n.ID, model.StatusOnline, nil); err != nil {
		t.Fatal(err)
	}
	// A dial that timed out leaves the sample's context already expired.
	spent, cancel := context.WithTimeout(context.Background(), -time.Second)
	defer cancel()
	node, err := s.store.NodeByID(context.Background(), n.ID)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < offlineAfterMisses; i++ {
		s.noteUnreachable(spent, node)
	}
	got, err := s.store.NodeByID(context.Background(), n.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != model.StatusOffline {
		t.Fatalf("status = %q, want offline even though the sample context had expired", got.Status)
	}
}
