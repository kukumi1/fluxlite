package api

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"
)

func pngDataURL(t *testing.T, w, h int) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	img.Set(0, 0, color.RGBA{R: 1, G: 2, B: 3, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return avatarDataURLPrefix + base64.StdEncoding.EncodeToString(buf.Bytes())
}

func TestDecodeAvatarAcceptsSmallPNG(t *testing.T) {
	raw, err := decodeAvatar(pngDataURL(t, 128, 128))
	if err != nil {
		t.Fatalf("正常的 128px PNG 被拒了: %v", err)
	}
	if len(raw) == 0 {
		t.Fatal("解出来是空的")
	}
	if round := avatarDataURL(raw); !strings.HasPrefix(round, avatarDataURLPrefix) {
		t.Fatalf("回写的 data URL 前缀不对: %q", round[:20])
	}
}

// 浏览器那侧会先过一遍 canvas，但请求不一定来自浏览器，所以服务端必须自己
// 确认这真是一张 PNG，而不是照着前缀伪装的任意字节。
func TestDecodeAvatarRejectsNonPNG(t *testing.T) {
	cases := map[string]string{
		"前缀不对":        "data:image/svg+xml;base64,PHN2Zz48L3N2Zz4=",
		"根本不是 base64": avatarDataURLPrefix + "!!!!",
		"是 base64 但不是 PNG": avatarDataURLPrefix +
			base64.StdEncoding.EncodeToString([]byte("<svg onload=alert(1)>")),
		"空的": "",
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeAvatar(input); err == nil {
				t.Fatal("本该被拒绝")
			}
		})
	}
}

// 小小一个 PNG 可以解出巨大的位图，只卡字节数拦不住。
func TestDecodeAvatarRejectsOversizedDimensions(t *testing.T) {
	if _, err := decodeAvatar(pngDataURL(t, avatarMaxPixels+1, 8)); err == nil {
		t.Fatal("超宽的图本该被拒绝")
	}
}

func TestAvatarDataURLEmptyWhenUnset(t *testing.T) {
	if got := avatarDataURL(nil); got != "" {
		t.Fatalf("没有头像时应当是空串, got %q", got)
	}
}
