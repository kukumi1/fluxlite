package api

import (
	"context"
	"fmt"
	"github.com/kukumi1/fluxlite/internal/model"
	"sort"
	"strings"
)

const telegramTrafficPageSize = 5

// Reuse the panel's entry-hop accounting: never sum every hop of a route.
func (s *Server) telegramTrafficPage(ctx context.Context, page int) (string, int, error) {
	routes, err := s.svc.Store().ListRoutes(ctx)
	if err != nil {
		return "", 0, err
	}
	nodes, err := s.svc.Store().ListNodes(ctx)
	if err != nil {
		return "", 0, err
	}
	users, err := s.svc.Store().ListSingBoxUsers(ctx)
	if err != nil {
		return "", 0, err
	}
	traffic, err := s.svc.Store().RouteTraffic(ctx)
	if err != nil {
		return "", 0, err
	}
	sort.Slice(routes, func(i, j int) bool { return routes[i].ID < routes[j].ID })
	pages := (len(routes) + telegramTrafficPageSize - 1) / telegramTrafficPageSize
	if pages < 1 {
		pages = 1
	}
	if page < 0 {
		page = 0
	}
	if page >= pages {
		page = pages - 1
	}
	var b strings.Builder
	online, enabled, sampled := 0, 0, 0
	for _, n := range nodes {
		if n.Status == model.StatusOnline {
			online++
		}
	}
	var totalIn, totalOut float64
	for _, r := range routes {
		if r.Enabled {
			enabled++
		}
		if v := traffic[r.ID]; v != nil {
			sampled++
			totalIn += float64(v.BytesIn)
			totalOut += float64(v.BytesOut)
		}
	}
	fmt.Fprintf(&b, "Fluxlite 整体仪表盘\n机器在线：%d / %d\n链路启用：%d / %d\n节点总数：%d\n", online, len(nodes), enabled, len(routes), len(users))
	if sampled == 0 {
		b.WriteString("累计流量：未采集\n")
	} else {
		fmt.Fprintf(&b, "累计入向：%s\n累计出向：%s\n累计合计：%s\n", telegramTrafficBytes(totalIn), telegramTrafficBytes(totalOut), telegramTrafficBytes(totalIn+totalOut))
	}
	fmt.Fprintf(&b, "\n每条链路累计用量 · 第 %d / %d 页\n", page+1, pages)
	if len(routes) == 0 {
		b.WriteString("暂无链路。\n")
	}
	start := page * telegramTrafficPageSize
	end := min(start+telegramTrafficPageSize, len(routes))
	for _, r := range routes[start:end] {
		fmt.Fprintf(&b, "\n• #%d %s\n", r.ID, telegramDisplayName(r.Name))
		v := traffic[r.ID]
		if v == nil {
			b.WriteString("  入向 / 出向 / 合计：未采集\n")
			continue
		}
		fmt.Fprintf(&b, "  入向 %s · 出向 %s\n  合计 %s\n", telegramTrafficBytes(float64(v.BytesIn)), telegramTrafficBytes(float64(v.BytesOut)), telegramTrafficBytes(float64(v.BytesIn)+float64(v.BytesOut)))
		if !v.FromEntry {
			b.WriteString("  注：中继跳采样，非入口完整计量\n")
		}
		fmt.Fprintf(&b, "  采样 %s UTC\n", v.UpdatedAt.UTC().Format("01-02 15:04"))
	}
	b.WriteString("\n以上为已采集累计值，不是本月用量；未采集不代表零流量。")
	return b.String(), pages, nil
}

func telegramTrafficBytes(value float64) string {
	if value < 0 {
		return "未知"
	}
	units := []string{"B", "KB", "MB", "GB", "TB", "PB", "EB"}
	i := 0
	for value >= 1024 && i < len(units)-1 {
		value /= 1024
		i++
	}
	if i == 0 {
		return fmt.Sprintf("%.0f %s", value, units[i])
	}
	return fmt.Sprintf("%.2f %s", value, units[i])
}
