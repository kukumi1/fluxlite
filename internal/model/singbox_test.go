package model

import (
	"testing"
	"time"
)

func TestSingBoxQuotaAndRemaining(t *testing.T) {
	now := time.Now().UTC()
	u := &SingBoxUser{NodeID: 1, Name: "a", Protocol: SingBoxSS2022, Port: 1,
		BaseQuotaBytes: 100, TopUpBytes: 20, UsedIn: 70, UsedOut: 40,
		PeriodStartedAt: now, PeriodEndsAt: now.Add(30 * 24 * time.Hour)}
	if got := u.QuotaBytes(); got != 120 {
		t.Fatalf("quota=%d", got)
	}
	if got := u.UsedBytes(); got != 110 {
		t.Fatalf("used=%d", got)
	}
	if got := u.RemainingBytes(); got != 10 {
		t.Fatalf("remaining=%d", got)
	}
}
