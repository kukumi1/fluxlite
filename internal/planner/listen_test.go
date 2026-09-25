package planner

import (
	"strings"
	"testing"

	"github.com/kukumi1/fluxlite/internal/model"
)

func TestListenAddressDefaultsToIPv4(t *testing.T) {
	got := ListenAddress(&model.Route{}, 0, 10001)
	if got != "0.0.0.0:10001" {
		t.Fatalf("默认应当维持 IPv4 监听，实际 %q —— 改监听地址会重写配置、"+
			"而 realm 不能热重载，等于把在跑的链路断一次", got)
	}
}

func TestListenAddressBracketsIPv6(t *testing.T) {
	got := ListenAddress(&model.Route{ListenIPv6: true}, 0, 10001)
	if got != "[::]:10001" {
		t.Fatalf("IPv6 监听地址必须带方括号，否则 realm 会把冒号当成端口分隔符: %q", got)
	}
}

// 这条守的是上一版真实踩到的故障：开关做在节点上时，它把该节点每条链路的监听
// 都从 0.0.0.0 改成了 [::]，而 [::] 会取走该端口的整个 IPv6 通配空间 —— 机器上
// 另一个程序（sing-box）正把同一端口绑在具体 v6 地址上，realm 直接冲突退出，
// 一条毫不相干的在跑链路就此中断。
func TestListenAddressOnlyWidensTheEntryHop(t *testing.T) {
	route := &model.Route{ListenIPv6: true}

	if got := ListenAddress(route, 0, 10001); got != "[::]:10001" {
		t.Errorf("首跳才是客户端连入的那一跳，应当绑 IPv6: %q", got)
	}
	for _, hop := range []int{1, 2} {
		if got := ListenAddress(route, hop, 10001); got != "0.0.0.0:10001" {
			t.Errorf("第 %d 跳是被上一跳按节点 v4 地址拨过去的，绑 IPv6 收不到任何东西，"+
				"却会占掉整个 v6 通配空间去和别的程序冲突: %q", hop, got)
		}
	}
}

// 客户端能不能用 IPv6 连进来，完全取决于这一行写的是什么。0.0.0.0 只收 IPv4，
// 所以一台双栈中转机在 v4 拥堵时也没法让客户端改走 v6。
func TestRenderConfigCarriesListenAddressVerbatim(t *testing.T) {
	route := &model.Route{Slug: "hk-tw", Protocol: model.ProtocolTCP}

	for _, tc := range []struct {
		name     string
		listen   string
		ipv6Only bool
	}{
		{"IPv4", "0.0.0.0:21300", false},
		{"IPv6", "[::]:21300", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := renderConfig(route, tc.listen, tc.ipv6Only, "203.0.113.9:443")
			want := "listen = \"" + tc.listen + "\""
			if !strings.Contains(cfg, want) {
				t.Fatalf("配置里找不到 %q:\n%s", want, cfg)
			}
		})
	}
}

// 监听地址进了配置，配置进了 hash，hash 决定要不要重写并重启 realm。两种监听
// 必须 hash 不同，否则把节点切到 IPv6 之后下发会被当成「无变化」跳过。
func TestListenAddressChangesConfigHash(t *testing.T) {
	route := &model.Route{Slug: "hk-tw", Protocol: model.ProtocolTCP}

	v4 := hashConfig(renderConfig(route, "0.0.0.0:21300", false, "203.0.113.9:443"))
	v6 := hashConfig(renderConfig(route, "[::]:21300", true, "203.0.113.9:443"))

	if v4 == v6 {
		t.Fatal("两种监听地址的配置 hash 相同，切换 IPv6 后下发会被当成无变化而跳过")
	}
}

// 双栈监听会让「IPv6 链路」照样接受 IPv4 客户端，于是面板无法回答那个唯一重要
// 的问题：我的流量到底有没有走 IPv6。ipv6_only 把它变成可验证的。
func TestIPv6HopIsListenerOnlyForIPv6(t *testing.T) {
	route := &model.Route{Slug: "hk-tw", Protocol: model.ProtocolTCPUDP, ListenIPv6: true}

	v6 := renderConfig(route, ListenAddress(route, 0, 21300), true, "203.0.113.9:443")
	if !strings.Contains(v6, "ipv6_only = true") {
		t.Fatalf("IPv6 首跳缺少 ipv6_only，监听会变成双栈:\n%s", v6)
	}

	// 同一条链路的后续跳仍然是 IPv4，不能被这行波及。
	later := renderConfig(route, ListenAddress(route, 1, 21300), false, "203.0.113.9:443")
	if strings.Contains(later, "ipv6_only") {
		t.Errorf("IPv4 跳不该出现 ipv6_only:\n%s", later)
	}
}

// 加了 ipv6_only 之后配置内容变了，hash 必须跟着变，否则已经下发过的 v6 链路
// 会被当成「无变化」跳过，机器上仍然跑着双栈的旧配置。
func TestIPv6OnlyChangesConfigHash(t *testing.T) {
	route := &model.Route{Slug: "hk-tw", Protocol: model.ProtocolTCP, ListenIPv6: true}

	dual := hashConfig(renderConfig(route, "[::]:21300", false, "203.0.113.9:443"))
	only := hashConfig(renderConfig(route, "[::]:21300", true, "203.0.113.9:443"))
	if dual == only {
		t.Fatal("开关 ipv6_only 没有改变配置 hash，下发会被跳过")
	}
}
