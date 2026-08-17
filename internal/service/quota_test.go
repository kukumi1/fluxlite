package service

import (
	"testing"
	"time"
)

func at(s string) time.Time {
	t, err := time.ParseInLocation("2006-01-02 15:04", s, trafficZone)
	if err != nil {
		panic(err)
	}
	return t
}

func TestQuotaPeriodStart(t *testing.T) {
	cases := []struct {
		name     string
		now      string
		resetDay int
		want     string
	}{
		{"当天正是重置日，周期从今天算起", "2026-08-15 00:30", 15, "2026-08-15"},
		{"重置日之后，周期是本月", "2026-08-20 12:00", 15, "2026-08-15"},
		{"重置日之前，周期还在上个月", "2026-08-03 12:00", 15, "2026-07-15"},
		{"跨年回退", "2026-01-05 12:00", 20, "2025-12-20"},
		{"默认 1 号", "2026-08-13 23:59", 1, "2026-08-01"},
		// 29-31 号在部分月份不存在，越界一律夹到 28
		{"重置日越界夹到 28", "2026-08-30 12:00", 31, "2026-08-28"},
		{"重置日为 0 视为 1 号", "2026-08-13 10:00", 0, "2026-08-01"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := QuotaPeriodStart(at(c.now), c.resetDay); got != c.want {
				t.Fatalf("QuotaPeriodStart(%s, %d) = %s, want %s", c.now, c.resetDay, got, c.want)
			}
		})
	}
}

// 面板机的本地时区不该影响周期切分。日本那台面板跑在 EDT 上，用本地时区切
// 会把北京时间的凌晨算进前一天。
func TestQuotaPeriodStartIgnoresHostTimezone(t *testing.T) {
	// 2026-08-15 01:00 UTC+8 等于 2026-08-14 13:00 EDT
	utc8 := at("2026-08-15 01:00")
	eastern := time.FixedZone("EDT", -4*3600)

	if got := QuotaPeriodStart(utc8.In(eastern), 15); got != "2026-08-15" {
		t.Fatalf("同一时刻换个时区表示就算出了不同周期: %s", got)
	}
}
