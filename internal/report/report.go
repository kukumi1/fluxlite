// Package report draws a machine's speed and return-quality test as a PNG,
// laid out like the TcpQuality reports operators already read: one section
// per item, provinces down the side and the three carriers across.
package report

import (
	"bytes"
	_ "embed"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

// The fonts are Noto Sans SC cut down to ASCII and the GB2312 characters;
// see fonts/OFL.txt for their licence.
var (
	//go:embed fonts/NotoSansSC-Regular.ttf
	regularTTF []byte
	//go:embed fonts/NotoSansSC-Bold.ttf
	boldTTF []byte
)

// Speed is one point's speed test in one mode, in Mbps.
type Speed struct {
	Point   string
	Mode    string
	Family  int
	RTT     string
	Down    *float64
	Up      *float64
	Retrans *float64
	Error   string
}

// Quality is one point's return-quality item.
type Quality struct {
	Point  string
	Item   string
	Family int
	Line   string
	MS     *int
	Loss   *int
	Error  string
}

// Input is everything one machine's report shows.
type Input struct {
	Name     string
	Families []int
	Settings string
	Egress   string
	Points   []string
	Items    []string
	Modes    []string
	Quality  []Quality
	Speeds   []Speed
	Traffic  string
	Elapsed  int64
	Finished time.Time
	Note     string
}

const scale = 2

var (
	bg      = color.RGBA{0x1f, 0x20, 0x29, 0xff}
	panel   = color.RGBA{0x26, 0x27, 0x32, 0xff}
	title   = color.RGBA{0x8f, 0xd3, 0xc8, 0xff}
	text    = color.RGBA{0xd8, 0xda, 0xe3, 0xff}
	muted   = color.RGBA{0x8b, 0x8d, 0x98, 0xff}
	good    = color.RGBA{0x7a, 0xd4, 0x8a, 0xff}
	fair    = color.RGBA{0xe6, 0xc3, 0x5c, 0xff}
	bad     = color.RGBA{0xe0, 0x62, 0x6a, 0xff}
	divider = color.RGBA{0x55, 0x57, 0x63, 0xff}
)

var carriers = []string{"电信", "联通", "移动"}

type faces struct {
	title, head, body, small font.Face
}

var (
	faceOnce sync.Once
	faceSet  faces
	faceErr  error
)

func loadFaces() (faces, error) {
	faceOnce.Do(func() {
		regular, err := opentype.Parse(regularTTF)
		if err != nil {
			faceErr = err
			return
		}
		bold, err := opentype.Parse(boldTTF)
		if err != nil {
			faceErr = err
			return
		}
		face := func(f *opentype.Font, size float64) font.Face {
			if faceErr != nil {
				return nil
			}
			var fc font.Face
			fc, faceErr = opentype.NewFace(f, &opentype.FaceOptions{Size: size * scale, DPI: 72, Hinting: font.HintingFull})
			return fc
		}
		faceSet = faces{title: face(bold, 22), head: face(bold, 15), body: face(regular, 14), small: face(regular, 12)}
	})
	return faceSet, faceErr
}

type canvas struct {
	img   *image.RGBA
	faces faces
}

func (c *canvas) width(f font.Face, s string) int { return font.MeasureString(f, s).Ceil() }

// write draws s with its left edge at x (in unscaled pixels) and its
// baseline at y; align is 'l', 'c' or 'r' relative to x.
func (c *canvas) write(f font.Face, col color.Color, x, y int, align byte, s string) {
	px := x * scale
	switch align {
	case 'c':
		px -= c.width(f, s) / 2
	case 'r':
		px -= c.width(f, s)
	}
	d := font.Drawer{Dst: c.img, Src: image.NewUniform(col), Face: f, Dot: fixed.P(px, y*scale)}
	d.DrawString(s)
}

func (c *canvas) rect(col color.Color, x0, y0, x1, y1 int) {
	draw.Draw(c.img, image.Rect(x0*scale, y0*scale, x1*scale, y1*scale), image.NewUniform(col), image.Point{}, draw.Src)
}

func (c *canvas) dots(y, x0, x1 int) {
	for x := x0; x < x1; x += 6 {
		c.rect(divider, x, y, x+3, y+1)
	}
}

// Tones follow the legend: fine at no loss and up to 150ms, fair up to 20%
// and 240ms, bad beyond or failed.
func msTone(ms int) color.Color {
	switch {
	case ms <= 150:
		return good
	case ms <= 240:
		return fair
	}
	return bad
}

func lossTone(loss int) color.Color {
	switch {
	case loss == 0:
		return good
	case loss <= 20:
		return fair
	}
	return bad
}

func retransTone(v float64) color.Color {
	switch {
	case v < 1:
		return good
	case v <= 20:
		return fair
	}
	return bad
}

func speedTone(v float64) color.Color {
	switch {
	case v >= 60:
		return good
	case v >= 20:
		return fair
	}
	return bad
}

// provinces lists the request's provinces in the order asked, and which
// carriers each was asked with.
func provinces(points []string) ([]string, map[string]bool) {
	var out []string
	used := map[string]bool{}
	for _, p := range points {
		for _, c := range carriers {
			if prov, ok := strings.CutSuffix(p, c); ok {
				if !slices.Contains(out, prov) {
					out = append(out, prov)
				}
				used[c] = true
			}
		}
	}
	return out, used
}

const (
	width    = 1000
	margin   = 32
	rowH     = 26
	headerH  = 118
	sectionG = 30
)

// Render draws the report.
func Render(in Input) ([]byte, error) {
	f, err := loadFaces()
	if err != nil {
		return nil, err
	}
	provs, used := provinces(in.Points)
	var cols []string
	for _, c := range carriers {
		if used[c] {
			cols = append(cols, c)
		}
	}
	quality := func(item string, family int) map[string]Quality {
		m := map[string]Quality{}
		for _, q := range in.Quality {
			if q.Item == item && q.Family == family {
				m[q.Point] = q
			}
		}
		return m
	}

	type section struct {
		height int
		draw   func(c *canvas, y int)
	}
	var sections []section
	tableH := func(rows int) int { return 64 + rows*rowH }
	// Each family's sections follow one another, IPv4 first, the way the
	// reports this one copies are read.
	for _, family := range in.Families {
		fam := fmt.Sprintf("IPv%d", family)
		route, large, udp := quality("route", family), quality("large", family), quality("udp", family)
		if slices.Contains(in.Items, "route") && len(route) > 0 {
			sections = append(sections, section{tableH(len(provs)), func(c *canvas, y int) {
				drawQualityTable(c, y, fam+"回程", provs, cols, route, route, true)
			}})
		}
		if slices.Contains(in.Items, "large") && len(large) > 0 {
			sections = append(sections, section{tableH(len(provs)), func(c *canvas, y int) {
				drawQualityTable(c, y, fam+"大包回程（1400B）", provs, cols, large, route, true)
			}})
		}
		if slices.Contains(in.Items, "udp") && len(udp) > 0 {
			sections = append(sections, section{tableH(len(provs)) - 22, func(c *canvas, y int) {
				drawQualityTable(c, y, fam+" UDP回程", provs, cols, udp, udp, false)
			}})
		}
		if !slices.Contains(in.Items, "speed") {
			continue
		}
		for _, mode := range in.Modes {
			var rows []Speed
			for _, p := range in.Points {
				for _, s := range in.Speeds {
					if s.Point == p && s.Mode == mode && s.Family == family {
						rows = append(rows, s)
					}
				}
			}
			if len(rows) == 0 {
				continue
			}
			label := fam + map[string]string{"single": " 单线程测速", "multi": " 多线程测速"}[mode]
			sections = append(sections, section{36 + 28 + len(rows)*rowH, func(c *canvas, y int) {
				drawSpeedTable(c, y, label, fam, rows)
			}})
		}
	}

	height := headerH
	for _, s := range sections {
		height += s.height + sectionG
	}
	height += 96
	c := &canvas{img: image.NewRGBA(image.Rect(0, 0, width*scale, height*scale)), faces: f}
	c.rect(bg, 0, 0, width, height)

	c.write(f.title, title, width/2, 40, 'c', "测速 | "+in.Name)
	sub := in.Settings
	if in.Egress != "" {
		sub = "出口 " + in.Egress + "    " + sub
	}
	c.write(f.small, muted, width/2, 66, 'c', sub)
	if in.Note != "" {
		c.write(f.small, fair, width/2, 88, 'c', in.Note)
	}
	c.dots(headerH-14, margin, width-margin)

	y := headerH
	for _, s := range sections {
		s.draw(c, y)
		y += s.height + sectionG
	}

	legendY := y + 4
	legend := []struct {
		col  color.Color
		text string
	}{{good, "正常：零丢包 / ≤150ms"}, {fair, "一般：≤20% 丢包或重传 / 151-240ms"}, {bad, "异常：>20% / >240ms / 失败"}}
	x := margin + 40
	for _, l := range legend {
		c.write(f.small, l.col, x, legendY, 'l', "● "+l.text)
		x += c.width(f.small, "● "+l.text)/scale + 36
	}
	foot := "报告时间：" + in.Finished.In(time.FixedZone("UTC+8", 8*3600)).Format("2006-01-02 15:04:05") + "（北京时间）"
	if in.Elapsed > 0 {
		foot += fmt.Sprintf("    耗时 %d 秒", in.Elapsed)
	}
	c.write(f.small, muted, width/2, legendY+32, 'c', foot)
	if in.Traffic != "" {
		c.write(f.small, muted, width/2, legendY+56, 'c', in.Traffic)
	}

	var buf bytes.Buffer
	if err := png.Encode(&buf, c.img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// drawQualityTable draws one return-quality section; lines come from
// lineFrom, which for large packets is the TCP route.
func drawQualityTable(c *canvas, y int, label string, provs, cols []string, data, lineFrom map[string]Quality, numbers bool) {
	f := c.faces
	c.write(f.head, title, width/2, y+20, 'c', label)
	if numbers {
		zero, some, many := 0, 0, 0
		for _, q := range data {
			switch {
			case q.Loss == nil:
			case *q.Loss == 0:
				zero++
			case *q.Loss <= 20:
				some++
			default:
				many++
			}
		}
		x := width/2 - 130
		c.write(f.small, text, x, y+42, 'l', "统计：")
		x += 44
		for _, part := range []struct {
			col  color.Color
			text string
		}{{good, fmt.Sprintf("零丢包 %d", zero)}, {fair, fmt.Sprintf("1-20%% %d", some)}, {bad, fmt.Sprintf(">20%% %d", many)}} {
			c.write(f.small, part.col, x, y+42, 'l', part.text)
			x += c.width(f.small, part.text)/scale + 16
		}
	}
	top := y + 64
	if !numbers {
		top -= 22
	}
	groupW := (width - 2*margin - 90) / max(len(cols), 1)
	c.write(f.body, title, margin, top, 'l', "三网")
	for i, col := range cols {
		c.write(f.body, title, margin+90+i*groupW+groupW/2, top, 'c', col)
	}
	for r, prov := range provs {
		ry := top + (r+1)*rowH
		if r%2 == 0 {
			c.rect(panel, margin-8, ry-18, width-margin+8, ry+8)
		}
		c.write(f.body, title, margin, ry, 'l', prov)
		for i, col := range cols {
			gx := margin + 90 + i*groupW
			q, ok := data[prov+col]
			if !ok {
				continue
			}
			line := lineFrom[prov+col].Line
			if line == "" && lineFrom[prov+col].Error != "" {
				line = "-"
			}
			if !numbers {
				lc := text
				if q.Error != "" && q.Line == "" {
					line, lc = "Hidden", muted
				}
				c.write(f.body, lc, gx+groupW/2, ry, 'c', line)
				continue
			}
			c.write(f.body, text, gx+groupW*38/100, ry, 'r', line)
			switch {
			case q.Loss == nil || q.MS == nil && *q.Loss >= 100:
				c.write(f.body, bad, gx+groupW*68/100, ry, 'r', "失败")
			default:
				if q.MS != nil {
					c.write(f.body, msTone(*q.MS), gx+groupW*68/100, ry, 'r', strconv.Itoa(*q.MS)+"ms")
				}
				c.write(f.body, lossTone(*q.Loss), gx+groupW*88/100, ry, 'r', strconv.Itoa(*q.Loss)+"%")
			}
		}
	}
}

func drawSpeedTable(c *canvas, y int, label, fam string, rows []Speed) {
	f := c.faces
	c.write(f.head, title, width/2, y+20, 'c', label)
	top := y + 36 + 22
	colX := []int{margin + 120, margin + 300, margin + 480, margin + 660, width - margin}
	heads := []string{"回程重传", "回程速度", "去程速度", "延迟", ""}
	c.write(f.body, title, margin, top, 'l', fam)
	for i, h := range heads {
		c.write(f.body, title, colX[i]+110, top, 'r', h)
	}
	for r, s := range rows {
		ry := top + (r+1)*rowH
		if r%2 == 0 {
			c.rect(panel, margin-8, ry-18, width-margin+8, ry+8)
		}
		c.write(f.body, title, margin, ry, 'l', s.Point)
		if s.Retrans != nil {
			c.write(f.body, retransTone(*s.Retrans), colX[0]+110, ry, 'r', fmt.Sprintf("%.2f%%", *s.Retrans))
		} else {
			c.write(f.body, muted, colX[0]+110, ry, 'r', "-")
		}
		for i, v := range []*float64{s.Up, s.Down} {
			x := colX[i+1] + 110
			switch {
			case v == nil:
				c.write(f.body, muted, x, ry, 'r', "-")
			case *v == 0:
				c.write(f.body, bad, x, ry, 'r', "失败")
			default:
				c.write(f.body, speedTone(*v), x, ry, 'r', fmt.Sprintf("%.1fMbps", *v))
			}
		}
		if ms, err := strconv.Atoi(strings.TrimSuffix(s.RTT, "ms")); err == nil {
			c.write(f.body, msTone(ms), colX[3]+110, ry, 'r', s.RTT)
		} else {
			c.write(f.body, muted, colX[3]+110, ry, 'r', "-")
		}
		if s.Error != "" {
			c.write(f.small, muted, colX[4], ry, 'r', s.Error)
		}
	}
}
