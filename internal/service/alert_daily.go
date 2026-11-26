package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/kukumi1/fluxlite/internal/model"
)

const dailySentKey = "alerts.daily_sent"

// dailySummary returns the day's summary once, during the configured hour,
// and "" at every other call. The day it went out is stored, so a restart
// within that hour does not send it twice; a panel that is down for the
// whole hour skips that day rather than sending it late.
func (s *Service) dailySummary(ctx context.Context, now time.Time, cfg AlertSettings) (string, error) {
	local := now.In(trafficZone)
	if local.Hour() != cfg.DailyHour {
		return "", nil
	}
	today := local.Format("2006-01-02")
	sent, _, err := s.store.Setting(ctx, dailySentKey)
	if err != nil || sent == today {
		return "", err
	}
	text, err := s.buildDailySummary(ctx, now, cfg)
	if err != nil {
		return "", err
	}
	// Recorded before it is sent: one summary lost to a failed delivery is
	// better than one repeated every ten seconds for an hour.
	if err := s.store.SetSetting(ctx, dailySentKey, today); err != nil {
		return "", err
	}
	return text, nil
}

func (s *Service) buildDailySummary(ctx context.Context, now time.Time, cfg AlertSettings) (string, error) {
	nodes, err := s.store.ListNodes(ctx)
	if err != nil {
		return "", err
	}
	routes, err := s.store.ListRoutes(ctx)
	if err != nil {
		return "", err
	}
	byID := make(map[int64]*model.Node, len(nodes))
	var offline []string
	for _, n := range nodes {
		byID[n.ID] = n
		if n.Status == model.StatusOffline {
			offline = append(offline, n.Name)
		}
	}
	enabled := 0
	var failing []string
	for _, r := range routes {
		if !r.Enabled {
			continue
		}
		enabled++
		if routeSubject(r, byID, now).state == obsFailing {
			failing = append(failing, r.Name)
		}
	}

	var b strings.Builder
	local := now.In(trafficZone)
	fmt.Fprintf(&b, "📊 每日汇总 · %s", local.Format("01-02"))
	fmt.Fprintf(&b, "\n机器：%d / %d 在线", len(nodes)-len(offline), len(nodes))
	if len(offline) > 0 {
		fmt.Fprintf(&b, "（离线：%s）", strings.Join(offline, "、"))
	}
	fmt.Fprintf(&b, "\n链路：%d 条启用", enabled)
	if len(failing) > 0 {
		fmt.Fprintf(&b, "，%d 条异常（%s）", len(failing), strings.Join(failing, "、"))
	} else if enabled > 0 {
		b.WriteString("，全部正常")
	}

	yesterday := local.AddDate(0, 0, -1).Format("2006-01-02")
	totals, err := s.store.DailyTotals(ctx, yesterday)
	if err != nil {
		return "", err
	}
	if d, ok := totals[yesterday]; ok {
		fmt.Fprintf(&b, "\n昨日链路流量：↑ %s ↓ %s", formatBytes(d.BytesIn), formatBytes(d.BytesOut))
	}

	levels, err := s.levelSubjects(ctx, now)
	if err != nil {
		return "", err
	}
	var nearQuota, spent, expiring int
	for _, l := range levels {
		switch {
		case l.kind == levelQuota && l.level == 1:
			nearQuota++
		case l.kind == levelQuota && l.level == 2:
			spent++
		case l.kind == levelExpiry && l.level == 1:
			expiring++
		}
	}
	if nearQuota+spent+expiring > 0 {
		fmt.Fprintf(&b, "\n需要处理：流量将用完 %d、已用完 %d、3 天内到期 %d", nearQuota, spent, expiring)
	}

	if bad, err := s.badLatencyPoints(ctx, now, cfg); err != nil {
		return "", err
	} else if len(bad) > 0 {
		fmt.Fprintf(&b, "\n线路质量异常：%s", strings.Join(bad, "、"))
	}
	return b.String(), nil
}
