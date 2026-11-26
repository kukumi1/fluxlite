package service

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
)

const alertSettingsKey = "alerts"

// AlertEvents chooses which kinds of change are pushed. Turning one off stops
// its messages, not the tracking behind them, so turning it back on later
// does not replay everything that happened in between.
type AlertEvents struct {
	NodeOffline bool `json:"node_offline"`
	RouteDown   bool `json:"route_down"`
	Quota       bool `json:"quota"`
	Expiry      bool `json:"expiry"`
	Latency     bool `json:"latency"`
	RouteChange bool `json:"route_change"`
	Daily       bool `json:"daily"`
}

// AlertSettings is what the operator chose on the notification page.
type AlertSettings struct {
	Events AlertEvents `json:"events"`
	// DailyHour is the hour, in UTC+8, the daily summary goes out.
	DailyHour int `json:"daily_hour"`
	// LossPercent is the packet loss to a test point that counts as bad.
	LossPercent int `json:"loss_percent"`
	// LatencyMS, when above zero, also counts a slower average as bad.
	LatencyMS int `json:"latency_ms"`
	// MutedNodes get no offline, latency or return-route alerts. A machine
	// the operator powers off on purpose should not page them every time.
	MutedNodes []int64 `json:"muted_nodes"`
}

var ErrAlertSettings = errors.New("通知设置无效")

func defaultAlertSettings() AlertSettings {
	return AlertSettings{
		Events: AlertEvents{
			NodeOffline: true, RouteDown: true, Quota: true, Expiry: true,
			Latency: true, RouteChange: true, Daily: true,
		},
		DailyHour:   9,
		LossPercent: 20,
		MutedNodes:  []int64{},
	}
}

func (a AlertSettings) validate() error {
	if a.DailyHour < 0 || a.DailyHour > 23 || a.LossPercent < 1 || a.LossPercent > 100 ||
		a.LatencyMS < 0 || a.LatencyMS > 10000 {
		return ErrAlertSettings
	}
	return nil
}

func (a AlertSettings) muted(nodeID int64) bool {
	return slices.Contains(a.MutedNodes, nodeID)
}

// AlertSettings returns the saved choices, or the defaults when none were
// saved yet.
func (s *Service) AlertSettings(ctx context.Context) (AlertSettings, error) {
	raw, ok, err := s.store.Setting(ctx, alertSettingsKey)
	if err != nil || !ok {
		return defaultAlertSettings(), err
	}
	cfg := defaultAlertSettings()
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return defaultAlertSettings(), err
	}
	if cfg.MutedNodes == nil {
		cfg.MutedNodes = []int64{}
	}
	return cfg, nil
}

func (s *Service) SetAlertSettings(ctx context.Context, cfg AlertSettings) error {
	if err := cfg.validate(); err != nil {
		return err
	}
	if cfg.MutedNodes == nil {
		cfg.MutedNodes = []int64{}
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	return s.store.SetSetting(ctx, alertSettingsKey, string(raw))
}
