package service

import (
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
