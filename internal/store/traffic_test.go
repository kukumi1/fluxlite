package store

import (
	"context"
	"testing"
)

func TestRecordTrafficFirstReadingOnlyBaselines(t *testing.T) {
	st := openTemp(t)
	ctx := context.Background()
	r := seedRoute(t, st)

	if err := st.RecordTraffic(ctx, r.ID, 0, 5000, 9000, "2026-08-13", true); err != nil {
		t.Fatalf("record: %v", err)
	}

	// Without a previous reading there is no way to tell traffic this panel is
	// accountable for from whatever flowed before it started watching.
	total := trafficOf(t, st, r.ID)
	if total.BytesIn != 0 || total.BytesOut != 0 {
		t.Fatalf("first reading credited traffic: in=%d out=%d", total.BytesIn, total.BytesOut)
	}
}

func TestRecordTrafficAccumulatesGrowth(t *testing.T) {
	st := openTemp(t)
	r := seedRoute(t, st)

	record(t, st, r.ID, 1000, 2000)
	record(t, st, r.ID, 1500, 2200)
	record(t, st, r.ID, 4000, 2900)

	total := trafficOf(t, st, r.ID)
	if total.BytesIn != 3000 {
		t.Fatalf("bytes in = %d, want 3000", total.BytesIn)
	}
	if total.BytesOut != 900 {
		t.Fatalf("bytes out = %d, want 900", total.BytesOut)
	}
}

// A reboot or a firewall flush restarts the kernel counters. The whole of the
// next reading is traffic since that restart: treating it as a negative delta
// would eat the running total, and skipping it would lose everything measured
// until the following poll.
func TestRecordTrafficSurvivesCounterReset(t *testing.T) {
	st := openTemp(t)
	r := seedRoute(t, st)

	record(t, st, r.ID, 1000, 1000)
	record(t, st, r.ID, 9000, 9000) // +8000 each
	record(t, st, r.ID, 700, 300)   // 归零后重新计，本次读数整体计入

	total := trafficOf(t, st, r.ID)
	if total.BytesIn != 8700 {
		t.Fatalf("bytes in = %d, want 8700", total.BytesIn)
	}
	if total.BytesOut != 8300 {
		t.Fatalf("bytes out = %d, want 8300", total.BytesOut)
	}
}

func TestResetTrafficBaselineKeepsTotals(t *testing.T) {
	st := openTemp(t)
	ctx := context.Background()
	r := seedRoute(t, st)

	record(t, st, r.ID, 1000, 1000)
	record(t, st, r.ID, 5000, 5000) // +4000 each

	if err := st.ResetTrafficBaseline(ctx, r.ID); err != nil {
		t.Fatalf("reset baseline: %v", err)
	}

	// Rules were rebuilt, so the counter legitimately starts over. The first
	// reading after that must not be credited a second time.
	record(t, st, r.ID, 6000, 6000)

	total := trafficOf(t, st, r.ID)
	if total.BytesIn != 4000 {
		t.Fatalf("bytes in = %d, want 4000", total.BytesIn)
	}
}

// Only the entry hop feeds the daily buckets: every byte crosses every hop, so
// summing them all would multiply the day's total by the chain's length.
func TestDailyTrafficCountsEntryHopOnly(t *testing.T) {
	st := openTemp(t)
	ctx := context.Background()
	r := seedRoute(t, st)

	for _, hop := range []int{0, 1} {
		if err := st.RecordTraffic(ctx, r.ID, hop, 100, 100, "2026-08-13", hop == 0); err != nil {
			t.Fatalf("baseline hop %d: %v", hop, err)
		}
		if err := st.RecordTraffic(ctx, r.ID, hop, 1100, 1100, "2026-08-13", hop == 0); err != nil {
			t.Fatalf("record hop %d: %v", hop, err)
		}
	}

	daily, err := st.DailyTraffic(ctx, r.ID, 30)
	if err != nil {
		t.Fatalf("daily: %v", err)
	}
	if len(daily) != 1 {
		t.Fatalf("got %d days, want 1", len(daily))
	}
	if daily[0].BytesIn != 1000 {
		t.Fatalf("daily bytes in = %d, want 1000 (entry hop only)", daily[0].BytesIn)
	}
}

func record(t *testing.T, st *Store, routeID int64, in, out uint64) {
	t.Helper()
	if err := st.RecordTraffic(context.Background(), routeID, 0, in, out, "2026-08-13", true); err != nil {
		t.Fatalf("record traffic: %v", err)
	}
}

func trafficOf(t *testing.T, st *Store, routeID int64) struct{ BytesIn, BytesOut int64 } {
	t.Helper()
	all, err := st.RouteTraffic(context.Background())
	if err != nil {
		t.Fatalf("route traffic: %v", err)
	}
	got, ok := all[routeID]
	if !ok {
		t.Fatalf("route %d absent from traffic", routeID)
	}
	return struct{ BytesIn, BytesOut int64 }{got.BytesIn, got.BytesOut}
}
