package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

type Subscription struct {
	Mode          string               `json:"mode"`
	LinkAvailable bool                 `json:"link_available"`
	ID            int64                `json:"id"`
	Name          string               `json:"name"`
	Enabled       bool                 `json:"enabled"`
	Status        string               `json:"status"`
	QuotaBytes    int64                `json:"quota_bytes"`
	UsedBytes     int64                `json:"used_bytes"`
	ExpiresAt     *time.Time           `json:"expires_at"`
	CreatedAt     time.Time            `json:"created_at"`
	Members       []SubscriptionMember `json:"members"`
}
type SubscriptionMember struct {
	UserID   int64  `json:"user_id"`
	NodeID   int64  `json:"node_id"`
	Protocol string `json:"protocol"`
}

func (v *Subscription) Allowed(now time.Time) bool {
	return v.Enabled && v.Status == "active" && (v.QuotaBytes == 0 || v.UsedBytes < v.QuotaBytes) && (v.ExpiresAt == nil || now.Before(*v.ExpiresAt))
}
func (s *Store) CreateSubscription(ctx context.Context, v *Subscription, hash []byte, secret ...[]byte) error {
	var sealed []byte
	if len(secret) > 0 {
		sealed = secret[0]
	}
	v.Mode = "owned"
	v.LinkAvailable = len(sealed) > 0
	v.CreatedAt = time.Now().UTC()
	r, e := s.db.ExecContext(ctx, `INSERT INTO subscriptions(name,enabled,status,quota_bytes,used_bytes,expires_at,created_at,token_hash,token_secret) VALUES(?,?,?,?,0,?,?,?,?)`, v.Name, v.Enabled, v.Status, v.QuotaBytes, v.ExpiresAt, v.CreatedAt, hash, sealed)
	if e != nil {
		return e
	}
	v.ID, e = r.LastInsertId()
	return e
}
func (s *Store) SubscriptionByID(ctx context.Context, id int64) (*Subscription, error) {
	v := &Subscription{Members: []SubscriptionMember{}}
	var expiry sql.NullTime
	e := s.db.QueryRowContext(ctx, `SELECT id,name,enabled,status,quota_bytes,used_bytes,expires_at,created_at,COALESCE(length(token_secret)>0,0),mode FROM subscriptions WHERE id=?`, id).Scan(&v.ID, &v.Name, &v.Enabled, &v.Status, &v.QuotaBytes, &v.UsedBytes, &expiry, &v.CreatedAt, &v.LinkAvailable, &v.Mode)
	if errors.Is(e, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if e != nil {
		return nil, e
	}
	if expiry.Valid {
		v.ExpiresAt = &expiry.Time
	}
	rows, e := s.db.QueryContext(ctx, `SELECT m.user_id,u.node_id,u.protocol FROM subscription_members m JOIN singbox_users u ON u.id=m.user_id WHERE m.subscription_id=? ORDER BY m.user_id`, id)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	for rows.Next() {
		var m SubscriptionMember
		if e = rows.Scan(&m.UserID, &m.NodeID, &m.Protocol); e != nil {
			return nil, e
		}
		v.Members = append(v.Members, m)
	}
	return v, rows.Err()
}
func (s *Store) ListSubscriptions(ctx context.Context) ([]*Subscription, error) {
	rows, e := s.db.QueryContext(ctx, `SELECT id FROM subscriptions ORDER BY id DESC`)
	if e != nil {
		return nil, e
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if e = rows.Scan(&id); e != nil {
			rows.Close()
			return nil, e
		}
		ids = append(ids, id)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}
	out := []*Subscription{}
	for _, id := range ids {
		v, e := s.SubscriptionByID(ctx, id)
		if e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	return out, nil
}
func (s *Store) SubscriptionByHash(ctx context.Context, hash []byte) (*Subscription, error) {
	var id int64
	e := s.db.QueryRowContext(ctx, `SELECT id FROM subscriptions WHERE token_hash=?`, hash).Scan(&id)
	if e != nil {
		return nil, ErrNotFound
	}
	return s.SubscriptionByID(ctx, id)
}
func (s *Store) UpdateSubscription(ctx context.Context, v *Subscription) error {
	_, e := s.db.ExecContext(ctx, `UPDATE subscriptions SET name=?,enabled=?,status=?,quota_bytes=?,expires_at=? WHERE id=?`, v.Name, v.Enabled, v.Status, v.QuotaBytes, v.ExpiresAt, v.ID)
	return e
}

func (s *Store) RenameSubscription(ctx context.Context, id int64, name string) error {
	r, err := s.db.ExecContext(ctx, `UPDATE subscriptions SET name=? WHERE id=?`, name, id)
	if err != nil {
		return err
	}
	return checkAffected(r, "subscription", id)
}
func (s *Store) ResetSubscriptionToken(ctx context.Context, id int64, hash []byte, secret ...[]byte) error {
	var sealed []byte
	if len(secret) > 0 {
		sealed = secret[0]
	}
	r, e := s.db.ExecContext(ctx, `UPDATE subscriptions SET token_hash=?,token_secret=? WHERE id=?`, hash, sealed, id)
	if e != nil {
		return e
	}
	return checkAffected(r, "subscription", id)
}
func (s *Store) AddSubscriptionMember(ctx context.Context, id int64, m SubscriptionMember) error {
	_, e := s.db.ExecContext(ctx, `INSERT INTO subscription_members(subscription_id,user_id,node_id,protocol) VALUES(?,?,?,?)`, id, m.UserID, m.NodeID, m.Protocol)
	return e
}
func (s *Store) RemoveSubscriptionMember(ctx context.Context, id int64) error {
	_, e := s.db.ExecContext(ctx, `DELETE FROM subscription_members WHERE user_id=?`, id)
	return e
}
func (s *Store) DeleteSubscription(ctx context.Context, id int64) error {
	_, e := s.db.ExecContext(ctx, `DELETE FROM subscriptions WHERE id=?`, id)
	return e
}
func (s *Store) SubscriptionForUser(ctx context.Context, id int64) (*Subscription, error) {
	var parent int64
	e := s.db.QueryRowContext(ctx, `SELECT subscription_id FROM subscription_members WHERE user_id=?`, id).Scan(&parent)
	if errors.Is(e, sql.ErrNoRows) {
		return nil, nil
	}
	if e != nil {
		return nil, e
	}
	return s.SubscriptionByID(ctx, parent)
}

// RecordSubscriptionCounters owns independent raw baselines: monthly child resets cannot reset lifetime usage.
func (s *Store) RecordSubscriptionCounters(ctx context.Context, userID, rawIn, rawOut int64) error {
	if rawIn < 0 || rawOut < 0 {
		return errors.New("negative counters")
	}
	return s.inTx(ctx, func(tx *sql.Tx) error {
		var id, lastIn, lastOut int64
		e := tx.QueryRowContext(ctx, `SELECT m.subscription_id,m.last_in,m.last_out FROM subscription_members m JOIN subscriptions s ON s.id=m.subscription_id WHERE m.user_id=? AND s.mode='owned'`, userID).Scan(&id, &lastIn, &lastOut)
		if errors.Is(e, sql.ErrNoRows) {
			return nil
		}
		if e != nil {
			return e
		}
		di, do := rawIn-lastIn, rawOut-lastOut
		if di < 0 {
			di = rawIn
		}
		if do < 0 {
			do = rawOut
		}
		const max int64 = 1<<63 - 1
		delta := di
		if do > max-delta {
			delta = max
		} else {
			delta += do
		}
		if _, e = tx.ExecContext(ctx, `UPDATE subscriptions SET used_bytes=CASE WHEN used_bytes > ? THEN ? ELSE used_bytes+? END WHERE id=?`, max-delta, max, delta, id); e != nil {
			return e
		}
		_, e = tx.ExecContext(ctx, `UPDATE subscription_members SET last_in=?,last_out=? WHERE user_id=?`, rawIn, rawOut, userID)
		return e
	})
}

// DeleteSubscriptionMemberAndUser removes ownership and its reserved user together,
// only after the caller confirmed remote cleanup. A failed DB write retains both.
func (s *Store) DeleteSubscriptionMemberAndUser(ctx context.Context, id int64) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		if _, e := tx.ExecContext(ctx, `DELETE FROM subscription_members WHERE user_id=?`, id); e != nil {
			return e
		}
		_, e := tx.ExecContext(ctx, `DELETE FROM singbox_users WHERE id=?`, id)
		return e
	})
}

// FailSubscriptionMeasurement revokes exports and future starts before any SSH
// stop is attempted. Unlimited groups do not claim a metered quota guarantee.
func (s *Store) FailSubscriptionMeasurement(ctx context.Context, userID int64) error {
	_, e := s.db.ExecContext(ctx, `UPDATE subscriptions SET enabled=0,status='error' WHERE mode='owned' AND quota_bytes>0 AND id=(SELECT subscription_id FROM subscription_members WHERE user_id=?)`, userID)
	return e
}

// MarkSubscriptionStopped records only a verified transition. Never erase an
// accounting/provisioning error that may have been recorded concurrently.
func (s *Store) MarkSubscriptionStopped(ctx context.Context, id int64) (bool, error) {
	res, e := s.db.ExecContext(ctx, `UPDATE subscriptions SET status='stopped' WHERE id=? AND enabled=0 AND status='stopping'`, id)
	if e != nil {
		return false, e
	}
	n, e := res.RowsAffected()
	return n == 1, e
}

// SubscriptionTokenMaterial is internal credential storage, never a JSON response.
func (s *Store) SubscriptionTokenMaterial(ctx context.Context, id int64) (hash, secret []byte, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT token_hash,token_secret FROM subscriptions WHERE id=?`, id).Scan(&hash, &secret)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrNotFound
	}
	return
}
