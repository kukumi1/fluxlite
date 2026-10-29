package api

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/kukumi1/fluxlite/internal/cryptox"
	"github.com/kukumi1/fluxlite/internal/model"
	"github.com/kukumi1/fluxlite/internal/service"
)

const telegramDraftTTL = 10 * time.Minute

var errTelegramDraft = errors.New("操作已失效，请重新从菜单开始")

type telegramScope struct {
	Actor, Owner int64
	Generation   uint64
	Credentials  [32]byte
	Expires      time.Time
}
type telegramWizard struct {
	Scope              telegramScope
	Nonce, Kind, Stage string
	Page               int
	Route              service.RouteInput
	Proxy              service.SingBoxUserInput
	Node               service.NodeInput
}
type telegramConfirmation struct {
	Scope      telegramScope
	Action     string
	ID         int64
	Identity   [32]byte
	Wizard     telegramWizard
	Nodes      map[int64][32]byte
	Dependents [32]byte
}
type telegramMachineDraft struct {
	Scope telegramScope
	Input service.NodeInput
}
type telegramButtons [][]map[string]any

func tgButton(label, data string) map[string]any {
	return map[string]any{"text": label, "callback_data": data}
}
func (b telegramButtons) markup() map[string]any { return map[string]any{"inline_keyboard": b} }
func tgCancel() telegramButtons                  { return telegramButtons{{tgButton("取消", "cancel")}} }

// All scope checks and one-shot consumption occur under telegram.mu. Service
// mutations deliberately run outside that lock and use bounded contexts.
func (s *Server) telegramScopeValid(ctx context.Context, scope telegramScope) bool {
	t := s.telegram
	if !t.cfg.Enabled || t.cfg.UserID != scope.Owner || t.cfg.BoundUserID != scope.Actor || t.generation != scope.Generation || !scope.Expires.After(time.Now()) {
		return false
	}
	owner, err := s.svc.Store().UserByID(ctx, scope.Owner)
	return err == nil && !owner.Locked(time.Now()) && telegramCredentialsFingerprint(owner) == scope.Credentials
}
func (s *Server) newTelegramScope(ctx context.Context, actor int64) (telegramScope, error) {
	t := s.telegram
	owner, err := s.svc.Store().UserByID(ctx, t.cfg.UserID)
	if err != nil || owner.Locked(time.Now()) || !t.cfg.Enabled || t.cfg.BoundUserID != actor {
		return telegramScope{}, errTelegramDraft
	}
	return telegramScope{Actor: actor, Owner: owner.ID, Generation: t.generation, Credentials: telegramCredentialsFingerprint(owner), Expires: time.Now().Add(telegramDraftTTL)}, nil
}
func (s *Server) telegramDirect(ctx context.Context, cfg telegramConfig, from telegramBotUser, command string, callback bool) bool {
	operationCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	text, buttons, handled, err := s.telegramDispatch(operationCtx, from, strings.TrimSpace(command), callback)
	cancel()
	if !handled {
		return false
	}
	if err != nil {
		text = "操作未完成或已失效，请重新选择。若涉及远端操作，请在面板核实状态后再试。"
		if errors.Is(err, errRouteDeploy) {
			text = "链路部署未完成，可能存在远端残留服务。请在面板检查各跳点状态后再试。"
		}
		if errors.Is(err, errRouteQuota) {
			text = "链路因流量配额暂停或额度已用尽，不能直接部署。请先在面板检查额度与计费周期。"
			if errors.Is(err, errRouteDeploy) {
				text += "本次部署可能存在远端残留服务，请核实并停止。"
			}
		}
		if errors.Is(err, service.ErrNodeInUse) {
			text = "无法删除：机器仍被链路或跳板引用，请先解除依赖。"
		}
		buttons = telegramButtons{{tgButton("返回菜单", "dashboard")}}
	}
	replyCtx, replyCancel := context.WithTimeout(ctx, 15*time.Second)
	defer replyCancel()
	if s.sendTelegramReply(replyCtx, cfg, from.ID, text, buttons.markup()) != nil {
		s.log.Debug("Telegram message delivery failed")
	}
	return true
}
func (s *Server) telegramDispatch(ctx context.Context, from telegramBotUser, command string, callback bool) (string, telegramButtons, bool, error) {
	t := s.telegram
	t.mu.Lock()
	scope, err := s.newTelegramScope(ctx, from.ID)
	if err != nil {
		t.mu.Unlock()
		return "", nil, true, err
	}
	for actor, w := range t.wizards {
		if !w.Scope.Expires.After(time.Now()) {
			delete(t.wizards, actor)
		}
	}
	for key, c := range t.confirmations {
		if !c.Scope.Expires.After(time.Now()) {
			delete(t.confirmations, key)
		}
	}
	for key, d := range t.machineDrafts {
		if !d.Scope.Expires.After(time.Now()) {
			delete(t.machineDrafts, key)
		}
	}
	if command == "/cancel" || command == "cancel" {
		delete(t.wizards, from.ID)
		for k, c := range t.confirmations {
			if c.Scope.Actor == from.ID {
				delete(t.confirmations, k)
			}
		}
		for k, d := range t.machineDrafts {
			if d.Scope.Actor == from.ID {
				delete(t.machineDrafts, k)
			}
		}
		t.mu.Unlock()
		return "已取消。", telegramButtons{{tgButton("返回菜单", "dashboard")}}, true, nil
	}
	if callback && strings.HasPrefix(command, "confirm:") {
		nonce := strings.TrimPrefix(command, "confirm:")
		c, ok := t.confirmations[nonce]
		if !ok || c.Scope.Actor != from.ID || !s.telegramScopeValid(ctx, c.Scope) {
			t.mu.Unlock()
			return "", nil, true, errTelegramDraft
		}
		delete(t.confirmations, nonce)
		t.mu.Unlock()
		text, err := s.telegramExecute(ctx, from, c)
		return text, telegramButtons{{tgButton("返回菜单", "dashboard")}}, true, err
	}
	if callback && strings.HasPrefix(command, "act:") {
		p := strings.Split(command, ":")
		if len(p) != 4 {
			t.mu.Unlock()
			return "", nil, true, errTelegramDraft
		}
		id, e := strconv.ParseInt(p[3], 10, 64)
		if e != nil || id <= 0 {
			t.mu.Unlock()
			return "", nil, true, errTelegramDraft
		}
		identity, label, e := s.telegramResource(ctx, p[1], id)
		if e != nil {
			t.mu.Unlock()
			return "", nil, true, e
		}
		action := p[1] + ":" + p[2]
		switch action {
		case "routes:delete", "routes:apply", "routes:stop", "nodes:delete", "singbox:delete", "singbox:start", "singbox:stop":
		default:
			t.mu.Unlock()
			return "", nil, true, errTelegramDraft
		}
		var dependents [32]byte
		warning := "将改变远端服务状态。"
		if p[2] == "delete" {
			warning = "永久删除，无法撤销。链路会清理远端服务；机器会清理其托管代理节点并删除凭据（被链路/跳板引用时拒绝）；代理节点会停止并删除配置。"
		}
		if action == "nodes:delete" {
			users, e := s.svc.Store().ListSingBoxUsersOnNode(ctx, id)
			if e != nil {
				t.mu.Unlock()
				return "", nil, true, e
			}
			dependents, e = telegramDependentsIdentity(users)
			if e != nil {
				t.mu.Unlock()
				return "", nil, true, e
			}
			managed := 0
			names := []string{}
			for _, u := range users {
				if u.Source == model.SingBoxManaged {
					managed++
					names = append(names, fmt.Sprintf("%s (#%d)", telegramDisplayName(u.Name), u.ID))
				}
			}
			warning = fmt.Sprintf("永久删除机器及其凭据，并清理 %d 个托管代理节点；外部节点记录会随机器删除，但不卸载外部服务。被链路或跳板引用时拒绝删除。无法撤销。\n托管节点：%s", managed, strings.Join(names, "、"))
		}
		if action == "singbox:delete" {
			u, e := s.svc.Store().SingBoxUserByID(ctx, id)
			if e != nil {
				t.mu.Unlock()
				return "", nil, true, e
			}
			if u.Source == model.SingBoxExternal {
				warning = "仅删除扫描记录，不停止、不卸载远端外部服务。记录删除无法撤销。"
			} else {
				warning = "停止并卸载托管代理服务，删除远端配置及节点记录。无法撤销。"
			}
		}
		actionLabels := map[string]string{"routes:delete": "删除链路", "routes:apply": "部署链路", "routes:stop": "停止链路", "nodes:delete": "删除机器", "singbox:delete": "删除代理节点", "singbox:start": "启用代理节点", "singbox:stop": "停止代理节点"}
		text, buttons, e := s.telegramConfirm(ctx, telegramConfirmation{Scope: scope, Action: action, ID: id, Identity: identity, Dependents: dependents}, "确认 "+actionLabels[action]+"："+label+"\n"+warning)
		t.mu.Unlock()
		return text, buttons, true, e
	}
	if strings.HasPrefix(command, "add:") && callback {
		kind := strings.TrimPrefix(command, "add:")
		if kind != "routes" && kind != "nodes" && kind != "singbox" {
			t.mu.Unlock()
			return "", nil, true, errTelegramDraft
		}
		nonce, e := cryptox.RandomToken(9)
		if e != nil {
			t.mu.Unlock()
			return "", nil, true, e
		}
		w := &telegramWizard{Scope: scope, Nonce: nonce, Kind: kind, Stage: "name"}
		if kind == "singbox" {
			w.Stage = "machine"
		}
		t.wizards[from.ID] = w
		text, b, e := s.telegramPrompt(ctx, w)
		t.mu.Unlock()
		return text, b, true, e
	}
	if w := t.wizards[from.ID]; w != nil && ((!callback && !strings.HasPrefix(command, "/")) || strings.HasPrefix(command, "wiz:")) {
		if !s.telegramScopeValid(ctx, w.Scope) {
			delete(t.wizards, from.ID)
			t.mu.Unlock()
			return "", nil, true, errTelegramDraft
		}
		value := command
		if callback {
			p := strings.SplitN(command, ":", 4)
			if len(p) != 4 || p[1] != w.Nonce || p[2] != w.Stage {
				t.mu.Unlock()
				return "", nil, true, errTelegramDraft
			}
			value = p[3]
		}
		text, b, e := s.telegramAdvance(ctx, w, value, callback)
		t.mu.Unlock()
		return text, b, true, e
	}
	if callback && strings.HasPrefix(command, "wiz:") {
		t.mu.Unlock()
		return "", nil, true, errTelegramDraft
	}
	panelURL := t.cfg.PanelURL
	t.mu.Unlock()
	page := strings.TrimPrefix(command, "/")
	if page == "dashboard" || page == "start" || strings.HasPrefix(page, "traffic:") {
		if page == "dashboard" || page == "start" {
			page = "traffic:0"
		}
		n, e := strconv.Atoi(strings.TrimPrefix(page, "traffic:"))
		if e != nil || n < 0 {
			return "", nil, true, errTelegramDraft
		}
		text, totalPages, e := s.telegramTrafficPage(ctx, n)
		if n >= totalPages {
			n = totalPages - 1
		}
		if n < 0 {
			n = 0
		}
		b := telegramButtons{}
		if n > 0 {
			b = append(b, []map[string]any{tgButton("上一页", fmt.Sprintf("traffic:%d", n-1))})
		}
		if n+1 < totalPages {
			b = append(b, []map[string]any{tgButton("下一页", fmt.Sprintf("traffic:%d", n+1))})
		}
		b = append(b, telegramMenu(panelURL, "dashboard")["inline_keyboard"].([][]map[string]any)...)
		return text, b, true, e
	}
	p := strings.Split(page, ":")
	if p[0] == "routes" || p[0] == "nodes" || p[0] == "singbox" {
		n := 0
		if len(p) == 2 {
			n, err = strconv.Atoi(p[1])
			if err != nil || n < 0 {
				return "", nil, true, errTelegramDraft
			}
		}
		text, b, e := s.telegramList(ctx, p[0], n)
		return text, b, true, e
	}
	return "", nil, false, nil
}

func (s *Server) telegramConfirm(ctx context.Context, c telegramConfirmation, summary string) (string, telegramButtons, error) {
	ids := append([]int64{}, c.Wizard.Route.NodeIDs...)
	if c.ID > 0 && strings.HasPrefix(c.Action, "routes:") {
		r, e := s.svc.Store().RouteByID(ctx, c.ID)
		if e != nil {
			return "", nil, e
		}
		for _, h := range r.Hops {
			ids = append(ids, h.NodeID)
		}
	}
	if c.ID > 0 && strings.HasPrefix(c.Action, "singbox:") {
		u, e := s.svc.Store().SingBoxUserByID(ctx, c.ID)
		if e != nil {
			return "", nil, e
		}
		ids = append(ids, u.NodeID)
	}
	if c.Wizard.Proxy.NodeID > 0 {
		ids = append(ids, c.Wizard.Proxy.NodeID)
	}
	c.Nodes = map[int64][32]byte{}
	for _, id := range ids {
		identity, _, e := s.telegramResource(ctx, "nodes", id)
		if e != nil {
			return "", nil, e
		}
		c.Nodes[id] = identity
	}
	nonce, err := cryptox.RandomToken(24)
	if err != nil {
		return "", nil, err
	}
	s.telegram.confirmations[nonce] = c
	return summary + "\n确认仅可使用一次，10 分钟内有效。", telegramButtons{{tgButton("确认执行", "confirm:"+nonce), tgButton("取消", "cancel")}}, nil
}
func (s *Server) telegramResource(ctx context.Context, kind string, id int64) ([32]byte, string, error) {
	var v any
	var name string
	var err error
	switch kind {
	case "routes":
		r, e := s.svc.Store().RouteByID(ctx, id)
		err = e
		if e == nil {
			hops := make([]model.RouteHop, len(r.Hops))
			for i, h := range r.Hops {
				hops[i] = model.RouteHop{RouteID: h.RouteID, HopOrder: h.HopOrder, NodeID: h.NodeID, RelayPort: h.RelayPort}
			}
			r.Hops = hops
			r.UpdatedAt = time.Time{}
			v = r
			name = r.Name
		}
	case "nodes":
		n, e := s.svc.Store().NodeByID(ctx, id)
		err = e
		if e == nil {
			v = map[string]any{"id": n.ID, "name": n.Name, "host": n.Host, "user": n.SSHUser, "port": n.SSHPort, "created": n.CreatedAt, "auth_type": n.AuthType, "secret_hash": sha256.Sum256(n.AuthSecret), "via": n.ViaNodeID, "host_key": n.HostKey, "port_start": n.PortStart, "port_end": n.PortEnd}

			name = n.Name
		}
	case "singbox":
		u, e := s.svc.Store().SingBoxUserByID(ctx, id)
		err = e
		if e == nil {
			v = telegramProxyIdentity(u)
			name = u.Name
		}
	default:
		err = errTelegramDraft
	}
	if err != nil {
		return [32]byte{}, "", err
	}
	raw, err := json.Marshal(v)
	return sha256.Sum256(raw), fmt.Sprintf("%s (#%d)", telegramDisplayName(name), id), err
}
func (s *Server) telegramExecute(ctx context.Context, from telegramBotUser, c telegramConfirmation) (string, error) {
	return s.telegramExecuteWithApply(ctx, from, c, s.svc.ApplyRoute)
}

func (s *Server) telegramExecuteWithApply(ctx context.Context, from telegramBotUser, c telegramConfirmation, apply routeApplier) (string, error) {
	t := s.telegram
	t.mu.Lock()
	valid := s.telegramScopeValid(ctx, c.Scope) && from.ID == c.Scope.Actor
	t.mu.Unlock()
	if !valid {
		return "", errTelegramDraft
	}
	if c.ID > 0 {
		identity, _, err := s.telegramResource(ctx, strings.Split(c.Action, ":")[0], c.ID)
		if err != nil || identity != c.Identity {
			return "", errTelegramDraft
		}
	}
	for id, expected := range c.Nodes {
		actual, _, e := s.telegramResource(ctx, "nodes", id)
		if e != nil || actual != expected {
			return "", errTelegramDraft
		}
	}
	if c.Action == "nodes:delete" {
		actual, e := s.telegramNodeDependents(ctx, c.ID)
		if e != nil || actual != c.Dependents {
			return "", errTelegramDraft
		}
	}
	owner, ownerErr := s.svc.Store().UserByID(ctx, c.Scope.Owner)
	if ownerErr != nil {
		return "", errTelegramDraft
	}
	targetID := c.ID
	var err error
	text := "操作完成。"
	switch c.Action {
	case "routes:create":
		var r *model.Route
		r, err = s.svc.CreateRoute(ctx, c.Wizard.Route)
		if r != nil {
			targetID = r.ID
		}
		if err == nil {
			text = fmt.Sprintf("链路 #%d 已创建，尚未部署。请在链路菜单单独点击部署。", r.ID)
		}
	case "singbox:create":
		var u *model.SingBoxUser
		u, err = s.svc.CreateSingBoxUser(ctx, c.Wizard.Proxy)
		if u != nil {
			targetID = u.ID
		}
	case "routes:delete":
		var leftovers []service.RouteLeftover
		leftovers, err = s.svc.DeleteRoute(ctx, c.ID)
		if len(leftovers) > 0 {
			text = fmt.Sprintf("链路已删除，但 %d 个远端服务可能残留，请在面板检查并清理。", len(leftovers))
		}
	case "routes:apply":
		_, err = s.startRoute(ctx, c.ID, apply)
		if err == nil {
			text = "链路部署成功，已启用自动维护与流量配额控制。"
		}
	case "routes:stop":
		err = s.svc.StopRoute(ctx, c.ID)
	case "nodes:delete":
		err = s.svc.DeleteNode(ctx, c.ID)
	case "singbox:delete":
		err = s.svc.DeleteSingBoxUser(ctx, c.ID)
	case "singbox:start":
		err = s.svc.SetSingBoxEnabled(ctx, c.ID, true)
	case "singbox:stop":
		err = s.svc.SetSingBoxEnabled(ctx, c.ID, false)
	default:
		return "", errTelegramDraft
	}
	outcome := "success"
	if err != nil {
		text = ""
		outcome = "failed"
	}
	auditCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), auditWriteTimeout)
	defer cancel()
	if e := s.svc.Store().AppendAudit(auditCtx, &model.AuditLog{Actor: fmt.Sprintf("telegram:%d:%s", from.ID, owner.Username), Action: "telegram." + c.Action, Target: strconv.FormatInt(targetID, 10), Detail: outcome}); e != nil {
		s.log.Error("write Telegram operation audit")
	}
	return text, err
}
func (s *Server) telegramList(ctx context.Context, kind string, page int) (string, telegramButtons, error) {
	type item struct {
		id           int64
		name, detail string
		external     bool
	}
	var items []item
	state := func(enabled bool) string {
		if enabled {
			return "已启用"
		}
		return "已停止"
	}
	switch kind {
	case "routes":
		v, e := s.svc.Store().ListRoutes(ctx)
		if e != nil {
			return "", nil, e
		}
		for _, r := range v {
			items = append(items, item{id: r.ID, name: r.Name, detail: fmt.Sprintf("%s · %s", r.Protocol, state(r.Enabled))})
		}
	case "nodes":
		v, e := s.svc.Store().ListNodes(ctx)
		if e != nil {
			return "", nil, e
		}
		metrics, e := s.svc.Store().ListNodeMetrics(ctx)
		if e != nil {
			return "", nil, e
		}
		for _, n := range v {
			detail := telegramNodeStatus(n.Status) + " · CPU 未知 · 内存 未知 · 磁盘 未知"
			if m := metrics[n.ID]; m != nil {
				detail = fmt.Sprintf("%s · CPU %s · 内存 %s · 磁盘 %s", telegramNodeStatus(n.Status), telegramCPUPercent(m.CPUPercent), telegramPercent(m.MemUsed, m.MemTotal), telegramPercent(m.DiskUsed, m.DiskTotal))
			}
			items = append(items, item{id: n.ID, name: n.Name, detail: detail})
		}
	case "singbox":
		v, e := s.svc.Store().ListSingBoxUsers(ctx)
		if e != nil {
			return "", nil, e
		}
		for _, u := range v {
			source := "托管"
			if u.Source == model.SingBoxExternal {
				source = "外部扫描（删除仅移除记录）"
			}
			items = append(items, item{id: u.ID, name: u.Name, detail: fmt.Sprintf("%s · %s · %s", u.Protocol, state(u.Enabled), source), external: u.Source == model.SingBoxExternal})
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].id < items[j].id })
	maxPage := 0
	if len(items) > 0 {
		maxPage = (len(items) - 1) / 6
	}
	if page > maxPage {
		page = maxPage
	}
	if page < 0 {
		page = 0
	}
	labels := map[string]string{"routes": "链路", "nodes": "机器", "singbox": "代理节点"}
	b := telegramButtons{{tgButton("添加"+labels[kind], "add:"+kind)}}
	if kind == "routes" {
		b = append(b, []map[string]any{tgButton("流量用量", "traffic:0")})
	}
	var text strings.Builder
	fmt.Fprintf(&text, "%s · 第 %d/%d 页 · 共 %d 项\n", labels[kind], page+1, maxPage+1, len(items))
	for i := page * 6; i < len(items) && i < (page+1)*6; i++ {
		it := items[i]
		label := fmt.Sprintf("%s #%d", telegramDisplayName(it.name), it.id)
		fmt.Fprintf(&text, "• %s\n  %s\n", label, it.detail)
		row := []map[string]any{tgButton("删除 "+label, fmt.Sprintf("act:%s:delete:%d", kind, it.id))}
		if kind != "nodes" && !it.external {
			action, title := "start", "启用"
			if kind == "routes" {
				action, title = "apply", "部署"
			}
			row = append(row, tgButton(title, fmt.Sprintf("act:%s:%s:%d", kind, action, it.id)), tgButton("停止", fmt.Sprintf("act:%s:stop:%d", kind, it.id)))
		}
		b = append(b, row)
	}
	nav := []map[string]any{}
	if page > 0 {
		nav = append(nav, tgButton("上一页", fmt.Sprintf("%s:%d", kind, page-1)))
	}
	if page < maxPage {
		nav = append(nav, tgButton("下一页", fmt.Sprintf("%s:%d", kind, page+1)))
	}
	b = append(b, append(nav, tgButton("返回菜单", "dashboard")))
	text.WriteString("删除及启停均需二次确认。")
	return text.String(), b, nil
}

func tgPort(value string, allowAuto bool) (int, error) {
	if allowAuto && (value == "auto" || value == "0" || value == "自动") {
		return 0, nil
	}
	n, e := strconv.Atoi(value)
	if e != nil || n < 1 || n > 65535 {
		return 0, errTelegramDraft
	}
	return n, nil
}
func tgQuota(value string) (int64, error) {
	n, e := strconv.ParseFloat(value, 64)
	if e != nil || math.IsNaN(n) || math.IsInf(n, 0) || n < 0 || (n > 0 && n*(1<<30) < 1) || n > float64(math.MaxInt64)/(1<<30)-1 {
		return 0, errTelegramDraft
	}
	return int64(n * (1 << 30)), nil
}
func (s *Server) telegramPrompt(ctx context.Context, w *telegramWizard) (string, telegramButtons, error) {
	b := tgCancel()
	button := func(label, value string) map[string]any { return tgButton(label, "wiz:"+w.Nonce+":"+w.Stage+":"+value) }
	prompts := map[string]string{"name": "请输入名称（不要发送密码或私钥）：", "target": "请输入目标 host:port（IPv6 使用 [地址]:端口）：", "port": "请输入端口 1–65535，或 auto 自动分配：", "quota": "请输入流量额度 GB，0 表示不限量：", "days": "请输入有效天数（0 永久，最多 36500）：", "host": "请输入机器 IP 或主机名（不要填写 SSH 密码）：", "sshport": "请输入 SSH 端口，发送 - 使用 22：", "sshuser": "请输入 SSH 用户名，发送 - 使用 root：", "poolstart": "请输入端口池起点，发送 - 使用 10000：", "poolend": "请输入端口池终点，发送 - 使用 65535："}
	text := prompts[w.Stage]
	switch w.Stage {
	case "machine", "hops":
		nodes, e := s.svc.Store().ListNodes(ctx)
		if e != nil {
			return "", nil, e
		}
		text = "选择机器："
		if w.Stage == "hops" {
			text = fmt.Sprintf("按顺序选择链路跳点（已选 %v），然后完成：", w.Route.NodeIDs)
		}
		available := make([]*model.Node, 0, len(nodes))
		for _, n := range nodes {
			selected := false
			for _, id := range w.Route.NodeIDs {
				if id == n.ID {
					selected = true
				}
			}
			if !selected {
				available = append(available, n)
			}
		}
		maxPage := 0
		if len(available) > 0 {
			maxPage = (len(available) - 1) / 8
		}
		if w.Page > maxPage {
			w.Page = maxPage
		}
		for i := w.Page * 8; i < len(available) && i < (w.Page+1)*8; i++ {
			n := available[i]
			b = append(b, []map[string]any{button(fmt.Sprintf("%s #%d", telegramDisplayName(n.Name), n.ID), strconv.FormatInt(n.ID, 10))})
		}
		nav := []map[string]any{}
		if w.Page > 0 {
			nav = append(nav, button("上一页", fmt.Sprintf("page%d", w.Page-1)))
		}
		if w.Page < maxPage {
			nav = append(nav, button("下一页", fmt.Sprintf("page%d", w.Page+1)))
		}
		if len(nav) > 0 {
			b = append(b, nav)
		}
		if w.Stage == "hops" {
			b = append(b, []map[string]any{button("完成跳点选择", "done")})
		}
	case "protocol":
		text = "选择协议："
		protocols := []string{"tcp", "tcp+udp"}
		if w.Kind == "singbox" {
			protocols = []string{"ss2022", "anytls", "vless-reality", "hysteria2", "tuic", "trojan", "vmess", "vless-tls"}
		}
		for _, p := range protocols {
			b = append(b, []map[string]any{button(p, p)})
		}
	}
	return text, b, nil
}
func (s *Server) telegramAdvance(ctx context.Context, w *telegramWizard, value string, callback bool) (string, telegramButtons, error) {
	invalid := func() (string, telegramButtons, error) {
		text, b, e := s.telegramPrompt(ctx, w)
		return "输入无效，请重试。\n" + text, b, e
	}
	switch w.Stage {
	case "name":
		if model.ValidateDisplayName(value) != nil {
			return invalid()
		}
		switch w.Kind {
		case "routes":
			w.Route.Name = value
			w.Stage = "hops"
		case "nodes":
			w.Node.Name = value
			w.Stage = "host"
		case "singbox":
			w.Proxy.Name = value
			w.Stage = "protocol"
		}
	case "machine", "hops":
		if !callback {
			return invalid()
		}
		if strings.HasPrefix(value, "page") {
			page, e := strconv.Atoi(strings.TrimPrefix(value, "page"))
			if e != nil || page < 0 {
				return invalid()
			}
			w.Page = page
			return s.telegramPrompt(ctx, w)
		}
		if value == "done" && w.Stage == "hops" {
			if len(w.Route.NodeIDs) == 0 {
				return invalid()
			}
			w.Stage = "target"
			break
		}
		id, e := strconv.ParseInt(value, 10, 64)
		if e != nil || id <= 0 {
			return invalid()
		}
		if _, e = s.svc.Store().NodeByID(ctx, id); e != nil {
			return invalid()
		}
		if w.Stage == "machine" {
			w.Proxy.NodeID = id
			w.Stage = "name"
		} else {
			for _, existing := range w.Route.NodeIDs {
				if existing == id {
					return invalid()
				}
			}
			if len(w.Route.NodeIDs) >= 16 {
				return invalid()
			}
			w.Route.NodeIDs = append(w.Route.NodeIDs, id)
		}
	case "target":
		h, p, e := net.SplitHostPort(value)
		if e != nil || h == "" || len(h) > 253 || strings.ContainsAny(h, " \t\r\n/@\\") {
			return invalid()
		}
		if _, e = tgPort(p, false); e != nil {
			return invalid()
		}
		w.Route.Target = value
		w.Stage = "protocol"
	case "protocol":
		if !callback {
			return invalid()
		}
		if w.Kind == "routes" {
			if value != "tcp" && value != "tcp+udp" {
				return invalid()
			}
			w.Route.Protocol = model.Protocol(value)
		} else {
			p := model.SingBoxProtocol(value)
			if !p.Valid() {
				return invalid()
			}
			w.Proxy.Protocol = p
		}
		w.Stage = "port"
	case "port":
		n, e := tgPort(value, true)
		if e != nil {
			return invalid()
		}
		if w.Kind == "routes" {
			if n != 0 {
				w.Route.EntryPort = &n
			}
		} else {
			w.Proxy.Port = n
		}
		w.Stage = "quota"
	case "quota":
		n, e := tgQuota(value)
		if e != nil {
			return invalid()
		}
		if w.Kind == "routes" {
			if n > 0 {
				w.Route.QuotaBytes = &n
				w.Route.QuotaResetDay = 1
			}
			w.Route.Enabled = false
			delete(s.telegram.wizards, w.Scope.Actor)
			return s.telegramConfirm(ctx, telegramConfirmation{Scope: w.Scope, Action: "routes:create", Wizard: *w}, fmt.Sprintf("创建链路 %s\n跳点 %v\n目标 %s\n协议 %s\n端口 %s\n额度 %s GB\n仅创建，不部署。", telegramDisplayName(w.Route.Name), w.Route.NodeIDs, w.Route.Target, w.Route.Protocol, tgEntryPort(w.Route.EntryPort), value))
		}
		w.Proxy.QuotaBytes = n
		w.Stage = "days"
	case "days":
		n, e := strconv.Atoi(value)
		if e != nil || n < 0 || n > 36500 {
			return invalid()
		}
		w.Proxy.AccessDays = n
		delete(s.telegram.wizards, w.Scope.Actor)
		return s.telegramConfirm(ctx, telegramConfirmation{Scope: w.Scope, Action: "singbox:create", Wizard: *w}, fmt.Sprintf("创建并部署代理节点 %s\n机器 #%d · %s · 端口 %d（0 自动）\n额度 %.2f GB（0 不限）· 有效 %d 天（0 永久）", telegramDisplayName(w.Proxy.Name), w.Proxy.NodeID, w.Proxy.Protocol, w.Proxy.Port, float64(w.Proxy.QuotaBytes)/(1<<30), n))
	case "host":
		if value == "" || strings.ContainsAny(value, " /\\\n\r\t@") || len(value) > 253 {
			return invalid()
		}
		w.Node.Host = value
		w.Stage = "sshport"
	case "sshport":
		if value == "-" {
			value = "22"
		}
		n, e := tgPort(value, false)
		if e != nil {
			return invalid()
		}
		w.Node.SSHPort = n
		w.Stage = "sshuser"
	case "sshuser":
		if value == "-" {
			value = "root"
		}
		if value == "" || len(value) > 64 || strings.ContainsAny(value, " /\\\n\r\t:@") {
			return invalid()
		}
		w.Node.SSHUser = value
		w.Stage = "poolstart"
	case "poolstart":
		if value == "-" {
			value = "10000"
		}
		n, e := tgPort(value, false)
		if e != nil {
			return invalid()
		}
		w.Node.PortStart = n
		w.Stage = "poolend"
	case "poolend":
		if value == "-" {
			value = "65535"
		}
		n, e := tgPort(value, false)
		if e != nil || n < w.Node.PortStart {
			return invalid()
		}
		w.Node.PortEnd = n
		nonce, e := cryptox.RandomToken(24)
		if e != nil {
			return "", nil, e
		}
		s.telegram.machineDrafts[nonce] = telegramMachineDraft{Scope: w.Scope, Input: w.Node}
		delete(s.telegram.wizards, w.Scope.Actor)
		return "机器资料已暂存，尚未添加。默认直连；跳板和 IPv6 等高级设置可在安全表单修改。请打开表单填写 SSH 密码或私钥并提交。不要在聊天中发送凭据。", telegramButtons{{{"text": "安全填写 SSH 凭据", "web_app": map[string]string{"url": strings.TrimRight(s.telegram.cfg.PanelURL, "/") + "/telegram?tab=nodes&draft=" + nonce}}}, {tgButton("取消", "cancel")}}, nil
	default:
		return "", nil, errTelegramDraft
	}
	return s.telegramPrompt(ctx, w)
}
func tgEntryPort(port *int) string {
	if port == nil {
		return "自动"
	}
	return strconv.Itoa(*port)
}
func (s *Server) handleTelegramMachineDraft(w http.ResponseWriter, r *http.Request) {
	actor, ok := r.Context().Value(telegramActorKey).(int64)
	if !ok {
		writeError(w, 403, "仅 Telegram 会话可读取草稿")
		return
	}
	t := s.telegram
	t.mu.Lock()
	defer t.mu.Unlock()
	d, ok := t.machineDrafts[r.URL.Query().Get("draft")]
	if !ok || d.Scope.Actor != actor || !s.telegramScopeValid(r.Context(), d.Scope) {
		writeError(w, 404, "草稿已失效，请重新开始")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, map[string]any{"input": map[string]any{"name": d.Input.Name, "host": d.Input.Host, "ssh_port": d.Input.SSHPort, "ssh_user": d.Input.SSHUser, "port_start": d.Input.PortStart, "port_end": d.Input.PortEnd, "auth_type": "password", "via_node_id": nil, "skip_udp_probe": false, "ipv6_entry": ""}, "expires_at": d.Scope.Expires})
}

// The destructive machine confirmation also binds the set of proxy records it
// will remove; additions after the preview require a fresh confirmation.
func (s *Server) telegramNodeDependents(ctx context.Context, id int64) ([32]byte, error) {
	users, e := s.svc.Store().ListSingBoxUsersOnNode(ctx, id)
	if e != nil {
		return [32]byte{}, e
	}
	return telegramDependentsIdentity(users)
}
func telegramProxyIdentity(u *model.SingBoxUser) map[string]any {
	return map[string]any{"id": u.ID, "node": u.NodeID, "name": u.Name, "port": u.Port, "protocol": u.Protocol, "created": u.CreatedAt, "source": u.Source, "service": u.ServiceName, "path": u.ConfigPath, "external_path": u.ExternalPath, "config_hash": sha256.Sum256(u.ConfigBlob)}
}
func telegramDependentsIdentity(users []*model.SingBoxUser) ([32]byte, error) {
	identities := make(map[int64]map[string]any, len(users))
	for _, u := range users {
		identities[u.ID] = telegramProxyIdentity(u)
	}
	raw, e := json.Marshal(identities)
	return sha256.Sum256(raw), e
}
