package service

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSubscriptionMemberBudgetsAttemptEveryMember(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	ids := make([]int64, 32)
	for i := range ids {
		ids[i] = int64(i + 1)
	}
	var mu sync.Mutex
	seen := map[int64]bool{}
	err := runSubscriptionMembers(ctx, ids, func(child context.Context, id int64) error {
		mu.Lock()
		seen[id] = true
		mu.Unlock()
		if id <= 8 {
			<-child.Done()
			return child.Err()
		}
		if child.Err() != nil {
			t.Errorf("healthy member %d starved", id)
		}
		return nil
	})
	if err == nil {
		t.Fatal("offline failures ignored")
	}
	if len(seen) != len(ids) {
		t.Fatalf("only %d/%d attempted", len(seen), len(ids))
	}
	if ctx.Err() != nil {
		t.Fatal("first offline batch consumed entire deadline")
	}
}
func TestParseSingBoxCountersRejectsUnknownMeasurements(t *testing.T) {
	line := func(bytes, protocol, direction string) string {
		return fmt.Sprintf("    0 %s %s -- * * 0.0.0.0/0 0.0.0.0/0 /* fluxlite-sb:7:%s */\n", bytes, protocol, direction)
	}
	valid := line("0", "tcp", "in") + line("0", "udp", "in") + line("0", "tcp", "out") + line("0", "udp", "out")
	got, e := parseSingBoxCounters(valid, 7)
	if e != nil || got != [2]int64{} {
		t.Fatalf("measured zero not accepted %v %v", got, e)
	}
	for name, raw := range map[string]string{"missing chain": "", "missing rule": line("0", "tcp", "in"), "duplicate": valid + line("1", "tcp", "in"), "wrong user": strings.ReplaceAll(valid, "sb:7:", "sb:8:"), "overflow": strings.Replace(valid, "0 tcp", "9223372036854775808 tcp", 1)} {
		t.Run(name, func(t *testing.T) {
			if _, e := parseSingBoxCounters(raw, 7); e == nil {
				t.Fatal("unknown measurement accepted as zero")
			}
		})
	}
	measured := line("10", "tcp", "in") + line("20", "udp", "in") + line("30", "tcp", "out") + line("40", "udp", "out")
	got, e = parseSingBoxCounters(measured, 7)
	if e != nil || got != [2]int64{30, 70} {
		t.Fatalf("sum %v %v", got, e)
	}
}
