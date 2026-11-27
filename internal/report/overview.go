package report

import (
	"fmt"
	"image/color"
	"slices"
	"strconv"
	"strings"
)

// column is one cell of the overview's rows: a point's reading of one item.
type column struct {
	head string
	w    int
	cell func(point string) (string, color.Color)
}

// group is the item a run of columns belongs to, named above them.
type group struct {
	name string
	cols []column
}

const (
	pointW   = 100
	lineW    = 130
	msW      = 72
	lossW    = 60
	speedW   = 82
	retransW = 70
	groupGap = 16
)

// overviewGroups are the columns one family's table has, for the items asked.
func overviewGroups(in Input, family int) []group {
	var groups []group
	route, large, udp := in.quality("route", family), in.quality("large", family), in.quality("udp", family)
	if slices.Contains(in.Items, "route") && len(route) > 0 {
		groups = append(groups, group{"回程", []column{
			{"线路", lineW, func(p string) (string, color.Color) { return lineCell(route[p], true) }},
			{"延迟", msW, func(p string) (string, color.Color) { return msCell(route[p]) }},
			{"丢包", lossW, func(p string) (string, color.Color) { return lossCell(route[p]) }},
		}})
	}
	if slices.Contains(in.Items, "large") && len(large) > 0 {
		groups = append(groups, group{"大包回程 1400B", []column{
			{"延迟", msW, func(p string) (string, color.Color) { return msCell(large[p]) }},
			{"丢包", lossW, func(p string) (string, color.Color) { return lossCell(large[p]) }},
		}})
	}
	if slices.Contains(in.Items, "udp") && len(udp) > 0 {
		groups = append(groups, group{"UDP回程", []column{
			{"线路", lineW, func(p string) (string, color.Color) { return lineCell(udp[p], false) }},
		}})
	}
	if !slices.Contains(in.Items, "speed") {
		return groups
	}
	byMode := map[string]map[string]Speed{}
	for _, mode := range in.Modes {
		rows := in.speeds(mode, family)
		if len(rows) == 0 {
			continue
		}
		m := map[string]Speed{}
		for _, s := range rows {
			m[s.Point] = s
		}
		byMode[mode] = m
	}
	if len(byMode) == 0 {
		return groups
	}
	// The latency is the same whichever mode measured it, so it is shown once.
	groups = append(groups, group{"测速延迟", []column{
		{"延迟", msW, func(p string) (string, color.Color) {
			for _, mode := range in.Modes {
				if ms, err := strconv.Atoi(strings.TrimSuffix(byMode[mode][p].RTT, "ms")); err == nil {
					return strconv.Itoa(ms) + "ms", msTone(ms)
				}
			}
			return "-", muted
		}},
	}})
	for _, mode := range in.Modes {
		m, ok := byMode[mode]
		if !ok {
			continue
		}
		groups = append(groups, group{modeNames[mode] + " Mbps", []column{
			{"去程↓", speedW, func(p string) (string, color.Color) { return speedCell(m, p, func(s Speed) *float64 { return s.Down }) }},
			{"回程↑", speedW, func(p string) (string, color.Color) { return speedCell(m, p, func(s Speed) *float64 { return s.Up }) }},
			{"重传", retransW, func(p string) (string, color.Color) {
				s, ok := m[p]
				if !ok || s.Retrans == nil {
					return "-", muted
				}
				return fmt.Sprintf("%.2f%%", *s.Retrans), retransTone(*s.Retrans)
			}},
		}})
	}
	return groups
}

func lineCell(q Quality, numbers bool) (string, color.Color) {
	switch {
	case q.Point == "":
		return "", text
	case q.Line != "":
		return q.Line, text
	case !numbers && q.Error != "":
		return "Hidden", muted
	}
	return "-", muted
}

func failed(q Quality) bool { return q.Loss == nil || q.MS == nil && *q.Loss >= 100 }

func msCell(q Quality) (string, color.Color) {
	switch {
	case q.Point == "":
		return "", text
	case failed(q):
		return "失败", bad
	case q.MS == nil:
		return "-", muted
	}
	return strconv.Itoa(*q.MS) + "ms", msTone(*q.MS)
}

func lossCell(q Quality) (string, color.Color) {
	if q.Point == "" || failed(q) {
		return "", text
	}
	return strconv.Itoa(*q.Loss) + "%", lossTone(*q.Loss)
}

func speedCell(m map[string]Speed, p string, v func(Speed) *float64) (string, color.Color) {
	s, ok := m[p]
	switch {
	case !ok || v(s) == nil:
		return "-", muted
	case *v(s) == 0:
		return "失败", bad
	}
	return fmt.Sprintf("%.1f", *v(s)), speedTone(*v(s))
}

func groupsWidth(groups []group) int {
	w := pointW
	for _, g := range groups {
		w += groupGap
		for _, c := range g.cols {
			w += c.w
		}
	}
	return w
}

// RenderOverview draws the whole report compactly: one table per family,
// a row per point and every item across, so a test of a few points stays
// a short image.
func RenderOverview(in Input) ([]byte, error) {
	type block struct {
		family int
		groups []group
	}
	var blocks []block
	tableW := 0
	for _, family := range in.Families {
		if g := overviewGroups(in, family); len(g) > 0 {
			blocks = append(blocks, block{family, g})
			tableW = max(tableW, groupsWidth(g))
		}
	}
	w := max(width, tableW+2*margin)
	blockH := func(rows int) int { return 30 + 2*rowH + rows*rowH + sectionG }
	bodyH := 0
	for range blocks {
		bodyH += blockH(len(in.Points))
	}
	return render(in, w, bodyH, func(c *canvas, y int) {
		left := (w - tableW) / 2
		for _, b := range blocks {
			drawOverviewTable(c, left, y, tableW, fmt.Sprintf("IPv%d", b.family), in.Points, b.groups)
			y += blockH(len(in.Points))
		}
	})
}

func drawOverviewTable(c *canvas, left, y, tableW int, label string, points []string, groups []group) {
	f := c.faces
	c.write(f.head, title, left+tableW/2, y+18, 'c', label)
	groupY, headY := y+30+rowH-6, y+30+2*rowH-6
	c.write(f.small, title, left, headY, 'l', "测速点")
	x := left + pointW
	for _, g := range groups {
		x += groupGap
		gw := 0
		for _, col := range g.cols {
			gw += col.w
		}
		c.write(f.small, title, x+gw/2, groupY, 'c', g.name)
		c.rect(divider, x+4, groupY+6, x+gw-4, groupY+7)
		cx := x
		for _, col := range g.cols {
			c.write(f.small, muted, cx+col.w/2, headY, 'c', col.head)
			cx += col.w
		}
		x += gw
	}
	for r, p := range points {
		ry := headY + (r+1)*rowH
		if r%2 == 0 {
			c.rect(panel, left-8, ry-18, left+tableW+8, ry+8)
		}
		c.write(f.body, title, left, ry, 'l', p)
		x := left + pointW
		for _, g := range groups {
			x += groupGap
			for _, col := range g.cols {
				if s, tone := col.cell(p); s != "" {
					c.write(f.body, tone, x+col.w/2, ry, 'c', s)
				}
				x += col.w
			}
		}
	}
}
