package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/kukumi1/fluxlite/internal/model"
)

const nodeColumns = `id, name, host, ssh_port, ssh_user, auth_type, auth_secret,
	via_node_id, port_start, port_end, host_key, arch, os_id, init_system,
	udp_capable, skip_udp_probe, realm_version, status, last_seen, created_at, updated_at`

func scanNode(row interface{ Scan(...any) error }) (*model.Node, error) {
	var n model.Node
	var via sql.NullInt64
	var udp sql.NullBool
	var lastSeen sql.NullTime

	err := row.Scan(&n.ID, &n.Name, &n.Host, &n.SSHPort, &n.SSHUser, &n.AuthType,
		&n.AuthSecret, &via, &n.PortStart, &n.PortEnd, &n.HostKey, &n.Arch,
		&n.OSID, &n.InitSystem, &udp, &n.SkipUDPProbe, &n.RealmVersion, &n.Status,
		&lastSeen, &n.CreatedAt, &n.UpdatedAt)
	if err != nil {
		return nil, err
	}
	if via.Valid {
		n.ViaNodeID = &via.Int64
	}
	if udp.Valid {
		n.UDPCapable = &udp.Bool
	}
	if lastSeen.Valid {
		n.LastSeen = &lastSeen.Time
	}
	return &n, nil
}

// CreateNode inserts a node and assigns its ID.
func (s *Store) CreateNode(ctx context.Context, n *model.Node) error {
	if err := n.Validate(); err != nil {
		return err
	}
	now := time.Now().UTC()
	n.CreatedAt, n.UpdatedAt = now, now
	if n.Status == "" {
		n.Status = model.StatusUnknown
	}

	res, err := s.db.ExecContext(ctx, `
		INSERT INTO nodes (name, host, ssh_port, ssh_user, auth_type, auth_secret,
			via_node_id, port_start, port_end, host_key, arch, os_id, init_system,
			udp_capable, skip_udp_probe, realm_version, status, last_seen,
			created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		n.Name, n.Host, n.SSHPort, n.SSHUser, n.AuthType, n.AuthSecret,
		nullInt64(n.ViaNodeID), n.PortStart, n.PortEnd, n.HostKey, n.Arch,
		n.OSID, n.InitSystem, nullBool(n.UDPCapable), n.SkipUDPProbe,
		n.RealmVersion, n.Status, nullTime(n.LastSeen), n.CreatedAt, n.UpdatedAt)
	if err != nil {
		if isUniqueViolation(err) {
			return fmt.Errorf("node name %q %w", n.Name, ErrConflict)
		}
		return fmt.Errorf("insert node: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return fmt.Errorf("node id: %w", err)
	}
	n.ID = id
	return nil
}

// NodeByID returns one node.
func (s *Store) NodeByID(ctx context.Context, id int64) (*model.Node, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+nodeColumns+` FROM nodes WHERE id = ?`, id)
	n, err := scanNode(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("node %d: %w", id, ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("scan node: %w", err)
	}
	return n, nil
}

// NodeByName returns one node by its unique name.
func (s *Store) NodeByName(ctx context.Context, name string) (*model.Node, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+nodeColumns+` FROM nodes WHERE name = ?`, name)
	n, err := scanNode(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("node %q: %w", name, ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("scan node: %w", err)
	}
	return n, nil
}

// ListNodes returns all nodes ordered by name.
func (s *Store) ListNodes(ctx context.Context) ([]*model.Node, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+nodeColumns+` FROM nodes ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("query nodes: %w", err)
	}
	defer rows.Close()

	var out []*model.Node
	for rows.Next() {
		n, err := scanNode(rows)
		if err != nil {
			return nil, fmt.Errorf("scan node: %w", err)
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// UpdateNode persists mutable fields of an existing node.
func (s *Store) UpdateNode(ctx context.Context, n *model.Node) error {
	if err := n.Validate(); err != nil {
		return err
	}
	n.UpdatedAt = time.Now().UTC()

	res, err := s.db.ExecContext(ctx, `
		UPDATE nodes SET name=?, host=?, ssh_port=?, ssh_user=?, auth_type=?,
			auth_secret=?, via_node_id=?, port_start=?, port_end=?, host_key=?,
			arch=?, os_id=?, init_system=?, udp_capable=?, skip_udp_probe=?,
			realm_version=?, status=?, last_seen=?, updated_at=?
		WHERE id=?`,
		n.Name, n.Host, n.SSHPort, n.SSHUser, n.AuthType, n.AuthSecret,
		nullInt64(n.ViaNodeID), n.PortStart, n.PortEnd, n.HostKey, n.Arch,
		n.OSID, n.InitSystem, nullBool(n.UDPCapable), n.SkipUDPProbe,
		n.RealmVersion, n.Status, nullTime(n.LastSeen), n.UpdatedAt, n.ID)
	if err != nil {
		if isUniqueViolation(err) {
			return fmt.Errorf("node name %q %w", n.Name, ErrConflict)
		}
		return fmt.Errorf("update node: %w", err)
	}
	return checkAffected(res, "node", n.ID)
}

// DeleteNode removes a node. The schema refuses deletion while routes still
// reference it, surfacing a clear conflict instead of orphaning hops.
func (s *Store) DeleteNode(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM nodes WHERE id = ?`, id)
	if err != nil {
		if isForeignKeyViolation(err) {
			return fmt.Errorf("node %d is still used by a route or as a jump host: %w", id, ErrConflict)
		}
		return fmt.Errorf("delete node: %w", err)
	}
	return checkAffected(res, "node", id)
}

// SetNodeHostKey records a host key learned on first connect.
func (s *Store) SetNodeHostKey(ctx context.Context, nodeID int64, hostKey string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE nodes SET host_key = ?, updated_at = ? WHERE id = ?`,
		hostKey, time.Now().UTC(), nodeID)
	if err != nil {
		return fmt.Errorf("set host key: %w", err)
	}
	return nil
}

// SetNodeStatus updates reachability without touching other fields.
func (s *Store) SetNodeStatus(ctx context.Context, nodeID int64, status model.NodeStatus, seen *time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE nodes SET status = ?, last_seen = ?, updated_at = ? WHERE id = ?`,
		status, nullTime(seen), time.Now().UTC(), nodeID)
	if err != nil {
		return fmt.Errorf("set node status: %w", err)
	}
	return nil
}

// UsedPortsOnNode returns the ports already claimed on a node, optionally
// ignoring one route so an update can reuse its own allocations.
func (s *Store) UsedPortsOnNode(ctx context.Context, nodeID int64, excludeRouteID *int64) (map[int]bool, error) {
	query := `SELECT relay_port FROM route_hops WHERE node_id = ?`
	args := []any{nodeID}
	if excludeRouteID != nil {
		query += ` AND route_id != ?`
		args = append(args, *excludeRouteID)
	}

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query used ports: %w", err)
	}
	defer rows.Close()

	used := make(map[int]bool)
	for rows.Next() {
		var p int
		if err := rows.Scan(&p); err != nil {
			return nil, fmt.Errorf("scan port: %w", err)
		}
		used[p] = true
	}
	return used, rows.Err()
}

// NodesReferencing returns nodes that use the given node as their jump host.
func (s *Store) NodesReferencing(ctx context.Context, viaID int64) ([]*model.Node, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+nodeColumns+` FROM nodes WHERE via_node_id = ? ORDER BY name`, viaID)
	if err != nil {
		return nil, fmt.Errorf("query dependents: %w", err)
	}
	defer rows.Close()

	var out []*model.Node
	for rows.Next() {
		n, err := scanNode(rows)
		if err != nil {
			return nil, fmt.Errorf("scan node: %w", err)
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func nullInt64(v *int64) any {
	if v == nil {
		return nil
	}
	return *v
}

func nullBool(v *bool) any {
	if v == nil {
		return nil
	}
	return *v
}

func nullTime(v *time.Time) any {
	if v == nil {
		return nil
	}
	return *v
}

func checkAffected(res interface{ RowsAffected() (int64, error) }, kind string, id int64) error {
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("%s %d: %w", kind, id, ErrNotFound)
	}
	return nil
}

// The SQLite driver reports constraint failures only through the message, so
// these classifiers match on its stable wording.
func isUniqueViolation(err error) bool {
	return strings.Contains(err.Error(), "UNIQUE constraint failed")
}

func isForeignKeyViolation(err error) bool {
	return strings.Contains(err.Error(), "FOREIGN KEY constraint failed")
}
