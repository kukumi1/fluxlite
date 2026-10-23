package model

import (
	"testing"
	"time"
)

func TestSingBoxRateValidation(t *testing.T) {
	u := &SingBoxUser{NodeID: 1, Name: "node", Protocol: SingBoxSS2022, Port: 10000, BaseQuotaBytes: 0, PeriodStartedAt: time.Now(), PeriodEndsAt: time.Now().Add(time.Hour), PeriodDays: 1}
	if err := u.Validate(); err != nil {
		t.Fatal(err)
	}
	u.UploadLimitBps = 100_000_000_000
	if err := u.Validate(); err != nil {
		t.Fatal(err)
	}
	u.DownloadLimitBps = 100_001_000_000
	if err := u.Validate(); err != ErrSingBoxRate {
		t.Fatalf("got %v, want ErrSingBoxRate", err)
	}
}
