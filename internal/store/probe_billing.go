package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// probeTimeLayouts are the ways Komari's database, and the panel since, have
// written a time.
var probeTimeLayouts = []string{time.RFC3339Nano, "2006-01-02 15:04:05.999999999-07:00", "2006-01-02 15:04:05-07:00", "2006-01-02 15:04:05", "2006-01-02"}

// ParseProbeTime reads a time as a probe machine's details hold it.
func ParseProbeTime(s string) (time.Time, bool) {
	for _, layout := range probeTimeLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// Billing is what a probe machine costs and until when, as its public-page
// details say.
type Billing struct {
	ExpiresAt   time.Time
	Price       float64
	Currency    string
	CycleDays   int
	AutoRenewal bool
}

// BillingOf reads a machine's billing; false when no expiry is set.
func BillingOf(display json.RawMessage) (Billing, bool) {
	var d struct {
		ExpiredAt   *string  `json:"expired_at"`
		Price       float64  `json:"price"`
		Currency    string   `json:"currency"`
		Cycle       float64  `json:"billing_cycle"`
		AutoRenewal autoBool `json:"auto_renewal"`
	}
	if json.Unmarshal(display, &d) != nil || d.ExpiredAt == nil {
		return Billing{}, false
	}
	at, ok := ParseProbeTime(*d.ExpiredAt)
	if !ok || at.IsZero() || at.Year() < 2000 {
		return Billing{}, false
	}
	return Billing{ExpiresAt: at, Price: d.Price, Currency: d.Currency, CycleDays: int(d.Cycle), AutoRenewal: bool(d.AutoRenewal)}, true
}

// autoBool reads a flag Komari stored as true/false or as 1/0.
type autoBool bool

func (b *autoBool) UnmarshalJSON(raw []byte) error {
	switch string(raw) {
	case "true", "1":
		*b = true
	default:
		*b = false
	}
	return nil
}

// NextRenewal is a billing period after at: whole months for the cycles
// sold as months and years, so a machine bought on the 2nd stays due on the
// 2nd, and days for anything else.
func NextRenewal(at time.Time, cycleDays int) time.Time {
	switch {
	case cycleDays >= 28 && cycleDays <= 31:
		return at.AddDate(0, 1, 0)
	case cycleDays >= 89 && cycleDays <= 93:
		return at.AddDate(0, 3, 0)
	case cycleDays >= 180 && cycleDays <= 186:
		return at.AddDate(0, 6, 0)
	case cycleDays >= 365 && cycleDays <= 366:
		return at.AddDate(1, 0, 0)
	case cycleDays >= 730 && cycleDays <= 731:
		return at.AddDate(2, 0, 0)
	default:
		return at.AddDate(0, 0, cycleDays)
	}
}

// AdvanceProbeRenewals moves the expiry of every machine set to renew by
// itself past now, a period at a time, as Komari did. Without it a renewed
// machine would read as expired on both pages and in the reminders.
func (s *Store) AdvanceProbeRenewals(ctx context.Context, now time.Time) (int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT uuid, display FROM probe_clients`)
	if err != nil {
		return 0, fmt.Errorf("list probe billing: %w", err)
	}
	type due struct {
		uuid    string
		display json.RawMessage
	}
	var renew []due
	for rows.Next() {
		var uuid, raw string
		if err := rows.Scan(&uuid, &raw); err != nil {
			rows.Close()
			return 0, err
		}
		b, ok := BillingOf(json.RawMessage(raw))
		if !ok || !b.AutoRenewal || b.CycleDays <= 0 || b.ExpiresAt.After(now) {
			continue
		}
		next := b.ExpiresAt
		for !next.After(now) {
			next = NextRenewal(next, b.CycleDays)
		}
		var display map[string]any
		if json.Unmarshal([]byte(raw), &display) != nil {
			continue
		}
		display["expired_at"] = next.UTC().Format(time.RFC3339)
		out, err := json.Marshal(display)
		if err != nil {
			continue
		}
		renew = append(renew, due{uuid, out})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if len(renew) == 0 {
		return 0, nil
	}
	err = s.inTx(ctx, func(tx *sql.Tx) error {
		for _, d := range renew {
			if _, err := tx.ExecContext(ctx, `UPDATE probe_clients SET display = ? WHERE uuid = ?`, string(d.display), d.uuid); err != nil {
				return fmt.Errorf("renew probe machine: %w", err)
			}
		}
		return nil
	})
	return len(renew), err
}
