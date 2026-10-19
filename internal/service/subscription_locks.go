package service

import (
	"context"
	"github.com/kukumi1/fluxlite/internal/store"
	"sync"
	"time"
)

const subscriptionMutationTimeout = 2 * time.Minute

type subscriptionLock struct {
	gate chan struct{}
	refs int
}

func (s *Service) retainSubscriptionLock(id int64) *subscriptionLock {
	s.subscriptionLocksMu.Lock()
	defer s.subscriptionLocksMu.Unlock()
	if s.subscriptionLocks == nil {
		s.subscriptionLocks = make(map[int64]*subscriptionLock)
	}
	l := s.subscriptionLocks[id]
	if l == nil {
		l = &subscriptionLock{gate: make(chan struct{}, 1)}
		s.subscriptionLocks[id] = l
	}
	l.refs++
	return l
}
func (s *Service) dropSubscriptionLock(id int64, l *subscriptionLock) {
	s.subscriptionLocksMu.Lock()
	defer s.subscriptionLocksMu.Unlock()
	l.refs--
	if l.refs == 0 {
		delete(s.subscriptionLocks, id)
	}
}
func (s *Service) releaseSubscriptionLock(id int64, l *subscriptionLock) func() {
	var once sync.Once
	return func() { once.Do(func() { <-l.gate; s.dropSubscriptionLock(id, l) }) }
}
func (s *Service) lockSubscription(ctx context.Context, id int64) (func(), error) {
	l := s.retainSubscriptionLock(id)
	select {
	case l.gate <- struct{}{}:
		return s.releaseSubscriptionLock(id, l), nil
	case <-ctx.Done():
		s.dropSubscriptionLock(id, l)
		return nil, ctx.Err()
	}
}

// Unknown IDs must not reserve a gate for an ID a concurrent INSERT may allocate.
func (s *Service) lockExistingSubscription(ctx context.Context, id int64) (func(), error) {
	if _, e := s.store.SubscriptionByID(ctx, id); e != nil {
		return nil, e
	}
	return s.lockSubscription(ctx, id)
}
func (s *Service) tryLockSubscription(id int64) (func(), bool) {
	l := s.retainSubscriptionLock(id)
	select {
	case l.gate <- struct{}{}:
		return s.releaseSubscriptionLock(id, l), true
	default:
		s.dropSubscriptionLock(id, l)
		return nil, false
	}
}

// Publish the new row and its held keyed lock together. The mutex protects only
// this short local transaction, never provisioning or another network operation.
func (s *Service) reserveSubscription(ctx context.Context, v *store.Subscription, hash []byte, secret []byte) (func(), error) {
	s.subscriptionLocksMu.Lock()
	if e := s.store.CreateSubscription(ctx, v, hash, secret); e != nil {
		s.subscriptionLocksMu.Unlock()
		return nil, e
	}
	if s.subscriptionLocks == nil {
		s.subscriptionLocks = make(map[int64]*subscriptionLock)
	}
	l := &subscriptionLock{gate: make(chan struct{}, 1), refs: 1}
	l.gate <- struct{}{}
	s.subscriptionLocks[v.ID] = l
	s.subscriptionLocksMu.Unlock()
	return s.releaseSubscriptionLock(v.ID, l), nil
}
