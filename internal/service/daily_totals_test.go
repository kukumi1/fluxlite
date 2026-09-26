package service

import (
	"context"
	"testing"
	"time"

	"github.com/kukumi1/fluxlite/internal/model"
)

// 仪表盘的趋势图按这个窗口画固定的横轴，所以三件事必须对：窗口长度、切日的时区、
// 以及「那天没人计数」和「那天流量为 0」不能混成一回事。
func TestDailyTotalsFillsWindowAndMarksUnmeasuredDays(t *testing.T) {
	ctx := context.Background()
	s := newReinstallService(t)
	node := seedReinstallNode(t, s)

	var routeIDs []int64
	for i, slug := range []string{"a", "b"} {
		r := &model.Route{
			Name: slug, Slug: slug, Target: "203.0.113.9:443",
			Protocol: model.ProtocolTCP, Enabled: true, EntryPort: 10001 + i,
			Hops: []model.RouteHop{{NodeID: node.ID, HopOrder: 0, RelayPort: 10001 + i}},
		}
		if err := s.store.CreateRoute(ctx, r); err != nil {
			t.Fatalf("建链路: %v", err)
		}
		routeIDs = append(routeIDs, r.ID)
	}

	insert := func(routeID int64, day string, in, out int64) {
		t.Helper()
		if _, err := s.store.DB().ExecContext(ctx,
			`INSERT INTO route_traffic_daily (route_id, day, bytes_in, bytes_out) VALUES (?,?,?,?)`,
			routeID, day, in, out); err != nil {
			t.Fatalf("写入按天流量: %v", err)
		}
	}
	insert(routeIDs[0], "2026-09-27", 100, 1000)
	insert(routeIDs[1], "2026-09-27", 20, 200)
	insert(routeIDs[0], "2026-09-25", 0, 0) // 量到了，就是 0
	insert(routeIDs[0], "2026-09-20", 5, 5) // 窗口之外，不该被算进来

	// 北京时间 9 月 27 日凌晨 1 点，UTC 还停在 26 日。按 UTC 切日的话，
	// 「今天」会被算成 26 号，当天的流量整条丢出窗口。
	now := time.Date(2026, 9, 26, 17, 0, 0, 0, time.UTC)

	got, err := s.dailyTotalsAt(ctx, 3, now)
	if err != nil {
		t.Fatalf("dailyTotalsAt: %v", err)
	}

	want := []model.DailyTotal{
		{Day: "2026-09-25", BytesIn: 0, BytesOut: 0, Counted: true},
		{Day: "2026-09-26", Counted: false},
		{Day: "2026-09-27", BytesIn: 120, BytesOut: 1200, Counted: true},
	}
	if len(got) != len(want) {
		t.Fatalf("窗口长度应为 %d 天，实际 %d: %+v", len(want), len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("第 %d 天\n期望 %+v\n实际 %+v", i, want[i], got[i])
		}
	}
}

func TestDailyTotalsClampsWindow(t *testing.T) {
	s := newReinstallService(t)
	now := time.Date(2026, 9, 26, 17, 0, 0, 0, time.UTC)

	for _, days := range []int{0, -1, 1000} {
		got, err := s.dailyTotalsAt(context.Background(), days, now)
		if err != nil {
			t.Fatalf("days=%d: %v", days, err)
		}
		if len(got) != 14 {
			t.Errorf("days=%d 越界时应退回 14 天，实际 %d 天", days, len(got))
		}
	}
}
