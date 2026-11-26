package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/kukumi1/fluxlite/internal/report"
	"github.com/kukumi1/fluxlite/internal/service"
)

var itemNames = map[string]string{
	service.ItemRoute: "回程", service.ItemLarge: "大包回程", service.ItemUDP: "UDP回程", service.ItemSpeed: "测速",
}

// speedItemsLabel names what a job tested, the speed test with its mode.
func speedItemsLabel(items []string, mode string) string {
	var names []string
	for _, item := range []string{service.ItemRoute, service.ItemLarge, service.ItemUDP, service.ItemSpeed} {
		if !slices.Contains(items, item) {
			continue
		}
		name := itemNames[item]
		if item == service.ItemSpeed {
			name = tgSpeedModeNames[mode] + "测速"
		}
		names = append(names, name)
	}
	return strings.Join(names, "、")
}

// speedReportInput gathers one machine's part of a job for the image.
func speedReportInput(job *service.SpeedJobView, nodeID int64, name string) report.Input {
	req := job.Request
	provinces, carriers := splitPoints(req.Points)
	in := report.Input{
		Name: name, Families: reportFamilies(job, nodeID), Points: req.Points, Items: req.Items,
		Settings: telegramSpeedSummary(req.Family, carriers, provinces, req.Mode, req.Items),
		Finished: time.Unix(job.FinishedAt, 0),
	}
	if req.Mode == "both" {
		in.Modes = []string{"single", "multi"}
	} else {
		in.Modes = []string{req.Mode}
	}
	if job.FinishedAt == 0 {
		in.Finished = time.Now()
	}
	for _, r := range job.Runs {
		if r.NodeID != nodeID {
			continue
		}
		in.Egress = r.Egress
		if r.TrafficBytes > 0 {
			in.Traffic = fmt.Sprintf("消耗流量：共 %s（下载 %s / 上传 %s）", telegramTrafficBytes(float64(r.TrafficBytes)),
				telegramTrafficBytes(float64(r.RxBytes)), telegramTrafficBytes(float64(r.TxBytes)))
		}
		if r.StartedAt > 0 && r.FinishedAt >= r.StartedAt {
			in.Elapsed = r.FinishedAt - r.StartedAt
		}
		if r.Error != "" && r.Error != "已取消" {
			in.Note = r.Error
		}
	}
	switch job.Status {
	case "running":
		in.Note = "测试中，结果陆续更新"
	case "cancelled":
		in.Note = "已取消，只列出测完的部分"
	}
	for _, q := range job.Quality {
		if q.NodeID == nodeID {
			in.Quality = append(in.Quality, report.Quality{Point: q.Point, Item: q.Item, Family: q.Family, Line: q.Line, MS: q.MS, Loss: q.Loss, Error: q.Error})
		}
	}
	for _, r := range job.Results {
		if r.NodeID == nodeID {
			in.Speeds = append(in.Speeds, report.Speed{Point: r.Point, Mode: r.Mode, Family: r.Family, RTT: r.RTT, Down: r.Down, Up: r.Up, Retrans: r.Retrans, Error: r.Error})
		}
	}
	return in
}

// familyLabel names a request's family: IPv4, IPv6 or both.
func familyLabel(family int) string {
	if family == service.FamilyBoth {
		return "IPv4 + IPv6"
	}
	return fmt.Sprintf("IPv%d", family)
}

// requestFamilies are the families a request asked for, IPv4 first.
func requestFamilies(family int) []int {
	if family == service.FamilyBoth {
		return []int{4, 6}
	}
	return []int{family}
}

// reportFamilies are the families a machine has results for, or those
// asked for while it has none yet: a machine without IPv6 asked for both
// shows IPv4 alone.
func reportFamilies(job *service.SpeedJobView, nodeID int64) []int {
	asked := requestFamilies(job.Request.Family)
	seen := map[int]bool{}
	for _, q := range job.Quality {
		if q.NodeID == nodeID {
			seen[q.Family] = true
		}
	}
	for _, r := range job.Results {
		if r.NodeID == nodeID {
			seen[r.Family] = true
		}
	}
	if len(seen) == 0 {
		return asked
	}
	var out []int
	for _, f := range asked {
		if seen[f] {
			out = append(out, f)
		}
	}
	return out
}

func (s *Server) nodeName(ctx context.Context, id int64) string {
	if n, err := s.svc.Store().NodeByID(ctx, id); err == nil {
		return n.Name
	}
	return fmt.Sprintf("机器 #%d", id)
}

// handleSpeedImage draws one machine's report of a job.
func (s *Server) handleSpeedImage(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	nodeID, err := strconv.ParseInt(r.URL.Query().Get("node"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid node")
		return
	}
	job, err := s.speedJob(r.Context(), id)
	if err != nil || !slices.Contains(job.Request.NodeIDs, nodeID) {
		writeError(w, http.StatusNotFound, "没有这次测试")
		return
	}
	png, err := report.Render(speedReportInput(job, nodeID, s.nodeName(r.Context(), nodeID)))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "生成报告图失败")
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(png)
}

// sendTelegramPhoto sends a PNG with a caption and buttons to the bound
// account, under the same authorization checks as a message. Unlike the
// panel's other messages it is not protected: a speed report is meant to be
// saved and shared, and it shows no address in full.
func (s *Server) sendTelegramPhoto(ctx context.Context, png []byte, caption string, buttons telegramButtons) error {
	s.telegram.mu.Lock()
	err := s.telegram.load(ctx)
	cfg := s.telegram.cfg
	s.telegram.mu.Unlock()
	if err != nil {
		return err
	}
	if !cfg.Enabled || cfg.Token == "" || cfg.BoundUserID <= 0 {
		return nil
	}
	owner, err := s.svc.Store().UserByID(ctx, cfg.UserID)
	if err != nil || owner.Locked(time.Now()) {
		return errors.New("Telegram owner unavailable")
	}
	markup, err := json.Marshal(buttons.markup())
	if err != nil {
		return err
	}
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	for k, v := range map[string]string{
		"chat_id": strconv.FormatInt(cfg.BoundUserID, 10), "caption": caption, "parse_mode": "HTML",
		"reply_markup": string(markup),
	} {
		if err := form.WriteField(k, v); err != nil {
			return err
		}
	}
	part, err := form.CreateFormFile("photo", "report.png")
	if err != nil {
		return err
	}
	if _, err := part.Write(png); err != nil {
		return err
	}
	if err := form.Close(); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.telegram.org/bot"+cfg.Token+"/sendPhoto", &body)
	if err != nil {
		return errors.New("cannot create Telegram request")
	}
	req.Header.Set("Content-Type", form.FormDataContentType())
	resp, err := telegramHTTPClient.Do(req)
	if err != nil {
		return errors.New("Telegram connection failed")
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("Telegram rejected the photo: status %d", resp.StatusCode)
	}
	return nil
}
