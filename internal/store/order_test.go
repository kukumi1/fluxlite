package store

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/kukumi1/fluxlite/internal/model"
)

func TestManualOrderPersistenceAndIsolation(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "order.db")
	st, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	nodes := make([]*model.Node, 0, 3)
	addNode := func(name string) *model.Node {
		n := &model.Node{Name: name, Host: "192.0.2.1", SSHPort: 22, SSHUser: "root", AuthType: model.AuthKey, AuthSecret: []byte("secret"), PortStart: 10000, PortEnd: 20000}
		if err := st.CreateNode(ctx, n); err != nil {
			t.Fatal(err)
		}
		nodes = append(nodes, n)
		return n
	}
	addNode("z")
	addNode("b")
	routes := []*model.Route{routeNamed(nodes[0].ID, "z", "z", 10001, false), routeNamed(nodes[0].ID, "b", "b", 10002, false)}
	for _, r := range routes {
		if err := st.CreateRoute(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	nodeIDs := func() []int64 {
		got, err := st.ListNodes(ctx)
		if err != nil {
			t.Fatal(err)
		}
		ids := []int64{}
		for _, v := range got {
			ids = append(ids, v.ID)
		}
		return ids
	}
	routeIDs := func() []int64 {
		got, err := st.ListRoutes(ctx)
		if err != nil {
			t.Fatal(err)
		}
		ids := []int64{}
		for _, v := range got {
			ids = append(ids, v.ID)
		}
		return ids
	}
	for _, tc := range []struct {
		name    string
		ids     []int64
		list    func() []int64
		reorder func(context.Context, []int64) error
	}{
		{"nodes", []int64{nodes[0].ID, nodes[1].ID}, nodeIDs, st.ReorderNodes},
		{"routes", []int64{routes[0].ID, routes[1].ID}, routeIDs, st.ReorderRoutes},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.list(); !reflect.DeepEqual(got, []int64{tc.ids[1], tc.ids[0]}) {
				t.Fatalf("default order %v", got)
			}
			if err := tc.reorder(ctx, tc.ids); err != nil {
				t.Fatal(err)
			}
			for _, bad := range []struct {
				ids  []int64
				want error
			}{
				{[]int64{tc.ids[0], tc.ids[0]}, ErrInvalidOrder}, {[]int64{0, tc.ids[1]}, ErrInvalidOrder}, {[]int64{-1, tc.ids[1]}, ErrInvalidOrder},
				{tc.ids[:1], ErrConflict}, {[]int64{tc.ids[0], 9999}, ErrConflict}, {append(append([]int64{}, tc.ids...), 9999), ErrConflict},
			} {
				if err := tc.reorder(ctx, bad.ids); !errors.Is(err, bad.want) {
					t.Fatalf("error %v, want %v", err, bad.want)
				}
				if got := tc.list(); !reflect.DeepEqual(got, tc.ids) {
					t.Fatalf("invalid request changed order %v", got)
				}
			}
		})
	}
	before, err := st.RouteByID(ctx, routes[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.ReorderRoutes(ctx, []int64{routes[1].ID, routes[0].ID}); err != nil {
		t.Fatal(err)
	}
	after, err := st.RouteByID(ctx, routes[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("reorder changed route configuration or hops")
	}
	nodes[0].Arch = "aarch64"
	if err := st.UpdateNode(ctx, nodes[0]); err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateRoute(ctx, routes[0]); err != nil {
		t.Fatal(err)
	}
	addNode("a")
	r := routeNamed(nodes[0].ID, "a", "a", 10003, false)
	if err := st.CreateRoute(ctx, r); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if got := nodeIDs(); !reflect.DeepEqual(got, []int64{nodes[0].ID, nodes[1].ID, nodes[2].ID}) {
		t.Fatalf("node persistence/append %v", got)
	}
	if got := routeIDs(); !reflect.DeepEqual(got, []int64{routes[1].ID, routes[0].ID, r.ID}) {
		t.Fatalf("route persistence/append %v", got)
	}
	var version int
	if err := st.db.QueryRowContext(ctx, "SELECT version FROM schema_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 51 {
		t.Fatalf("schema version %d", version)
	}
}

func TestManualOrderRollbackOnWriteFailure(t *testing.T) {
	ctx := context.Background()
	st := openTemp(t)
	r := seedRoute(t, st)
	nodes, err := st.ListNodes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.ExecContext(ctx, `CREATE TRIGGER reject_order BEFORE UPDATE OF sort_order ON nodes WHEN NEW.sort_order=1 BEGIN SELECT RAISE(ABORT, 'test failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err := st.ReorderNodes(ctx, []int64{nodes[1].ID, nodes[0].ID}); err == nil {
		t.Fatal("expected write failure")
	}
	var count int
	if err := st.db.QueryRowContext(ctx, "SELECT count(*) FROM nodes WHERE sort_order IS NOT NULL").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("partially committed order")
	}
	if _, err := st.RouteByID(ctx, r.ID); err != nil {
		t.Fatal(err)
	}
}

func TestManualOrderMigrationFrom49PreservesData(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "legacy.db")
	st, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	route := seedRoute(t, st)
	before, err := st.RouteByID(ctx, route.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.ExecContext(ctx, `ALTER TABLE nodes DROP COLUMN sort_order; ALTER TABLE routes DROP COLUMN sort_order; UPDATE schema_version SET version=49`); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	after, err := st.RouteByID(ctx, route.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("migration changed route or hops")
	}
	for _, table := range []string{"nodes", "routes"} {
		var count int
		if err := st.db.QueryRowContext(ctx, "SELECT count(*) FROM "+table+" WHERE sort_order IS NOT NULL").Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("%s legacy order should remain unset", table)
		}
	}
}
