package service

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kukumi1/fluxlite/internal/model"
)

// runSubscriptionMembers gives every queued member a share of the deadline.
// One unreachable SSH peer cannot consume the entire enforcement interval.
func runSubscriptionMembers(ctx context.Context, ids []int64, run func(context.Context, int64) error) error {
	if len(ids) == 0 {
		return nil
	}
	workers := min(8, len(ids))
	budget := 5 * time.Second
	if end, ok := ctx.Deadline(); ok {
		share := time.Until(end) * 4 / 5 / time.Duration((len(ids)+workers-1)/workers)
		if share < budget {
			budget = share
		}
	}
	if budget <= 0 {
		return context.DeadlineExceeded
	}
	jobs := make(chan int64)
	errs := make(chan error, len(ids))
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for id := range jobs {
				child, cancel := context.WithTimeout(ctx, budget)
				err := run(child, id)
				cancel()
				if err != nil {
					errs <- err
				}
			}
		}()
	}
	for _, id := range ids {
		jobs <- id
	}
	close(jobs)
	wg.Wait()
	close(errs)
	var failed bool
	for range errs {
		failed = true
	}
	if failed {
		return errors.New("subscription member operation incomplete")
	}
	return nil
}

// CollectSubscriptionTraffic is isolated from Realm and standalone user work.
// Unknown measurements fail closed for finite quotas; they are never zero usage.
func (s *Service) CollectSubscriptionTraffic(ctx context.Context) error {
	all, e := s.store.ListSubscriptions(ctx)
	if e != nil {
		return e
	}
	ids := []int64{}
	for _, v := range all {
		if v.Mode == "collection" || !v.Allowed(time.Now()) {
			continue
		}
		for _, m := range v.Members {
			ids = append(ids, m.UserID)
		}
	}
	return runSubscriptionMembers(ctx, ids, func(sample context.Context, id int64) error {
		u, e := s.store.SingBoxUserByID(sample, id)
		if e == nil {
			var n *model.Node
			n, e = s.store.NodeByID(sample, u.NodeID)
			if e == nil {
				var counters [2]int64
				counters, e = s.readSingBoxCounters(sample, n, id)
				if e == nil {
					e = s.store.RecordSubscriptionCounters(sample, id, counters[0], counters[1])
					if e == nil {
						_, e = s.store.RecordSingBoxTraffic(sample, id, counters[0], counters[1])
					}
				}
			}
		}
		if e != nil {
			persist, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
			defer cancel()
			if err := s.store.FailSubscriptionMeasurement(persist, id); err != nil {
				return errors.New("cannot persist subscription measurement failure")
			}
			return errors.New("subscription measurement unavailable")
		}
		return nil
	})
}

func parseSingBoxCounters(text string, id int64) ([2]int64, error) {
	var out [2]int64
	seen := map[string]bool{}
	for _, line := range strings.Split(text, "\n") {
		m := singBoxCounterLine.FindStringSubmatch(line)
		if len(m) != 4 || m[2] != strconv.FormatInt(id, 10) {
			continue
		}
		protocol := ""
		for _, p := range []string{"tcp", "udp"} {
			for _, field := range strings.Fields(line) {
				if field == p {
					protocol = p
					break
				}
			}
		}
		if protocol == "" {
			return out, errors.New("accounting rule protocol missing")
		}
		key := m[3] + ":" + protocol
		if seen[key] {
			return out, errors.New("duplicate accounting rule")
		}
		seen[key] = true
		b, e := strconv.ParseInt(m[1], 10, 64)
		if e != nil || b < 0 {
			return out, errors.New("invalid accounting counter")
		}
		i := 0
		if m[3] == "out" {
			i = 1
		}
		if b > (1<<63-1)-out[i] {
			return out, errors.New("accounting counter overflow")
		}
		out[i] += b
	}
	if len(seen) != 4 {
		return out, errors.New("accounting rules missing")
	}
	return out, nil
}
