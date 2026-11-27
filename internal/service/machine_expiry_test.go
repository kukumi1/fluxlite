package service

import (
	"strings"
	"testing"
	"time"

	"github.com/kukumi1/fluxlite/internal/store"
)

func dueAt(name string, at time.Time, auto bool) machineDue {
	return machineDue{key: "me:" + name, nodeID: 7, name: name, billing: store.Billing{ExpiresAt: at, Price: 10, Currency: "¥", CycleDays: 30, AutoRenewal: auto}}
}

// A machine is told about the chosen days ahead, the day before, and once
// expired; a renewal moving its date out is quiet, and the next approach
// is told again.
func TestMachineExpiryReminders(t *testing.T) {
	now := time.Date(2026, 11, 27, 10, 0, 0, 0, trafficZone)
	var tracker levelTracker
	observe := func(at time.Time) []levelEvent {
		return tracker.observe(machineExpiryLevels([]machineDue{dueAt("DATA-TW", at, true)}, now, 7))
	}
	observe(now.AddDate(0, 0, 20))
	if ev := observe(now.AddDate(0, 0, 20)); len(ev) != 0 {
		t.Fatal("far off says nothing", ev)
	}
	ev := observe(time.Date(2026, 12, 2, 0, 0, 0, 0, trafficZone))
	if len(ev) != 1 || ev[0].nodeID != 7 || ev[0].text != "⏰ 机器 7 天内到期：DATA-TW\n到期日 2026-12-02（还剩 5 天） · ¥10/月 · 自动续费" {
		t.Fatalf("%+v", ev)
	}
	if ev := observe(now.Add(20 * time.Hour)); len(ev) != 1 || !strings.HasPrefix(ev[0].text, "⏰ 机器明天到期：DATA-TW") {
		t.Fatalf("%+v", ev)
	}
	if ev := observe(now.Add(-time.Hour)); len(ev) != 1 || !strings.HasPrefix(ev[0].text, "⛔ 机器已到期：DATA-TW") {
		t.Fatalf("%+v", ev)
	}
	if ev := observe(now.AddDate(0, 1, 0)); len(ev) != 0 {
		t.Fatal("a renewal is quiet", ev)
	}
	if ev := observe(now.AddDate(0, 0, 3)); len(ev) != 1 {
		t.Fatal("the next approach is told again", ev)
	}
}

func TestMachinesDueSoon(t *testing.T) {
	now := time.Date(2026, 11, 27, 10, 0, 0, 0, trafficZone)
	due := []machineDue{
		dueAt("DATA-TW", time.Date(2026, 12, 2, 0, 0, 0, 0, time.UTC), true),
		dueAt("Panstar-SG", time.Date(2026, 12, 4, 0, 0, 0, 0, time.UTC), true),
		dueAt("喵喵", time.Date(2027, 9, 24, 0, 0, 0, 0, time.UTC), false),
	}
	got := strings.Join(machinesDueSoon(due, now, 7), "、")
	if got != "DATA-TW（12-02）、Panstar-SG（12-04）" {
		t.Fatal(got)
	}
	if s := defaultAlertSettings(); s.MachineExpiryDays != 7 || !s.Events.MachineExpiry {
		t.Fatal("reminders are on by default, a week ahead")
	}
	bad := defaultAlertSettings()
	bad.MachineExpiryDays = 9
	if bad.validate() == nil {
		t.Fatal("only the offered choices are taken")
	}
}
