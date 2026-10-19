package api

import (
	"context"
	"fmt"
	"github.com/kukumi1/fluxlite/internal/model"
	"strings"
	"testing"
)

func TestTelegramTrafficPagingAndUnknown(t *testing.T) {
	s, st := newAuditTestServer(t)
	ctx := context.Background()
	text, pages, err := s.telegramTrafficPage(ctx, 0)
	if err != nil || pages != 1 || !strings.Contains(text, "暂无链路") || !strings.Contains(text, "累计流量：未采集") {
		t.Fatalf("empty: %s %d %v", text, pages, err)
	}
	node := &model.Node{Name: "traffic-host", Host: "192.0.2.1", SSHPort: 22, SSHUser: "root", AuthType: model.AuthPassword, AuthSecret: []byte("test-only"), PortStart: 10000, PortEnd: 20000}
	if err := st.CreateNode(ctx, node); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 7; i++ {
		r := &model.Route{Name: fmt.Sprintf("链路%d", i), Slug: fmt.Sprintf("traffic-%d", i), Target: "192.0.2.5:443", Protocol: model.ProtocolTCP, Enabled: true, EntryPort: 10000 + i, Hops: []model.RouteHop{{NodeID: node.ID, HopOrder: 0, RelayPort: 10000 + i}}}
		if err := st.CreateRoute(ctx, r); err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			if err := st.RecordTraffic(ctx, r.ID, 0, 0, 0, "2026-10-17", true); err != nil {
				t.Fatal(err)
			}
			if err := st.RecordTraffic(ctx, r.ID, 0, 1<<30, 2<<30, "2026-10-17", true); err != nil {
				t.Fatal(err)
			}
			// A middle-hop reading must not double the route's displayed cumulative total.
			if err := st.RecordTraffic(ctx, r.ID, 1, 0, 0, "2026-10-17", false); err != nil {
				t.Fatal(err)
			}
			if err := st.RecordTraffic(ctx, r.ID, 1, 4<<30, 5<<30, "2026-10-17", false); err != nil {
				t.Fatal(err)
			}
		}
		if i == 1 {
			if err := st.RecordTraffic(ctx, r.ID, 1, 0, 0, "2026-10-17", false); err != nil {
				t.Fatal(err)
			}
		}
	}
	text, pages, err = s.telegramTrafficPage(ctx, 0)
	if err != nil || pages != 2 {
		t.Fatal(pages, err)
	}
	for _, want := range []string{"第 1 / 2 页", "入向 1.00 GB · 出向 2.00 GB", "合计 3.00 GB", "中继跳采样", "入向 / 出向 / 合计：未采集", "入向 0 B · 出向 0 B", "链路4"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q: %s", want, text)
		}
	}
	if strings.Contains(text, "链路5") || strings.Contains(text, "合计 12.00 GB") {
		t.Fatalf("paging/double count: %s", text)
	}
	text, pages, err = s.telegramTrafficPage(ctx, 99999)
	if err != nil || pages != 2 || !strings.Contains(text, "第 2 / 2 页") || !strings.Contains(text, "链路6") || strings.Contains(text, "链路4") {
		t.Fatalf("last page: %s %v", text, err)
	}
	text, _, err = s.telegramTrafficPage(ctx, -1)
	if err != nil || !strings.Contains(text, "第 1 / 2 页") {
		t.Fatal(text, err)
	}
}
