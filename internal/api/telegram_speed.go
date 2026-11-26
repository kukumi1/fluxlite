package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/kukumi1/fluxlite/internal/model"
	"github.com/kukumi1/fluxlite/internal/service"
	"github.com/kukumi1/fluxlite/internal/store"
)

const telegramSpeedPrefsKey = "telegram.speed_prefs"

// tgQuickProvinces are offered on the settings page; the rest are one tap
// further, on the full province list.
var tgQuickProvinces = []string{"北京", "上海", "广东", "四川"}

var tgSpeedModes = [][2]string{{"single", "单线程"}, {"multi", "多线程"}, {"both", "单+多"}}

var tgSpeedModeNames = map[string]string{"single": "单线程", "multi": "多线程", "both": "单线程 + 多线程"}

// telegramSpeedPrefs is what the last test from the bot used, offered again
// next time.
type telegramSpeedPrefs struct {
	Family    int      `json:"family"`
	Carriers  []string `json:"carriers"`
	Provinces []string `json:"provinces"`
	Mode      string   `json:"mode"`
}

type telegramSpeedDraft struct {
	Scope    telegramScope
	NodeID   int64
	NodeName string
	HasV6    bool
	AllProvs bool
	telegramSpeedPrefs
}

func (d *telegramSpeedDraft) points() []string {
	var out []string
	for _, p := range d.Provinces {
		for _, c := range service.SpeedCarriers {
			if slices.Contains(d.Carriers, c) {
				out = append(out, p+c)
			}
		}
	}
	return out
}

func (d *telegramSpeedDraft) request() service.SpeedRequest {
	return service.SpeedRequest{NodeIDs: []int64{d.NodeID}, Points: d.points(), Mode: d.Mode, Family: d.Family, Origin: "telegram"}
}

func toggleString(list []string, v string) []string {
	if i := slices.Index(list, v); i >= 0 {
		return slices.Delete(slices.Clone(list), i, i+1)
	}
	return append(slices.Clone(list), v)
}

// telegramSpeed handles the speed test flow: "/speed" or "测速" typed, or
// any "sp:" button. Toggles edit the settings message in place rather than
// piling up a new message per tap.
func (s *Server) telegramSpeed(ctx context.Context, cfg telegramConfig, from telegramBotUser, update telegramUpdate, command string) bool {
	cmd := strings.TrimSpace(command)
	callback := update.Callback != nil
	if !callback {
		if cmd != "/speed" && cmd != "测速" {
			return false
		}
		cmd = "sp:menu"
	}
	if !strings.HasPrefix(cmd, "sp:") {
		return false
	}
	opCtx, cancel := context.WithTimeout(ctx, time.Minute)
	text, buttons, edit, err := s.telegramSpeedStep(opCtx, from.ID, cmd)
	cancel()
	if err != nil {
		text = "测速没有开始：" + html.EscapeString(err.Error())
		if errors.Is(err, errTelegramDraft) {
			text = "这一步已经失效，请重新选择。"
		}
		buttons = telegramButtons{{tgButton("🚀 重新选择", "sp:menu"), tgButton("返回菜单", "dashboard")}}
		edit = false
	}
	var editID int64
	if edit && callback && update.Callback.Message != nil {
		editID = update.Callback.Message.MessageID
	}
	replyCtx, replyCancel := context.WithTimeout(ctx, 15*time.Second)
	defer replyCancel()
	if err := s.sendTelegramRich(replyCtx, cfg, from.ID, editID, text, buttons.markup(), true); err != nil {
		s.log.Debug("Telegram speed test reply failed")
	}
	return true
}

func (s *Server) telegramSpeedStep(ctx context.Context, actor int64, cmd string) (string, telegramButtons, bool, error) {
	parts := strings.SplitN(cmd, ":", 3)
	action, arg := parts[1], ""
	if len(parts) == 3 {
		arg = parts[2]
	}
	switch action {
	case "menu":
		return s.telegramSpeedNodes(ctx, 0)
	case "nodes":
		page, err := strconv.Atoi(arg)
		if err != nil || page < 0 {
			return "", nil, false, errTelegramDraft
		}
		return s.telegramSpeedNodes(ctx, page)
	case "node":
		id, err := strconv.ParseInt(arg, 10, 64)
		if err != nil {
			return "", nil, false, errTelegramDraft
		}
		return s.telegramSpeedPick(ctx, actor, id)
	case "fam", "isp", "prov", "mode", "more", "less":
		return s.telegramSpeedToggle(ctx, actor, action, arg)
	case "go":
		return s.telegramSpeedGo(ctx, actor)
	case "cancel":
		id, err := strconv.ParseInt(arg, 10, 64)
		if err != nil {
			return "", nil, false, errTelegramDraft
		}
		return "确定取消这次测速？机器上正在跑的测速会停掉，已经测完的点仍会发结果。",
			telegramButtons{{tgButton("✅ 确定取消", fmt.Sprintf("sp:stop:%d", id)), tgButton("继续测速", fmt.Sprintf("sp:keep:%d", id))}}, true, nil
	case "keep":
		id, err := strconv.ParseInt(arg, 10, 64)
		if err != nil {
			return "", nil, false, errTelegramDraft
		}
		return "⏳ 继续测速中，测完会自动发结果。", telegramButtons{{tgButton("❌ 取消测速", fmt.Sprintf("sp:cancel:%d", id))}}, true, nil
	case "stop":
		id, err := strconv.ParseInt(arg, 10, 64)
		if err != nil {
			return "", nil, false, errTelegramDraft
		}
		if err := s.svc.CancelSpeedtest(ctx, id); err != nil {
			return "这次测速已经结束了。", telegramButtons{{tgButton("返回菜单", "dashboard")}}, true, nil
		}
		s.telegramSpeedAudit(ctx, actor, "speedtest.cancel", fmt.Sprintf("测速 #%d", id), "")
		return "已取消，测完的点马上会发结果。", nil, true, nil
	case "again":
		id, err := strconv.ParseInt(arg, 10, 64)
		if err != nil {
			return "", nil, false, errTelegramDraft
		}
		return s.telegramSpeedAgain(ctx, actor, id)
	}
	return "", nil, false, errTelegramDraft
}

const tgSpeedNodesPerPage = 10

func (s *Server) telegramSpeedNodes(ctx context.Context, page int) (string, telegramButtons, bool, error) {
	nodes, err := s.svc.Store().ListNodes(ctx)
	if err != nil {
		return "", nil, false, err
	}
	var online []*model.Node
	for _, n := range nodes {
		if n.Status == model.StatusOnline {
			online = append(online, n)
		}
	}
	if len(online) == 0 {
		return "没有在线的机器可以测速。", telegramButtons{{tgButton("返回菜单", "dashboard")}}, true, nil
	}
	pages := (len(online) + tgSpeedNodesPerPage - 1) / tgSpeedNodesPerPage
	page = min(page, pages-1)
	shown := online[page*tgSpeedNodesPerPage : min(len(online), (page+1)*tgSpeedNodesPerPage)]
	b := telegramButtons{}
	for i := 0; i < len(shown); i += 2 {
		row := []map[string]any{}
		for _, n := range shown[i:min(i+2, len(shown))] {
			row = append(row, tgButton(telegramFlag(n.Name)+telegramDisplayName(n.Name), fmt.Sprintf("sp:node:%d", n.ID)))
		}
		b = append(b, row)
	}
	nav := []map[string]any{}
	if page > 0 {
		nav = append(nav, tgButton("‹ 上一页", fmt.Sprintf("sp:nodes:%d", page-1)))
	}
	if page+1 < pages {
		nav = append(nav, tgButton("下一页 ›", fmt.Sprintf("sp:nodes:%d", page+1)))
	}
	if len(nav) > 0 {
		b = append(b, nav)
	}
	b = append(b, []map[string]any{tgButton("返回菜单", "dashboard")})
	return "🚀 <b>测速</b>\n选择要测速的机器：", b, true, nil
}

func (s *Server) telegramSpeedPrefs(ctx context.Context) telegramSpeedPrefs {
	prefs := telegramSpeedPrefs{Family: 4, Carriers: slices.Clone(service.SpeedCarriers), Provinces: []string{"北京", "上海", "广东"}, Mode: "single"}
	raw, ok, err := s.svc.Store().Setting(ctx, telegramSpeedPrefsKey)
	if err != nil || !ok {
		return prefs
	}
	var saved telegramSpeedPrefs
	if json.Unmarshal([]byte(raw), &saved) != nil {
		return prefs
	}
	return saved
}

func (s *Server) telegramSpeedPick(ctx context.Context, actor, nodeID int64) (string, telegramButtons, bool, error) {
	node, err := s.svc.Store().NodeByID(ctx, nodeID)
	if err != nil {
		return "", nil, false, err
	}
	prefs := s.telegramSpeedPrefs(ctx)
	t := s.telegram
	t.mu.Lock()
	defer t.mu.Unlock()
	scope, err := s.newTelegramScope(ctx, actor)
	if err != nil {
		return "", nil, false, err
	}
	d := &telegramSpeedDraft{Scope: scope, NodeID: node.ID, NodeName: node.Name, HasV6: node.IPv6Address != "", telegramSpeedPrefs: prefs}
	if !d.HasV6 {
		d.Family = 4
	}
	t.speedDrafts[actor] = d
	text, b := telegramSpeedSettings(d)
	return text, b, true, nil
}

func (s *Server) telegramSpeedDraftFor(ctx context.Context, actor int64) (*telegramSpeedDraft, error) {
	d := s.telegram.speedDrafts[actor]
	if d == nil || !s.telegramScopeValid(ctx, d.Scope) {
		delete(s.telegram.speedDrafts, actor)
		return nil, errTelegramDraft
	}
	return d, nil
}

func (s *Server) telegramSpeedToggle(ctx context.Context, actor int64, action, arg string) (string, telegramButtons, bool, error) {
	t := s.telegram
	t.mu.Lock()
	defer t.mu.Unlock()
	d, err := s.telegramSpeedDraftFor(ctx, actor)
	if err != nil {
		return "", nil, false, err
	}
	switch action {
	case "fam":
		if arg == "6" && d.HasV6 {
			d.Family = 6
		} else {
			d.Family = 4
		}
	case "isp":
		if slices.Contains(service.SpeedCarriers, arg) {
			d.Carriers = toggleString(d.Carriers, arg)
		}
	case "prov":
		if slices.Contains(service.SpeedProvinces, arg) {
			d.Provinces = toggleString(d.Provinces, arg)
		}
	case "mode":
		if _, ok := tgSpeedModeNames[arg]; ok {
			d.Mode = arg
		}
	case "more":
		d.AllProvs = true
	case "less":
		d.AllProvs = false
	}
	text, b := telegramSpeedSettings(d)
	return text, b, true, nil
}

func tgCheck(on bool, label string) string {
	if on {
		return "✅ " + label
	}
	return label
}

func telegramSpeedSettings(d *telegramSpeedDraft) (string, telegramButtons) {
	points := d.points()
	var b strings.Builder
	fmt.Fprintf(&b, "🚀 <b>测速 · %s</b>\n%s\n", html.EscapeString(d.NodeName), html.EscapeString(telegramSpeedSummary(d.Family, d.Carriers, d.Provinces, d.Mode)))
	switch {
	case len(points) == 0:
		b.WriteString("\n至少选一个运营商和一个省份。")
	case len(points) > 9:
		fmt.Fprintf(&b, "\n⚠️ 共 %d 个测速点，一次最多 9 个，请少选一些。", len(points))
	default:
		runs := len(points)
		if d.Mode == "both" {
			runs *= 2
		}
		fmt.Fprintf(&b, "\n共 %d 个测速点，跑 %d 次，大约 %d 分钟；单线程每次几百 MB，多线程可能上 GB。", len(points), runs, max(1, runs*25/60))
	}

	rows := telegramButtons{}
	fam := []map[string]any{tgButton(tgCheck(d.Family == 4, "IPv4"), "sp:fam:4")}
	if d.HasV6 {
		fam = append(fam, tgButton(tgCheck(d.Family == 6, "IPv6"), "sp:fam:6"))
	}
	rows = append(rows, fam)
	isp := []map[string]any{}
	for _, c := range service.SpeedCarriers {
		isp = append(isp, tgButton(tgCheck(slices.Contains(d.Carriers, c), c), "sp:isp:"+c))
	}
	rows = append(rows, isp)

	provinces := tgQuickProvinces
	if d.AllProvs {
		provinces = service.SpeedProvinces
	} else {
		for _, p := range d.Provinces {
			if !slices.Contains(provinces, p) {
				provinces = append(slices.Clone(provinces), p)
			}
		}
	}
	for i := 0; i < len(provinces); i += 4 {
		row := []map[string]any{}
		for _, p := range provinces[i:min(i+4, len(provinces))] {
			row = append(row, tgButton(tgCheck(slices.Contains(d.Provinces, p), p), "sp:prov:"+p))
		}
		rows = append(rows, row)
	}
	if d.AllProvs {
		rows = append(rows, []map[string]any{tgButton("‹ 收起省份", "sp:less")})
	} else {
		rows = append(rows, []map[string]any{tgButton("更多省份 ›", "sp:more")})
	}
	mode := []map[string]any{}
	for _, m := range tgSpeedModes {
		mode = append(mode, tgButton(tgCheck(d.Mode == m[0], m[1]), "sp:mode:"+m[0]))
	}
	rows = append(rows, mode)
	rows = append(rows, []map[string]any{tgButton("▶️ 开始", "sp:go"), tgButton("‹ 换机器", "sp:menu")})
	return b.String(), rows
}

// telegramSpeedSummary is the settings line shared by the settings page,
// the running notice and the result: 协议：IPv4 | 运营商：… | 测速点：… | 模式.
func telegramSpeedSummary(family int, carriers, provinces []string, mode string) string {
	isp := make([]string, 0, len(carriers))
	for _, c := range service.SpeedCarriers {
		if slices.Contains(carriers, c) {
			isp = append(isp, c)
		}
	}
	return fmt.Sprintf("协议：IPv%d | 运营商：%s | 测速点：%s | %s",
		family, orDash(strings.Join(isp, "、")), orDash(strings.Join(provinces, "、")), tgSpeedModeNames[mode])
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

// splitPoints recovers the provinces and carriers a request's points came
// from, in the order the points were asked for.
func splitPoints(points []string) (provinces, carriers []string) {
	for _, p := range points {
		for _, c := range service.SpeedCarriers {
			if prov, ok := strings.CutSuffix(p, c); ok {
				if !slices.Contains(provinces, prov) {
					provinces = append(provinces, prov)
				}
				if !slices.Contains(carriers, c) {
					carriers = append(carriers, c)
				}
			}
		}
	}
	return provinces, carriers
}

func (s *Server) telegramSpeedGo(ctx context.Context, actor int64) (string, telegramButtons, bool, error) {
	t := s.telegram
	t.mu.Lock()
	d, err := s.telegramSpeedDraftFor(ctx, actor)
	if err != nil {
		t.mu.Unlock()
		return "", nil, false, err
	}
	if n := len(d.points()); n == 0 || n > 9 {
		text, b := telegramSpeedSettings(d)
		t.mu.Unlock()
		return text, b, true, nil
	}
	draft := *d
	delete(t.speedDrafts, actor)
	t.mu.Unlock()

	if raw, err := json.Marshal(draft.telegramSpeedPrefs); err == nil {
		if err := s.svc.Store().SetSetting(ctx, telegramSpeedPrefsKey, string(raw)); err != nil {
			s.log.Warn("save Telegram speed test choices", "error", err)
		}
	}
	return s.telegramSpeedStart(ctx, actor, draft.NodeName, draft.request())
}

func (s *Server) telegramSpeedStart(ctx context.Context, actor int64, name string, req service.SpeedRequest) (string, telegramButtons, bool, error) {
	id, err := s.svc.StartSpeedtest(ctx, req)
	if err != nil {
		return "", nil, false, err
	}
	s.telegramSpeedAudit(ctx, actor, "speedtest.run", "1 台机器",
		fmt.Sprintf("IPv%d %s：%s", req.Family, req.Mode, strings.Join(req.Points, "、")))
	provinces, carriers := splitPoints(req.Points)
	runs := len(req.Points)
	if req.Mode == "both" {
		runs *= 2
	}
	text := fmt.Sprintf("⏳ <b>测速中：%s</b>\n%s\n测完会自动发结果，大约 %d 分钟。",
		html.EscapeString(name), html.EscapeString(telegramSpeedSummary(req.Family, carriers, provinces, req.Mode)), max(1, runs*25/60))
	return text, telegramButtons{{tgButton("❌ 取消测速", fmt.Sprintf("sp:cancel:%d", id))}}, true, nil
}

func (s *Server) telegramSpeedAgain(ctx context.Context, actor, jobID int64) (string, telegramButtons, bool, error) {
	job, err := s.speedJob(ctx, jobID)
	if err != nil {
		return "", nil, false, err
	}
	req := job.Request
	req.Origin = "telegram"
	name := ""
	if len(req.NodeIDs) == 1 {
		if n, err := s.svc.Store().NodeByID(ctx, req.NodeIDs[0]); err == nil {
			name = n.Name
		}
	}
	return s.telegramSpeedStart(ctx, actor, name, req)
}

func (s *Server) telegramSpeedAudit(ctx context.Context, actor int64, action, target, detail string) {
	auditCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), auditWriteTimeout)
	defer cancel()
	if err := s.svc.Store().AppendAudit(auditCtx, &model.AuditLog{Actor: fmt.Sprintf("telegram:%d", actor), Action: action, Target: target, Detail: detail}); err != nil {
		s.log.Error("write Telegram speed test audit", "error", err)
	}
}

func (s *Server) speedJob(ctx context.Context, id int64) (*service.SpeedJobView, error) {
	overview, err := s.svc.SpeedOverview(ctx)
	if err != nil {
		return nil, err
	}
	for i := range overview.Jobs {
		if overview.Jobs[i].ID == id {
			return &overview.Jobs[i], nil
		}
	}
	return nil, store.ErrNotFound
}

// telegramSpeedDone sends a test started from the bot its result there.
func (s *Server) telegramSpeedDone(ctx context.Context, jobID int64, req service.SpeedRequest) {
	if req.Origin != "telegram" {
		return
	}
	job, err := s.speedJob(ctx, jobID)
	if err != nil {
		s.log.Warn("read finished speed test", "job", jobID, "error", err)
		return
	}
	buttons := telegramButtons{{tgButton("🔁 再测一次", fmt.Sprintf("sp:again:%d", jobID)), tgButton("📋 返回菜单", "dashboard")}}
	for _, id := range job.Request.NodeIDs {
		name := fmt.Sprintf("机器 #%d", id)
		if n, err := s.svc.Store().NodeByID(ctx, id); err == nil {
			name = n.Name
		}
		if err := s.notifyTelegramRich(ctx, telegramSpeedReport(job, id, name), buttons); err != nil {
			s.log.Warn("send speed test result to Telegram", "job", jobID, "error", err)
		}
	}
}

// telegramSpeedReport lays a machine's result out like the plugin it was
// modelled on: settings, speeds in a copyable block, retransmits in another,
// then traffic and time.
func telegramSpeedReport(job *service.SpeedJobView, nodeID int64, name string) string {
	req := job.Request
	provinces, carriers := splitPoints(req.Points)
	var run store.SpeedRun
	for _, r := range job.Runs {
		if r.NodeID == nodeID {
			run = r
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s<b>测速 | %s</b>\n%s\n", telegramFlag(name), html.EscapeString(name),
		html.EscapeString(telegramSpeedSummary(req.Family, carriers, provinces, req.Mode)))
	if run.Egress != "" {
		fmt.Fprintf(&b, "出口：%s\n", html.EscapeString(run.Egress))
	}
	if job.Status == "cancelled" {
		b.WriteString("⚠️ 已取消，只列出测完的点\n")
	}
	if run.Error != "" && run.Error != "已取消" {
		fmt.Fprintf(&b, "⚠️ %s\n", html.EscapeString(run.Error))
	}

	showSingle, showMulti := req.Mode != "multi", req.Mode != "single"
	head := []string{fmt.Sprintf("IPv%d", req.Family), "延迟"}
	if showSingle {
		head = append(head, "单↓", "单↑")
	}
	if showMulti {
		head = append(head, "多↓", "多↑")
	}
	var speedRows, retransRows [][]string
	var notes []string
	find := func(point, mode string) *store.SpeedResult {
		for i := range job.Results {
			r := &job.Results[i]
			if r.NodeID == nodeID && r.Point == point && r.Mode == mode {
				return r
			}
		}
		return nil
	}
	for _, point := range req.Points {
		single, multi := find(point, "single"), find(point, "multi")
		if single == nil && multi == nil {
			continue
		}
		first := single
		if first == nil {
			first = multi
		}
		row := []string{point, orDash(strings.TrimSuffix(first.RTT, "ms"))}
		if showSingle {
			row = append(row, speedCell(single, true), speedCell(single, false))
		}
		if showMulti {
			row = append(row, speedCell(multi, true), speedCell(multi, false))
		}
		speedRows = append(speedRows, row)
		var retrans *float64
		for _, r := range []*store.SpeedResult{single, multi} {
			if r == nil {
				continue
			}
			if retrans == nil {
				retrans = r.Retrans
			}
			if r.Error != "" {
				notes = append(notes, fmt.Sprintf("%s %s：%s", point, tgSpeedModeNames[r.Mode], r.Error))
			}
		}
		if retrans != nil {
			retransRows = append(retransRows, []string{fmt.Sprintf("%s v%d", point, req.Family), fmt.Sprintf("%.2f%%", *retrans)})
		}
	}

	if len(speedRows) > 0 {
		b.WriteString("\n🚀 <b>速度（Mbps）</b>\n<pre>")
		b.WriteString(html.EscapeString(monoTable(head, speedRows)))
		b.WriteString("</pre>\n↓ 去程（中国→机器）　↑ 回程（机器→中国）\n延迟单位 ms\n")
	} else {
		b.WriteString("\n没有测到结果。\n")
	}
	if len(retransRows) > 0 {
		b.WriteString("\n🔁 <b>回程 TCP 重传率（测速期间，机器→中国）</b>\n<pre>")
		b.WriteString(html.EscapeString(monoTable([]string{"测速点", "回程重传"}, retransRows)))
		b.WriteString("</pre>\n")
	}
	if len(notes) > 0 {
		b.WriteString("\n" + html.EscapeString(strings.Join(notes, "\n")) + "\n")
	}
	if run.TrafficBytes > 0 {
		fmt.Fprintf(&b, "\n📦 消耗流量：共 %s（下载 %s / 上传 %s）\n",
			telegramTrafficBytes(float64(run.TrafficBytes)), telegramTrafficBytes(float64(run.RxBytes)), telegramTrafficBytes(float64(run.TxBytes)))
	}
	end := time.Unix(job.FinishedAt, 0)
	if run.FinishedAt > 0 {
		end = time.Unix(run.FinishedAt, 0)
	}
	if run.StartedAt > 0 && run.FinishedAt >= run.StartedAt {
		fmt.Fprintf(&b, "<i>耗时 %d 秒 | %s (UTC+8)</i>", run.FinishedAt-run.StartedAt, end.In(telegramZone).Format("2006-01-02 15:04"))
	} else {
		fmt.Fprintf(&b, "<i>%s (UTC+8)</i>", end.In(telegramZone).Format("2006-01-02 15:04"))
	}
	return b.String()
}

var telegramZone = time.FixedZone("UTC+8", 8*3600)

func speedCell(r *store.SpeedResult, down bool) string {
	if r == nil {
		return "-"
	}
	v := r.Up
	if down {
		v = r.Down
	}
	switch {
	case v == nil:
		return "-"
	case *v == 0:
		return "失败"
	case *v >= 100:
		return strconv.FormatFloat(*v, 'f', 0, 64)
	default:
		return strconv.FormatFloat(*v, 'f', 1, 64)
	}
}

// displayWidth counts a CJK character as two columns, as Telegram's
// monospace font draws it.
func displayWidth(s string) int {
	w := 0
	for _, r := range s {
		if r >= 0x2E80 {
			w += 2
		} else {
			w++
		}
	}
	return w
}

// monoTable left-aligns the first column and right-aligns the rest.
func monoTable(head []string, rows [][]string) string {
	widths := make([]int, len(head))
	for _, row := range append([][]string{head}, rows...) {
		for i, cell := range row {
			widths[i] = max(widths[i], displayWidth(cell))
		}
	}
	var b strings.Builder
	for _, row := range append([][]string{head}, rows...) {
		for i, cell := range row {
			pad := strings.Repeat(" ", widths[i]-displayWidth(cell))
			switch {
			case i == 0:
				b.WriteString(cell + pad)
			default:
				b.WriteString("  " + pad + cell)
			}
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

var telegramFlagCodes = []string{"JP", "HK", "TW", "SG", "US", "KR", "DE", "GB", "FR", "NL", "CN", "MO", "AU", "CA", "RU", "MY", "TH", "VN", "PH", "IN", "TR", "AE", "CH", "SE", "PL", "IT", "ES", "BR"}

// telegramFlag is the flag of the region a machine is named after, followed
// by a space, or nothing when the name says no region.
func telegramFlag(name string) string {
	tokens := strings.FieldsFunc(strings.ToUpper(name), func(r rune) bool { return (r < 'A' || r > 'Z') && (r < '0' || r > '9') })
	for _, code := range telegramFlagCodes {
		if slices.Contains(tokens, code) {
			flag := ""
			for _, r := range code {
				flag += string(rune(0x1F1E6 + r - 'A'))
			}
			return flag + " "
		}
	}
	return ""
}
