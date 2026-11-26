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
	NodeOffline  bool `json:"node_offline"`
	RouteDown    bool `json:"route_down"`
	Quota        bool `json:"quota"`
	Expiry       bool `json:"expiry"`
	Latency      bool `json:"latency"`
	LatencySpike bool `json:"latency_spike"`
	RouteChange  bool `json:"route_change"`
	Blocked      bool `json:"blocked"`
	Daily        bool `json:"daily"`
}

// The events that are about one machine, each with its own list of machines
// left out of it: one powered off on purpose should not page about being
// offline, yet its address being blocked may still matter.
const (
	EventNodeOffline  = "node_offline"
	EventLatency      = "latency"
	EventLatencySpike = "latency_spike"
	EventRouteChange  = "route_change"
	EventBlocked      = "blocked"
)

var machineEvents = []string{EventNodeOffline, EventLatency, EventLatencySpike, EventRouteChange, EventBlocked}

// AlertSettings is what the operator chose on the notification page.
type AlertSettings struct {
	Events AlertEvents `json:"events"`
	// DailyHour is the hour, in UTC+8, the daily summary goes out.
	DailyHour int `json:"daily_hour"`
	// LossPercent is the packet loss to a test point that counts as bad.
	LossPercent int `json:"loss_percent"`
	// LatencyMS, when above zero, also counts a slower average as bad.
	LatencyMS int `json:"latency_ms"`
	// ExcludedNodes lists, per machine event, the machines it is not sent
	// for. A machine added later is included until it is left out.
	ExcludedNodes map[string][]int64 `json:"excluded_nodes"`
	// MutedNodes is the single list settings saved before ExcludedNodes
	// kept; it is read once into the events it used to cover.
	MutedNodes []int64 `json:"muted_nodes,omitempty"`
}

var ErrAlertSettings = errors.New("通知设置无效")

func defaultAlertSettings() AlertSettings {
	return AlertSettings{
		Events: AlertEvents{
			NodeOffline: true, RouteDown: true, Quota: true, Expiry: true,
			Latency: true, LatencySpike: true, RouteChange: true, Blocked: true, Daily: true,
		},
		DailyHour:     9,
		LossPercent:   20,
		ExcludedNodes: map[string][]int64{},
	}
}

func (a AlertSettings) validate() error {
	if a.DailyHour < 0 || a.DailyHour > 23 || a.LossPercent < 1 || a.LossPercent > 100 ||
		a.LatencyMS < 0 || a.LatencyMS > 10000 {
		return ErrAlertSettings
	}
	for event := range a.ExcludedNodes {
		if !slices.Contains(machineEvents, event) {
			return ErrAlertSettings
		}
	}
	return nil
}

// excluded reports whether a machine is left out of an event's messages.
func (a AlertSettings) excluded(event string, nodeID int64) bool {
	return slices.Contains(a.ExcludedNodes[event], nodeID)
}

// normalize carries an old single mute list over to the events it covered
// (blocks were never muted) and leaves every event with a list.
func (a *AlertSettings) normalize() {
	if a.ExcludedNodes == nil {
		a.ExcludedNodes = map[string][]int64{}
		for _, event := range []string{EventNodeOffline, EventLatency, EventLatencySpike, EventRouteChange} {
			a.ExcludedNodes[event] = slices.Clone(a.MutedNodes)
		}
	}
	a.MutedNodes = nil
	for _, event := range machineEvents {
		if a.ExcludedNodes[event] == nil {
			a.ExcludedNodes[event] = []int64{}
		}
	}
}

// AlertSettings returns the saved choices, or the defaults when none were
// saved yet.
func (s *Service) AlertSettings(ctx context.Context) (AlertSettings, error) {
	cfg := defaultAlertSettings()
	raw, ok, err := s.store.Setting(ctx, alertSettingsKey)
	if err == nil && ok {
		cfg.ExcludedNodes = nil
		if err = json.Unmarshal([]byte(raw), &cfg); err != nil {
			cfg = defaultAlertSettings()
		}
	}
	cfg.normalize()
	return cfg, err
}

func (s *Service) SetAlertSettings(ctx context.Context, cfg AlertSettings) error {
	if err := cfg.validate(); err != nil {
		return err
	}
	cfg.normalize()
	raw, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	return s.store.SetSetting(ctx, alertSettingsKey, string(raw))
}
