package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/kukumi1/fluxlite/internal/model"
)

func (s *Store) CreateSingBoxUser(ctx context.Context, u *model.SingBoxUser) error {
	if err := u.Validate(); err != nil {
		return err
	}
	now := time.Now().UTC()
	u.CreatedAt, u.UpdatedAt = now, now
	if u.Status == "" {
		u.Status = model.SingBoxStatusUnknown
	}
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO singbox_users (node_id,name,protocol,port,enabled,source,status,
			service_name,config_path,external_path,config_blob,base_quota_bytes,
			top_up_bytes,used_in,used_out,raw_in,raw_out,period_started_at,
			period_ends_at,expires_at,quota_paused_at,created_at,updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		u.NodeID, u.Name, u.Protocol, u.Port, u.Enabled, u.Source, u.Status,
		u.ServiceName, u.ConfigPath, u.ExternalPath, u.ConfigBlob, u.BaseQuotaBytes,
		u.TopUpBytes, u.UsedIn, u.UsedOut, u.RawIn, u.RawOut, u.PeriodStartedAt,
		u.PeriodEndsAt, nullableExpiry(u.ExpiresAt), u.QuotaPausedAt, u.CreatedAt, u.UpdatedAt)
	if err != nil {
		if isUniqueViolation(err) {
			return fmt.Errorf("sing-box name or port already exists: %w", ErrConflict)
		}
		return fmt.Errorf("insert sing-box user: %w", err)
	}
	u.ID, err = res.LastInsertId()
	return err
}

func (s *Store) SingBoxUserByID(ctx context.Context, id int64) (*model.SingBoxUser, error) {
	row := s.db.QueryRowContext(ctx, singBoxSelect+` WHERE id = ?`, id)
	u, err := scanSingBoxUser(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("sing-box user %d: %w", id, ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("scan sing-box user: %w", err)
	}
	return u, nil
}

func (s *Store) SingBoxUserByNodePort(ctx context.Context, nodeID int64, port int) (*model.SingBoxUser, error) {
	row := s.db.QueryRowContext(ctx, singBoxSelect+` WHERE node_id=? AND port=?`, nodeID, port)
	u, err := scanSingBoxUser(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("sing-box user on %d:%d: %w", nodeID, port, ErrNotFound)
	}
	return u, err
}

func (s *Store) ListSingBoxUsers(ctx context.Context) ([]*model.SingBoxUser, error) {
	rows, err := s.db.QueryContext(ctx, singBoxSelect+` ORDER BY name, id`)
	if err != nil {
		return nil, fmt.Errorf("list sing-box users: %w", err)
	}
	defer rows.Close()
	var out []*model.SingBoxUser
	for rows.Next() {
		u, err := scanSingBoxUser(rows)
		if err != nil {
			return nil, fmt.Errorf("scan sing-box user: %w", err)
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func (s *Store) ListSingBoxUsersOnNode(ctx context.Context, nodeID int64) ([]*model.SingBoxUser, error) {
	rows, err := s.db.QueryContext(ctx, singBoxSelect+` WHERE node_id=? ORDER BY id`, nodeID)
	if err != nil {
		return nil, fmt.Errorf("list sing-box users on node: %w", err)
	}
	defer rows.Close()
	var out []*model.SingBoxUser
	for rows.Next() {
		u, err := scanSingBoxUser(rows)
		if err != nil {
			return nil, fmt.Errorf("scan sing-box user on node: %w", err)
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

const singBoxSelect = `SELECT id,node_id,name,protocol,port,enabled,source,status,
	service_name,config_path,external_path,config_blob,base_quota_bytes,top_up_bytes,
	used_in,used_out,raw_in,raw_out,period_started_at,period_ends_at,expires_at,quota_paused_at,
	created_at,updated_at FROM singbox_users`

type rowScanner interface{ Scan(...any) error }

func scanSingBoxUser(row rowScanner) (*model.SingBoxUser, error) {
	u := &model.SingBoxUser{}
	var expires sql.NullTime
	err := row.Scan(&u.ID, &u.NodeID, &u.Name, &u.Protocol, &u.Port, &u.Enabled,
		&u.Source, &u.Status, &u.ServiceName, &u.ConfigPath, &u.ExternalPath,
		&u.ConfigBlob, &u.BaseQuotaBytes, &u.TopUpBytes, &u.UsedIn, &u.UsedOut,
		&u.RawIn, &u.RawOut, &u.PeriodStartedAt, &u.PeriodEndsAt, &expires, &u.QuotaPausedAt,
		&u.CreatedAt, &u.UpdatedAt)
	if err == nil {
		if expires.Valid {
			u.ExpiresAt = expires.Time
		} else {
			u.ExpiresAt = u.CreatedAt.Add(365 * 24 * time.Hour)
		}
	}
	return u, err
}

func (s *Store) UpdateSingBoxUser(ctx context.Context, u *model.SingBoxUser) error {
	if err := u.Validate(); err != nil {
		return err
	}
	u.UpdatedAt = time.Now().UTC()
	res, err := s.db.ExecContext(ctx, `UPDATE singbox_users SET name=?, protocol=?, port=?,
		enabled=?, source=?, status=?, service_name=?, config_path=?, external_path=?,
		config_blob=?, base_quota_bytes=?, top_up_bytes=?, used_in=?, used_out=?, raw_in=?, raw_out=?, period_started_at=?,
		period_ends_at=?, expires_at=?, quota_paused_at=?, updated_at=? WHERE id=?`,
		u.Name, u.Protocol, u.Port, u.Enabled, u.Source, u.Status, u.ServiceName,
		u.ConfigPath, u.ExternalPath, u.ConfigBlob, u.BaseQuotaBytes, u.TopUpBytes, u.UsedIn, u.UsedOut, u.RawIn, u.RawOut,
		u.PeriodStartedAt, u.PeriodEndsAt, nullableExpiry(u.ExpiresAt), u.QuotaPausedAt, u.UpdatedAt, u.ID)
	if err != nil {
		if isUniqueViolation(err) {
			return fmt.Errorf("sing-box name or port already exists: %w", ErrConflict)
		}
		return fmt.Errorf("update sing-box user: %w", err)
	}
	return checkAffected(res, "sing-box user", u.ID)
}

func (s *Store) DeleteSingBoxUser(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM singbox_users WHERE id=?`, id)
	if err != nil {
		return fmt.Errorf("delete sing-box user: %w", err)
	}
	return checkAffected(res, "sing-box user", id)
}

func (s *Store) AddSingBoxTopUp(ctx context.Context, id, bytes int64) error {
	if bytes <= 0 {
		return model.ErrSingBoxQuota
	}
	res, err := s.db.ExecContext(ctx, `UPDATE singbox_users SET top_up_bytes=top_up_bytes+?, updated_at=? WHERE id=?`, bytes, time.Now().UTC(), id)
	if err != nil {
		return err
	}
	return checkAffected(res, "sing-box user", id)
}

func (s *Store) ResetSingBoxUsage(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `UPDATE singbox_users SET used_in=0,used_out=0,raw_in=0,raw_out=0,quota_paused_at=NULL,updated_at=? WHERE id=?`, time.Now().UTC(), id)
	if err != nil {
		return err
	}
	return checkAffected(res, "sing-box user", id)
}

func (s *Store) RollSingBoxPeriod(ctx context.Context, u *model.SingBoxUser, now time.Time) error {
	for !now.Before(u.PeriodEndsAt) {
		u.PeriodStartedAt = u.PeriodEndsAt
		u.PeriodEndsAt = u.PeriodEndsAt.Add(30 * 24 * time.Hour)
	}
	u.TopUpBytes, u.UsedIn, u.UsedOut, u.RawIn, u.RawOut = 0, 0, 0, 0, 0
	u.QuotaPausedAt = nil
	return s.UpdateSingBoxUser(ctx, u)
}

func (s *Store) RecordSingBoxTraffic(ctx context.Context, id int64, rawIn, rawOut int64) (*model.SingBoxUser, error) {
	u, err := s.SingBoxUserByID(ctx, id)
	if err != nil {
		return nil, err
	}
	deltaIn, deltaOut := rawIn-u.RawIn, rawOut-u.RawOut
	if u.RawIn == 0 || deltaIn < 0 {
		deltaIn = rawIn
	}
	if u.RawOut == 0 || deltaOut < 0 {
		deltaOut = rawOut
	}
	u.RawIn, u.RawOut = rawIn, rawOut
	u.UsedIn += deltaIn
	u.UsedOut += deltaOut
	if u.QuotaBytes() > 0 && u.UsedBytes() >= u.QuotaBytes() {
		u.QuotaPausedAt = ptrTime(time.Now().UTC())
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE singbox_users SET used_in=?,used_out=?,raw_in=?,raw_out=?,quota_paused_at=?,updated_at=? WHERE id=?`, u.UsedIn, u.UsedOut, u.RawIn, u.RawOut, u.QuotaPausedAt, time.Now().UTC(), id); err != nil {
		return nil, err
	}
	return u, nil
}

// PortClaimed checks both Realm hops and sing-box users because both bind on
// the same node namespace but live in different tables.
func (s *Store) PortClaimed(ctx context.Context, nodeID int64, port int, exceptUserID int64) (bool, error) {
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM route_hops WHERE node_id=? AND relay_port=?`, nodeID, port).Scan(&n); err != nil {
		return false, err
	}
	if n > 0 {
		return true, nil
	}
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM singbox_users WHERE node_id=? AND port=? AND id<>?`, nodeID, port, exceptUserID).Scan(&n); err != nil {
		return false, err
	}
	return n > 0, nil
}

func ptrTime(t time.Time) *time.Time { return &t }

func nullableExpiry(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t
}
