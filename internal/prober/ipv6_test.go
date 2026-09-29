package prober

import "testing"

// 输入都是真实机器 /proc/net/if_inet6 里 scope 00 的原始行。
func TestJudgeIPv6(t *testing.T) {
	cases := []struct {
		name       string
		bindv6only string
		raw        string
		wantDual   bool
		wantAddr   string
	}{
		{
			// WAWO-HK：容器身上只有 fd91::/64 这个私有地址，服务商把公网 v6 转进的是
			// 容器的 IPv4。内核把它算作 global，旧逻辑就把它当成了入口地址。
			name:       "只有 ULA 的 NAT 容器不算有 IPv6 入口",
			bindv6only: "0",
			raw:        "fd91cafecafe0010000000000000001c",
		},
		{
			name:       "原生公网 IPv6",
			bindv6only: "0",
			raw:        "2a0e97c003f000010000000000000421",
			wantDual:   true,
			wantAddr:   "2a0e:97c0:3f0:1::421",
		},
		{
			name:       "ULA 排在前面时跳过它，取后面的公网地址",
			bindv6only: "0",
			raw:        "fd91cafecafe0010000000000000001c,2a0e97c003f000010000000000000421",
			wantDual:   true,
			wantAddr:   "2a0e:97c0:3f0:1::421",
		},
		{
			// 地址照样记下（客户端要连它），只是不认为 [::] 能兼顾 IPv4。
			name:       "bindv6only 为 1",
			bindv6only: "1",
			raw:        "2a0e97c003f000010000000000000421",
			wantDual:   false,
			wantAddr:   "2a0e:97c0:3f0:1::421",
		},
		{
			name:       "没有任何 scope 00 地址",
			bindv6only: "0",
			raw:        "",
		},
		{
			name:       "读 /proc 失败时的空输出与乱码",
			bindv6only: "",
			raw:        "zz,1234",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dual, addr := judgeIPv6(tc.bindv6only, tc.raw)
			if dual != tc.wantDual || addr != tc.wantAddr {
				t.Errorf("得到 (%v, %q)，期望 (%v, %q)", dual, addr, tc.wantDual, tc.wantAddr)
			}
		})
	}
}
