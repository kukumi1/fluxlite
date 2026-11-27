package service

import (
	"context"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/kukumi1/fluxlite/internal/store"
)

// machineDue is a machine's paid-for time running out: when, for how much,
// and whether it renews by itself.
type machineDue struct {
	key     string
	nodeID  int64
	name    string
	billing store.Billing
}

// machinesDue is every machine with an expiry set on the probe page, named
// as the machine list names it.
func (s *Service) machinesDue(ctx context.Context) ([]machineDue, error) {
	clients, err := s.store.ProbeClients(ctx)
	if err != nil {
		return nil, err
	}
	nodes, err := s.store.ListNodes(ctx)
	if err != nil {
		return nil, err
	}
	names := make(map[int64]string, len(nodes))
	for _, n := range nodes {
		names[n.ID] = n.Name
	}
	var out []machineDue
	for _, c := range clients {
		b, ok := store.BillingOf(c.Display)
		if !ok {
			continue
		}
		d := machineDue{key: "me:" + c.UUID, name: c.Name, billing: b}
		if c.NodeID != nil {
			d.nodeID = *c.NodeID
			if name := names[*c.NodeID]; name != "" {
				d.name = name
			}
		}
		out = append(out, d)
	}
	slices.SortFunc(out, func(a, b machineDue) int { return a.billing.ExpiresAt.Compare(b.billing.ExpiresAt) })
	return out, nil
}

// daysLeft counts whole days to the expiry in UTC+8, as the operator reads
// a date: due tomorrow is one day left, whatever the hour.
func daysLeft(at, now time.Time) int {
	day := func(t time.Time) time.Time {
		t = t.In(trafficZone)
		return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
	}
	return int(math.Round(day(at).Sub(day(now)).Hours() / 24))
}

// cycleText names a billing period as it is sold.
func cycleText(days int) string {
	switch {
	case days >= 28 && days <= 31:
		return "月"
	case days >= 89 && days <= 93:
		return "季"
	case days >= 180 && days <= 186:
		return "半年"
	case days >= 365 && days <= 366:
		return "年"
	case days >= 730 && days <= 731:
		return "两年"
	case days > 0:
		return strconv.Itoa(days) + " 天"
	}
	return ""
}

func (d machineDue) priceText() string {
	var parts []string
	if d.billing.Price > 0 {
		price := strconv.FormatFloat(d.billing.Price, 'f', -1, 64)
		if c := cycleText(d.billing.CycleDays); c != "" {
			price += "/" + c
		}
		parts = append(parts, d.billing.Currency+price)
	}
	if d.billing.AutoRenewal {
		parts = append(parts, "自动续费")
	}
	return strings.Join(parts, " · ")
}

// machineExpiryLevels follows each machine towards its expiry: due within
// the chosen days, due within a day, then expired. A renewal moves the date
// back out, which lowers the level quietly, so the next approach is told
// again.
func machineExpiryLevels(due []machineDue, now time.Time, aheadDays int) []levelSubject {
	subjects := make([]levelSubject, 0, len(due))
	for _, d := range due {
		left := d.billing.ExpiresAt.Sub(now)
		level := 0
		switch {
		case left <= 0:
			level = 3
		case left <= 24*time.Hour:
			level = 2
		case daysLeft(d.billing.ExpiresAt, now) <= aheadDays:
			level = 1
		}
		date := d.billing.ExpiresAt.In(trafficZone).Format("2006-01-02")
		detail := "到期日 " + date
		if days := daysLeft(d.billing.ExpiresAt, now); days > 0 {
			detail += fmt.Sprintf("（还剩 %d 天）", days)
		}
		if p := d.priceText(); p != "" {
			detail += " · " + p
		}
		subjects = append(subjects, levelSubject{
			key: d.key, kind: levelMachineExpiry, level: level, nodeID: d.nodeID,
			texts: map[int]string{
				1: fmt.Sprintf("⏰ 机器 %d 天内到期：%s\n%s", aheadDays, d.name, detail),
				2: fmt.Sprintf("⏰ 机器明天到期：%s\n%s", d.name, detail),
				3: fmt.Sprintf("⛔ 机器已到期：%s\n%s", d.name, detail),
			},
		})
	}
	return subjects
}

// machinesDueSoon names, for the daily summary, the machines due within the
// chosen days, soonest first.
func machinesDueSoon(due []machineDue, now time.Time, aheadDays int) []string {
	var out []string
	for _, d := range due {
		if daysLeft(d.billing.ExpiresAt, now) <= aheadDays {
			out = append(out, fmt.Sprintf("%s（%s）", d.name, d.billing.ExpiresAt.In(trafficZone).Format("01-02")))
		}
	}
	return out
}
