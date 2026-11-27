package report

import (
	"bytes"
	"errors"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func iptr(v int) *int         { return &v }
func fptr(v float64) *float64 { return &v }

func sample() Input {
	in := Input{
		Name: "V6VPS-HK", Families: []int{4, 6}, Settings: "协议：IPv4 + IPv6 | 运营商：电信、联通、移动 | 测速点：四川、上海、广东 | 单线程",
		Egress: "154.83.*.* 香港 Zillion", Items: []string{"route", "large", "udp", "speed"}, Modes: []string{"single"},
		Traffic: "消耗流量：共 1.20 GB（下载 0.70 GB / 上传 0.50 GB）", Elapsed: 277, Finished: time.Unix(1_764_160_440, 0),
	}
	lines := map[string]string{"电信": "CN2 GIA", "联通": "CUG→联通", "移动": "CMIN2"}
	for i, prov := range []string{"四川", "上海", "广东"} {
		for j, c := range []string{"电信", "联通", "移动"} {
			p := prov + c
			in.Points = append(in.Points, p)
			in.Quality = append(in.Quality,
				Quality{Point: p, Item: "route", Family: 4, Line: lines[c], MS: iptr(140 + i*40 + j*30), Loss: iptr([]int{0, 4, 28}[(i+j)%3])},
				Quality{Point: p, Item: "large", Family: 4, MS: iptr(150 + i*40 + j*30), Loss: iptr([]int{0, 8, 0}[(i+j)%3])},
				Quality{Point: p, Item: "udp", Family: 4, Line: lines[c]},
				Quality{Point: p, Item: "route", Family: 6, Line: []string{"163", "4837", "CMI"}[j], MS: iptr(220 + j*10), Loss: iptr(0)})
			in.Speeds = append(in.Speeds, Speed{Point: p, Mode: "single", Family: 4, RTT: "164ms", Down: fptr(87.5 - float64(j*30)), Up: fptr(221.3 - float64(i*80)), Retrans: fptr([]float64{0, 4.5, 0.02}[j])})
		}
	}
	in.Quality[4] = Quality{Point: "四川联通", Item: "route", Family: 4, Error: "追踪没有任何跳点回应", Loss: iptr(100)}
	in.Speeds[4].Down = fptr(0)
	in.Speeds[4].Error = "服务器忙"
	return in
}

func decode(t *testing.T, b []byte, err error) image.Image {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	return img
}

func save(t *testing.T, name string, b []byte) {
	if dir := os.Getenv("REPORT_OUT"); dir != "" {
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRenderOverview(t *testing.T) {
	b, err := RenderOverview(sample())
	img := decode(t, b, err)
	if img.Bounds().Dx() < width*scale {
		t.Fatal(img.Bounds())
	}
	save(t, "overview.png", b)

	one := Input{Name: "Bage-SG", Families: []int{4, 6}, Points: []string{"四川移动"}, Items: []string{"route", "large", "udp", "speed"}, Modes: []string{"single", "multi"}}
	for _, fam := range []int{4, 6} {
		one.Quality = append(one.Quality,
			Quality{Point: "四川移动", Item: "route", Family: fam, Line: "CMI", MS: iptr(120), Loss: iptr(0)},
			Quality{Point: "四川移动", Item: "large", Family: fam, MS: iptr(120), Loss: iptr(55)},
			Quality{Point: "四川移动", Item: "udp", Family: fam, Line: "CMI"})
		for _, mode := range one.Modes {
			one.Speeds = append(one.Speeds, Speed{Point: "四川移动", Mode: mode, Family: fam, RTT: "123ms", Down: fptr(294.8), Up: fptr(267.9), Retrans: fptr(0)})
		}
	}
	b, err = RenderOverview(one)
	if img := decode(t, b, err); img.Bounds().Dy() > 600*scale {
		t.Fatalf("one point on both families is a short image: %v", img.Bounds())
	}
	save(t, "overview-one.png", b)

	routeOnly := sample()
	routeOnly.Items = []string{"route"}
	narrow, err := RenderOverview(routeOnly)
	if img := decode(t, narrow, err); img.Bounds().Dx() != width*scale {
		t.Fatalf("without a speed test the table needs no extra width: %v", img.Bounds())
	}
}

func TestRenderSections(t *testing.T) {
	in := sample()
	tabs := Sections(in)
	var keys []string
	for _, tab := range tabs {
		keys = append(keys, tab.Key)
	}
	if strings.Join(keys, " ") != "route-4 large-4 udp-4 single-4 route-6" {
		t.Fatal(keys)
	}
	for _, tab := range tabs {
		b, err := RenderSection(in, tab.Key)
		if img := decode(t, b, err); img.Bounds().Dx() != width*scale {
			t.Fatal(tab, img.Bounds())
		}
		save(t, tab.Key+".png", b)
	}
	if _, err := RenderSection(in, "multi-4"); !errors.Is(err, ErrNoSection) {
		t.Fatal(err)
	}
}

// The page asks for every machine's report at once.
func TestRenderConcurrently(t *testing.T) {
	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := RenderOverview(sample()); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
}
