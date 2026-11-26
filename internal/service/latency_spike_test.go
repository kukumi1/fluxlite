package service

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/kukumi1/fluxlite/internal/model"
)

func TestSpikeVerdict(t *testing.T) {
	online := &model.Node{ID: 3, Name: "V6VPS-HK", Status: model.StatusOnline}
	link := func(target string, family, ms, base int) spikeReading {
		return spikeReading{link: linkKey{3, target, family}, ms: ms, base: base}
	}
	sub := spikeSubject(online, []spikeReading{
		link("广东联通", 6, 182, 69),
		link("北京移动", 6, 186, 42),
		link("上海电信", 4, 70, 20), // 3.5x but only 50ms up: counts
		link("上海联通", 4, 60, 20), // 3x but only 40ms up: does not
		link("广东电信", 4, 130, 80), // 50ms up but under 2x: does not
	})
	if sub.state != obsFailing || len(sub.reasons) != 3 {
		t.Fatalf("%+v", sub)
	}
	if sub.reasons[0] != "北京移动 IPv6：186ms（基线 42ms，+342%）" || !strings.HasPrefix(sub.reasons[2], "广东联通 IPv6：182ms") {
		t.Fatalf("worst first, with the rise: %q", sub.reasons)
	}

	calm := spikeSubject(online, []spikeReading{link("广东联通", 6, 75, 69)})
	if calm.state != obsOK || calm.reasons[0] != "广东联通 IPv6：75ms（基线 69ms）" {
		t.Fatalf("%+v", calm)
	}
	if spikeSubject(online, nil).state != obsUnknown {
		t.Fatal("no baseline yet is unknown, not fine")
	}
	offline := &model.Node{ID: 3, Status: model.StatusOffline}
	if spikeSubject(offline, []spikeReading{link("x", 4, 500, 20)}).state != obsUnknown {
		t.Fatal("an offline machine's last rounds are not a spike")
	}
}

func TestSpikeAndRecoveryMessages(t *testing.T) {
	sub := alertSubject{kind: alertSpike, name: "V6VPS-HK", reasons: []string{"广东联通 IPv6：182ms（基线 69ms，+163%）"}}
	text := alertEventText(alertEvent{subject: sub, failing: true})
	if !strings.HasPrefix(text, "⚠️ 延迟异常暴涨：V6VPS-HK\n触发：连续 3 轮高于历史基线 2 倍（且至少高出 50ms）\n· 广东联通") {
		t.Fatal(text)
	}
	sub.reasons = []string{"广东联通 IPv6：72ms（基线 69ms）"}
	text = alertEventText(alertEvent{subject: sub, lasted: 23 * time.Minute})
	if text != "✅ 延迟恢复正常：V6VPS-HK（持续 23 分钟）\n现在：\n· 广东联通 IPv6：72ms（基线 69ms）" {
		t.Fatal(text)
	}
}

func TestOldMuteListCarriesOver(t *testing.T) {
	cfg := defaultAlertSettings()
	cfg.ExcludedNodes = nil
	if err := json.Unmarshal([]byte(`{"muted_nodes":[5]}`), &cfg); err != nil {
		t.Fatal(err)
	}
	cfg.normalize()
	for _, event := range []string{EventNodeOffline, EventLatency, EventLatencySpike, EventRouteChange} {
		if !cfg.excluded(event, 5) {
			t.Errorf("%s keeps the old mute", event)
		}
	}
	if cfg.excluded(EventBlocked, 5) || cfg.MutedNodes != nil {
		t.Fatalf("blocks were never muted: %+v", cfg)
	}
	if !cfg.Events.LatencySpike {
		t.Fatal("spikes are on for settings saved before they existed")
	}
	bad := defaultAlertSettings()
	bad.ExcludedNodes["daily"] = []int64{1}
	if bad.validate() == nil {
		t.Fatal("only machine events take machines")
	}
}
