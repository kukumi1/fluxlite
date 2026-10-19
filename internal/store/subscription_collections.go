package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

type SubscriptionCandidate struct {
	UserID           int64  `json:"user_id"`
	NodeID           int64  `json:"node_id"`
	Name             string `json:"name"`
	NodeName         string `json:"node_name"`
	Protocol         string `json:"protocol"`
	Port             int    `json:"port"`
	SubscriptionID   *int64 `json:"subscription_id"`
	SubscriptionName string `json:"subscription_name"`
}

func (s *Store) SubscriptionCandidates(ctx context.Context) ([]SubscriptionCandidate, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT u.id,u.node_id,u.name,n.name,u.protocol,u.port,m.subscription_id,COALESCE(s.name,'') FROM singbox_users u JOIN nodes n ON n.id=u.node_id LEFT JOIN subscription_members m ON m.user_id=u.id LEFT JOIN subscriptions s ON s.id=m.subscription_id WHERE u.source='managed' ORDER BY u.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SubscriptionCandidate{}
	for rows.Next() {
		var v SubscriptionCandidate
		if err = rows.Scan(&v.UserID, &v.NodeID, &v.Name, &v.NodeName, &v.Protocol, &v.Port, &v.SubscriptionID, &v.SubscriptionName); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// A single write transaction reserves all selected nodes or none. The primary
// key on user_id is the final exclusivity guard across concurrent requests.
func (s *Store) CreateSubscriptionCollection(ctx context.Context, v *Subscription, ids []int64, hash, secret []byte) error {
	if len(ids) < 1 || len(ids) > 100 {
		return ErrConflict
	}
	v.CreatedAt = time.Now().UTC()
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		r, err := tx.ExecContext(ctx, `INSERT INTO subscriptions(name,enabled,status,quota_bytes,used_bytes,created_at,token_hash,token_secret,mode) VALUES(?,1,'active',0,0,?,?,?,'collection')`, v.Name, v.CreatedAt, hash, secret)
		if err != nil {
			return err
		}
		id, err := r.LastInsertId()
		if err != nil {
			return err
		}
		members := []SubscriptionMember{}
		for _, uid := range ids {
			var m SubscriptionMember
			var source string
			err = tx.QueryRowContext(ctx, `SELECT id,node_id,protocol,source FROM singbox_users WHERE id=?`, uid).Scan(&m.UserID, &m.NodeID, &m.Protocol, &source)
			if errors.Is(err, sql.ErrNoRows) {
				return ErrConflict
			}
			if err != nil {
				return err
			}
			if source != "managed" {
				return ErrConflict
			}
			var occupied int
			if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM subscription_members WHERE user_id=?`, uid).Scan(&occupied); err != nil {
				return err
			}
			if occupied != 0 {
				return ErrConflict
			}
			if _, err = tx.ExecContext(ctx, `INSERT INTO subscription_members(subscription_id,user_id,node_id,protocol) VALUES(?,?,?,?)`, id, uid, m.NodeID, m.Protocol); err != nil {
				return err
			}
			members = append(members, m)
		}
		v.ID = id
		v.Members = members
		return nil
	})
	if err == nil {
		v.Mode = "collection"
		v.Enabled = true
		v.Status = "active"
		v.LinkAvailable = len(secret) > 0
	}
	return err
}
func (s *Store) DeleteSubscriptionCollection(ctx context.Context, id int64) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		var mode string
		if err := tx.QueryRowContext(ctx, `SELECT mode FROM subscriptions WHERE id=?`, id).Scan(&mode); err != nil {
			return err
		}
		if mode != "collection" {
			return ErrConflict
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM subscription_members WHERE subscription_id=?`, id); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `DELETE FROM subscriptions WHERE id=?`, id)
		return err
	})
}
