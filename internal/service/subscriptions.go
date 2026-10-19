package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/kukumi1/fluxlite/internal/model"
	"github.com/kukumi1/fluxlite/internal/store"
	"github.com/kukumi1/fluxlite/internal/subscription"
)

var ErrSubscriptionPolicy = errors.New("subscription policy denies this operation")

type SubscriptionEntry struct {
	Port     int                   `json:"port,omitempty"`
	NodeID   int64                 `json:"node_id"`
	Protocol model.SingBoxProtocol `json:"protocol"`
}
type SubscriptionInput struct {
	Name       string              `json:"name"`
	UserIDs    []int64             `json:"user_ids"`
	Entries    []SubscriptionEntry `json:"entries"`
	QuotaBytes int64               `json:"quota_bytes"`
	ExpiresAt  *time.Time          `json:"expires_at"`
}
type SubscriptionPatch struct {
	Name       *string         `json:"name"`
	Enabled    *bool           `json:"enabled"`
	QuotaBytes *int64          `json:"quota_bytes"`
	ExpiresAt  json.RawMessage `json:"expires_at"`
}

func newSubscriptionToken() (string, []byte, error) {
	b := make([]byte, 32)
	if _, e := rand.Read(b); e != nil {
		return "", nil, e
	}
	t := base64.RawURLEncoding.EncodeToString(b)
	h := sha256.Sum256([]byte(t))
	return t, h[:], nil
}
func (s *Service) ListSubscriptions(ctx context.Context) ([]*store.Subscription, error) {
	return s.store.ListSubscriptions(ctx)
}
func (s *Service) CreateSubscription(ctx context.Context, in SubscriptionInput) (*store.Subscription, string, error) {
	if err := model.ValidateDisplayName(in.Name); err != nil {
		return nil, "", err
	}
	if len(in.UserIDs) < 1 || len(in.UserIDs) > 100 || len(in.Entries) > 0 || in.QuotaBytes != 0 || in.ExpiresAt != nil {
		return nil, "", errors.New("require 1..100 existing node IDs without group policy")
	}
	seen := map[int64]bool{}
	for _, id := range in.UserIDs {
		if id <= 0 || seen[id] {
			return nil, "", errors.New("invalid or duplicate node selection")
		}
		seen[id] = true
	}
	token, hash, err := newSubscriptionToken()
	if err != nil {
		return nil, "", err
	}
	secret, err := s.sealSubscriptionToken(token)
	if err != nil {
		return nil, "", err
	}
	v := &store.Subscription{Name: strings.TrimSpace(in.Name), Mode: "collection", Enabled: true, Status: "active", Members: []store.SubscriptionMember{}}
	if err = s.store.CreateSubscriptionCollection(ctx, v, in.UserIDs, hash, secret); err != nil {
		return nil, "", err
	}
	return v, token, nil
}
func (s *Service) createSubscription(ctx context.Context, in SubscriptionInput, create func(context.Context, SingBoxUserInput) (*model.SingBoxUser, error), cleanupMembers func(context.Context, *store.Subscription) error) (*store.Subscription, string, error) {
	ctx, cancel := context.WithTimeout(ctx, subscriptionMutationTimeout)
	defer cancel()
	var e error
	if e = model.ValidateDisplayName(in.Name); e != nil {
		return nil, "", e
	}
	if len(in.Entries) < 1 || len(in.Entries) > 20 || in.QuotaBytes < 0 {
		return nil, "", errors.New("require 1..20 entries and a nonnegative lifetime quota")
	}
	if in.ExpiresAt != nil && !in.ExpiresAt.After(time.Now()) {
		return nil, "", errors.New("expiry must be in the future")
	}
	ports := make(map[[2]int64]bool)
	for _, entry := range in.Entries {
		if entry.Port < 0 || entry.Port > 65535 {
			return nil, "", errors.New("端口必须为 0（自动）或 1–65535")
		}
		if entry.Port != 0 {
			key := [2]int64{entry.NodeID, int64(entry.Port)}
			if ports[key] {
				return nil, "", errors.New("同一机器不能重复指定相同端口")
			}
			ports[key] = true
		}
		if !entry.Protocol.Valid() {
			return nil, "", model.ErrSingBoxProtocol
		}
		if _, e = s.store.NodeByID(ctx, entry.NodeID); e != nil {
			return nil, "", e
		}
	}
	token, hash, e := newSubscriptionToken()
	if e != nil {
		return nil, "", e
	}
	secret, e := s.sealSubscriptionToken(token)
	if e != nil {
		return nil, "", e
	}
	v := &store.Subscription{Name: strings.TrimSpace(in.Name), Enabled: true, Status: "provisioning", QuotaBytes: in.QuotaBytes, ExpiresAt: in.ExpiresAt, Members: []store.SubscriptionMember{}}
	release, e := s.reserveSubscription(ctx, v, hash, secret)
	if e != nil {
		return nil, "", e
	}
	defer release()
	for i, entry := range in.Entries {
		u, createErr := create(ctx, SingBoxUserInput{subscriptionID: v.ID, NodeID: entry.NodeID, Name: fmt.Sprintf("subscription-%d-%d", v.ID, i+1), Protocol: entry.Protocol, Port: entry.Port})
		if u != nil {
			m := store.SubscriptionMember{UserID: u.ID, NodeID: u.NodeID, Protocol: string(u.Protocol)}
			cleanCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
			owner, ownerErr := s.store.SubscriptionForUser(cleanCtx, u.ID)
			e = ownerErr
			if e == nil && owner == nil {
				e = s.store.AddSubscriptionMember(cleanCtx, v.ID, m)
			} else if e == nil && owner.ID != v.ID {
				e = ErrSubscriptionPolicy
			}
			cancel()
			v.Members = append(v.Members, m)
			if e != nil {
				createErr = e
			}
		}
		if createErr != nil {
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
			defer cancel()
			v.Enabled = false
			v.Status = "error"
			_ = s.store.UpdateSubscription(cleanup, v)
			_ = cleanupMembers(cleanup, v)
			return v, "", errors.New("subscription provisioning failed; inspect retained subscription and retry deletion")
		}
	}
	v.Status = "active"
	if e = s.store.UpdateSubscription(ctx, v); e != nil {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
		defer cancel()
		v.Enabled = false
		v.Status = "error"
		_ = s.store.UpdateSubscription(cleanup, v)
		_ = cleanupMembers(cleanup, v)
		return v, "", errors.New("subscription activation failed; inspect retained subscriptions")
	}
	return v, token, nil
}
func (s *Service) ResetSubscriptionToken(ctx context.Context, id int64) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	release, e := s.lockExistingSubscription(ctx, id)
	if e != nil {
		return "", e
	}
	defer release()
	t, h, e := newSubscriptionToken()
	if e != nil {
		return "", e
	}
	secret, e := s.sealSubscriptionToken(t)
	if e != nil {
		return "", e
	}
	if e = s.store.ResetSubscriptionToken(ctx, id, h, secret); e != nil {
		return "", e
	}
	return t, nil
}
func (s *Service) UpdateSubscription(ctx context.Context, id int64, p SubscriptionPatch) (*store.Subscription, error) {
	ctx, cancel := context.WithTimeout(ctx, subscriptionMutationTimeout)
	defer cancel()
	release, e := s.lockExistingSubscription(ctx, id)
	if e != nil {
		return nil, e
	}
	defer release()
	v, e := s.store.SubscriptionByID(ctx, id)
	if e != nil {
		return nil, e
	}
	if v.Mode == "collection" {
		if p.Name == nil || p.Enabled != nil || p.QuotaBytes != nil || p.ExpiresAt != nil {
			return nil, ErrSubscriptionPolicy
		}
		if e = model.ValidateDisplayName(*p.Name); e != nil {
			return nil, e
		}
		v.Name = strings.TrimSpace(*p.Name)
		if e = s.store.RenameSubscription(ctx, id, v.Name); e != nil {
			return nil, e
		}
		return v, nil
	}
	if p.Name != nil {
		if e = model.ValidateDisplayName(*p.Name); e != nil {
			return nil, e
		}
		v.Name = strings.TrimSpace(*p.Name)
	}
	if p.QuotaBytes != nil {
		if *p.QuotaBytes < 0 {
			return nil, model.ErrSingBoxQuota
		}
		v.QuotaBytes = *p.QuotaBytes
	}
	if p.ExpiresAt != nil {
		var t *time.Time
		if e = json.Unmarshal(p.ExpiresAt, &t); e != nil {
			return nil, errors.New("invalid expiry")
		}
		v.ExpiresAt = t
	}
	if p.Enabled != nil {
		v.Enabled = *p.Enabled
	}
	if v.Enabled {
		if v.Status == "error" || v.Status == "provisioning" {
			return nil, ErrSubscriptionPolicy
		}
		v.Status = "active"
	}
	if !v.Allowed(time.Now()) {
		v.Enabled = false
		v.Status = "stopping"
	}
	if e = s.store.UpdateSubscription(ctx, v); e != nil {
		return nil, e
	}
	if !v.Enabled {
		e = s.stopSubscription(ctx, v)
	} else {
		for _, m := range v.Members {
			u, err := s.store.SingBoxUserByID(ctx, m.UserID)
			if err != nil {
				return v, err
			}
			if !u.Enabled {
				if err = s.setSingBoxEnabled(ctx, u.ID, true); err != nil {
					v.Enabled = false
					v.Status = "error"
					cleanup, cleanupCancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
					_ = s.store.UpdateSubscription(cleanup, v)
					_ = s.stopSubscription(cleanup, v)
					cleanupCancel()
					return v, errors.New("subscription start failed")
				}
			}
		}
	}
	return v, e
}
func (s *Service) stopSubscription(ctx context.Context, v *store.Subscription) error {
	return s.stopSubscriptionWith(ctx, v, func(child context.Context, id int64) error { return s.setSingBoxEnabled(child, id, false) })
}
func (s *Service) stopSubscriptionWith(ctx context.Context, v *store.Subscription, stop func(context.Context, int64) error) error {
	ids := make([]int64, 0, len(v.Members))
	for _, m := range v.Members {
		ids = append(ids, m.UserID)
	}
	if e := runSubscriptionMembers(ctx, ids, stop); e != nil {
		return e
	}
	return s.markSubscriptionStopped(ctx, v)
}
func (s *Service) markSubscriptionStopped(ctx context.Context, v *store.Subscription) error {
	if v.Status == "error" {
		return nil
	}
	changed, e := s.store.MarkSubscriptionStopped(ctx, v.ID)
	if changed {
		v.Status = "stopped"
	}
	return e
}
func (s *Service) deleteSubscriptionMembers(ctx context.Context, v *store.Subscription) error {
	var failed bool
	for _, m := range v.Members {
		u, e := s.store.SingBoxUserByID(ctx, m.UserID)
		if e != nil {
			failed = true
			continue
		}
		n, e := s.store.NodeByID(ctx, u.NodeID)
		if e != nil {
			failed = true
			continue
		}
		if e = s.removeSingBoxUser(ctx, n, u); e != nil {
			// Retry ownership persistence if provisioning had failed during member insertion.
			owner, ownerErr := s.store.SubscriptionForUser(ctx, u.ID)
			if ownerErr == nil && owner == nil {
				_ = s.store.AddSubscriptionMember(ctx, v.ID, m)
			}
			u.Enabled = false
			u.Status = model.SingBoxStatusError
			_ = s.store.UpdateSingBoxUser(ctx, u)
			failed = true
			continue
		}
		if e = s.store.DeleteSubscriptionMemberAndUser(ctx, u.ID); e != nil {
			failed = true
		}
	}
	if failed {
		return errors.New("subscription deletion incomplete; retained members require retry")
	}
	return s.store.DeleteSubscription(ctx, v.ID)
}
func (s *Service) DeleteSubscription(ctx context.Context, id int64) error {
	ctx, cancel := context.WithTimeout(ctx, subscriptionMutationTimeout)
	defer cancel()
	release, e := s.lockExistingSubscription(ctx, id)
	if e != nil {
		return e
	}
	defer release()
	v, e := s.store.SubscriptionByID(ctx, id)
	if e != nil {
		return e
	}
	if v.Mode == "collection" {
		return s.store.DeleteSubscriptionCollection(ctx, id)
	}
	v.Enabled = false
	v.Status = "error"
	if e = s.store.UpdateSubscription(ctx, v); e != nil {
		return e
	}
	return s.deleteSubscriptionMembers(ctx, v)
}
func (s *Service) ReconcileSubscriptions(ctx context.Context) error {
	return s.reconcileSubscriptions(ctx, func(child context.Context, id int64) error { return s.setSingBoxEnabled(child, id, false) })
}
func (s *Service) reconcileSubscriptions(ctx context.Context, stop func(context.Context, int64) error) error {
	ctx, cancel := context.WithTimeout(ctx, subscriptionMutationTimeout)
	defer cancel()
	all, e := s.store.ListSubscriptions(ctx)
	if e != nil {
		return e
	}
	ids := []int64{}
	groups := []*store.Subscription{}
	var failed bool
	// Busy groups are owned by a bounded mutation. Never wait behind unrelated
	// provisioning; after restart no in-memory owner exists so orphaned work stops.
	for _, snapshot := range all {
		if snapshot.Mode == "collection" {
			continue
		}
		release, ok := s.tryLockSubscription(snapshot.ID)
		if !ok {
			failed = true
			continue
		}
		v, e := s.store.SubscriptionByID(ctx, snapshot.ID)
		if e != nil {
			release()
			failed = true
			continue
		}
		if v.Allowed(time.Now()) {
			release()
			continue
		}
		v.Enabled = false
		if v.Status == "provisioning" {
			v.Status = "error"
		}
		if v.Status != "error" {
			v.Status = "stopping"
		}
		if e = s.store.UpdateSubscription(ctx, v); e != nil {
			release()
			failed = true
			continue
		}
		defer release()
		groups = append(groups, v)
		for _, m := range v.Members {
			ids = append(ids, m.UserID)
		}
	}
	var outcomesMu sync.Mutex
	stopped := make(map[int64]bool, len(ids))
	if e = runSubscriptionMembers(ctx, ids, func(child context.Context, id int64) error {
		err := stop(child, id)
		if err == nil {
			outcomesMu.Lock()
			stopped[id] = true
			outcomesMu.Unlock()
		}
		return err
	}); e != nil {
		failed = true
	}
	// Workers have joined. Judge each group independently, not from a global
	// error or Enabled=false: an offline peer must not hide successful stops.
	for _, v := range groups {
		complete := true
		for _, m := range v.Members {
			if !stopped[m.UserID] {
				complete = false
				break
			}
		}
		if complete {
			if e = s.markSubscriptionStopped(ctx, v); e != nil {
				failed = true
			}
		}
	}
	if failed {
		return errors.New("subscription enforcement incomplete")
	}
	return nil
}
func (s *Service) subscriptionStartAllowed(ctx context.Context, id int64) error {
	v, e := s.store.SubscriptionForUser(ctx, id)
	if e != nil {
		return e
	}
	if v != nil && v.Mode != "collection" && !v.Allowed(time.Now()) {
		return ErrSubscriptionPolicy
	}
	return nil
}
func (s *Service) subscriptionIndependentOnly(ctx context.Context, id int64) error {
	v, e := s.store.SubscriptionForUser(ctx, id)
	if e != nil {
		return e
	}
	if v != nil && v.Mode != "collection" {
		return ErrSubscriptionPolicy
	}
	return nil
}
func (s *Service) RenderSubscription(ctx context.Context, id int64, token, format string) (subscription.Result, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var v *store.Subscription
	var e error
	if token != "" {
		if len(token) != 43 {
			return subscription.Result{}, store.ErrNotFound
		}
		h := sha256.Sum256([]byte(token))
		v, e = s.store.SubscriptionByHash(ctx, h[:])
	} else {
		v, e = s.store.SubscriptionByID(ctx, id)
	}
	if e != nil {
		return subscription.Result{}, store.ErrNotFound
	}
	release, lockErr := s.lockSubscription(ctx, v.ID)
	if lockErr != nil {
		return subscription.Result{}, store.ErrNotFound
	}
	defer release()
	// Token reset/revocation may have happened while acquiring the keyed lock.
	if token != "" {
		hash := sha256.Sum256([]byte(token))
		v, e = s.store.SubscriptionByHash(ctx, hash[:])
	} else {
		v, e = s.store.SubscriptionByID(ctx, id)
	}
	if e != nil || !v.Allowed(time.Now()) {
		return subscription.Result{}, store.ErrNotFound
	}
	nodes := []subscription.Node{}
	for _, m := range v.Members {
		u, e := s.store.SingBoxUserByID(ctx, m.UserID)
		if e != nil || !u.Enabled || u.Status == model.SingBoxStatusError || u.QuotaPausedAt != nil || (!u.ExpiresAt.IsZero() && !time.Now().Before(u.ExpiresAt)) {
			if v.Mode == "collection" {
				continue
			}
			return subscription.Result{}, store.ErrNotFound
		}
		n, e := s.store.NodeByID(ctx, u.NodeID)
		if e != nil {
			return subscription.Result{}, store.ErrNotFound
		}
		b, e := s.ExportSingBoxBundle(ctx, m.UserID, strings.Trim(n.Host, "[]"))
		if e != nil {
			return subscription.Result{}, store.ErrNotFound
		}
		link, _ := b["link"].(string)
		config, _ := b["config"].(json.RawMessage)
		if config == nil {
			if raw, ok := b["config"].([]byte); ok {
				config = raw
			}
		}
		nodes = append(nodes, subscription.Node{Name: u.Name + " / " + n.Name + " / " + string(u.Protocol), Link: link, Config: config})
	}
	return subscription.Render(format, nodes)
}

var ErrSubscriptionLinkUnavailable = errors.New("subscription link unavailable")
var ErrSubscriptionLinkCorrupt = errors.New("subscription link cannot be decrypted or verified")

func (s *Service) sealSubscriptionToken(token string) ([]byte, error) {
	if s.sealer == nil {
		return nil, errors.New("subscription encryption unavailable")
	}
	return s.sealer.Seal([]byte(token))
}

// SubscriptionLink returns the original download credential without changing it.
func (s *Service) SubscriptionLink(ctx context.Context, id int64) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	release, err := s.lockExistingSubscription(ctx, id)
	if err != nil {
		return "", err
	}
	defer release()
	hash, secret, err := s.store.SubscriptionTokenMaterial(ctx, id)
	if err != nil {
		return "", err
	}
	if len(secret) == 0 {
		return "", ErrSubscriptionLinkUnavailable
	}
	if s.sealer == nil {
		return "", ErrSubscriptionLinkCorrupt
	}
	plain, err := s.sealer.Open(secret)
	if err != nil {
		return "", ErrSubscriptionLinkCorrupt
	}
	actual := sha256.Sum256(plain)
	if subtle.ConstantTimeCompare(actual[:], hash) != 1 {
		return "", ErrSubscriptionLinkCorrupt
	}
	return string(plain), nil
}

func (s *Service) SubscriptionCandidates(ctx context.Context) ([]store.SubscriptionCandidate, error) {
	return s.store.SubscriptionCandidates(ctx)
}
