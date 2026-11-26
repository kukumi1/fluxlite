package report

import (
	"bytes"
	"image/png"
	"os"
	"testing"
	"time"
)

func iptr(v int) *int         { return &v }
func fptr(v float64) *float64 { return &v }

func sample() Input {
	in := Input{
		Name: "V6VPS-HK", Family: 4, Settings: "协议：IPv4 | 运营商：电信、联通、移动 | 测速点：四川、上海、广东 | 单线程",
		Egress: "154.83.*.* 香港 Zillion", Items: []string{"route", "large", "udp", "speed"}, Modes: []string{"single"},
		Traffic: "消耗流量：共 1.20 GB（下载 0.70 GB / 上传 0.50 GB）", Elapsed: 277, Finished: time.Unix(1_764_160_440, 0),
	}
	lines := map[string]string{"电信": "CN2 GIA", "联通": "CUG→联通", "移动": "CMIN2"}
	for i, prov := range []string{"四川", "上海", "广东"} {
		for j, c := range []string{"电信", "联通", "移动"} {
			p := prov + c
			in.Points = append(in.Points, p)
			in.Quality = append(in.Quality,
				Quality{Point: p, Item: "route", Line: lines[c], MS: iptr(140 + i*40 + j*30), Loss: iptr([]int{0, 4, 28}[(i+j)%3])},
				Quality{Point: p, Item: "large", MS: iptr(150 + i*40 + j*30), Loss: iptr([]int{0, 8, 0}[(i+j)%3])},
				Quality{Point: p, Item: "udp", Line: lines[c]})
			in.Speeds = append(in.Speeds, Speed{Point: p, Mode: "single", RTT: "164ms", Down: fptr(87.5 - float64(j*30)), Up: fptr(221.3 - float64(i*80)), Retrans: fptr([]float64{0, 4.5, 0.02}[j])})
		}
	}
	in.Quality[3] = Quality{Point: "四川联通", Item: "route", Error: "追踪没有任何跳点回应", Loss: iptr(100)}
	in.Speeds[4].Down = fptr(0)
	in.Speeds[4].Error = "服务器忙"
	return in
}

func TestRender(t *testing.T) {
	b, err := Render(sample())
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(bytes.NewReader(b))
	if err != nil || img.Bounds().Dx() != width*scale {
		t.Fatalf("%v %v", img.Bounds(), err)
	}
	if out := os.Getenv("REPORT_OUT"); out != "" {
		if err := os.WriteFile(out, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}
