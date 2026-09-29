package service

import (
	"context"
	"errors"
	"testing"

	"github.com/kukumi1/fluxlite/internal/model"
)

func boolPtr(b bool) *bool { return &b }

// 开启 IPv6 入口会改写首跳的监听地址，而 realm 不能热重载 —— 猜错的代价是把在跑
// 的连接断一次。所以只有探测确认过的节点才允许开。
func TestAllowListenIPv6(t *testing.T) {
	cases := []struct {
		name    string
		node    *model.Node
		was     bool
		want    bool
		wantErr error
	}{
		{
			name: "未探测时拒绝开启",
			node: &model.Node{IPv6Capable: nil},
			want: true, wantErr: ErrIPv6Unprobed,
		},
		{
			name: "探测确认不支持时拒绝开启",
			node: &model.Node{IPv6Capable: boolPtr(false)},
			want: true, wantErr: ErrIPv6Unavailable,
		},
		{
			name: "探测确认支持时允许开启",
			node: &model.Node{IPv6Capable: boolPtr(true)},
			want: true, wantErr: nil,
		},
		{
			name: "关闭永远允许 —— 退回 IPv4 是安全方向，不该被任何判断挡住",
			node: &model.Node{IPv6Capable: boolPtr(false)},
			want: false, wantErr: nil,
		},
		{
			// 机器可能在开启之后才失去 IPv6。若把这当成持久不变量校验，这条链路
			// 的每一次无关编辑（改名、改额度）都会被连带拒绝。
			name: "已开启的链路即使节点现在探测不到 IPv6，也不阻断其他编辑",
			node: &model.Node{IPv6Capable: boolPtr(false)},
			was:  true, want: true, wantErr: nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := allowListenIPv6(tc.node, tc.was, tc.want)
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("应当放行，却报错: %v", err)
				}
				return
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("错误不对\n期望: %v\n实际: %v", tc.wantErr, err)
			}
		})
	}
}

func TestNormalizeIPv6Entry(t *testing.T) {
	cases := []struct {
		in, want string
		err      error
	}{
		{"", "", nil},
		{"2400:c620:22:282::10", "2400:c620:22:282::10", nil},
		{"  [2400:C620:0022:0282:0:0:0:10]  ", "2400:c620:22:282::10", nil}, // 从 [addr]:port 里复制出来、带大写和前导零
		{"fd91:cafe:cafe:10::1c", "", ErrIPv6EntryNotPublic},                // 容器自己的 ULA，外面连不到
		{"fe80::1", "", ErrIPv6EntryNotPublic},
		{"2.27.109.96", "", ErrIPv6EntryInvalid},
		{"not-an-address", "", ErrIPv6EntryInvalid},
	}
	for _, tc := range cases {
		got, err := normalizeIPv6Entry(tc.in)
		if !errors.Is(err, tc.err) || got != tc.want {
			t.Errorf("normalizeIPv6Entry(%q) = (%q, %v)，期望 (%q, %v)", tc.in, got, err, tc.want, tc.err)
		}
	}
}

// 手填了入口的 NAT 机，探测结果是「没有公网 IPv6」，但它确实能从 IPv6 连进来。
func TestAllowListenIPv6AcceptsForwardedEntry(t *testing.T) {
	node := &model.Node{IPv6Capable: boolPtr(false), IPv6Entry: "2400:c620:22:282::10"}
	if err := allowListenIPv6(node, false, true); err != nil {
		t.Fatalf("有手填 IPv6 入口的节点应当允许建 IPv6 链路: %v", err)
	}
}

// 清空入口地址后，以它为入口的 IPv6 链路会改绑 [::]，而这台机器自己没有公网
// IPv6 —— 链路照常「运行」，却一个包都收不到。必须在改的那一刻拦下来。
func TestUpdateNodeRefusesToStrandIPv6Routes(t *testing.T) {
	ctx := context.Background()
	s := newReinstallService(t)
	node := seedReinstallNode(t, s)
	node.IPv6Capable = boolPtr(false)
	node.IPv6Entry = "2400:c620:22:282::10"
	if err := s.store.UpdateNode(ctx, node); err != nil {
		t.Fatalf("设置入口: %v", err)
	}

	route := &model.Route{
		Name: "v6", Slug: "v6", Target: "203.0.113.9:443", Protocol: model.ProtocolTCP,
		Enabled: true, ListenIPv6: true, EntryPort: 10001,
		Hops: []model.RouteHop{{NodeID: node.ID, HopOrder: 0, RelayPort: 10001}},
	}
	if err := s.store.CreateRoute(ctx, route); err != nil {
		t.Fatalf("建链路: %v", err)
	}

	in := NodeInput{
		Name: node.Name, Host: node.Host, SSHPort: node.SSHPort, SSHUser: node.SSHUser,
		AuthType: node.AuthType, PortStart: node.PortStart, PortEnd: node.PortEnd,
	}
	if _, err := s.UpdateNode(ctx, node.ID, in); !errors.Is(err, ErrIPv6EntryInUse) {
		t.Fatalf("还有 IPv6 链路在用时清空入口应当被拒绝，实际: %v", err)
	}

	route.ListenIPv6 = false
	if err := s.store.UpdateRoute(ctx, route); err != nil {
		t.Fatalf("关掉链路的 IPv6: %v", err)
	}
	updated, err := s.UpdateNode(ctx, node.ID, in)
	if err != nil {
		t.Fatalf("没有 IPv6 链路依赖后应当允许清空: %v", err)
	}
	if updated.IPv6Entry != "" {
		t.Errorf("入口没有被清空: %q", updated.IPv6Entry)
	}
}
