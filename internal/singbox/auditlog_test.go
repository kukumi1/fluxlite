package singbox

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/kukumi1/fluxlite/internal/model"
)

func TestWithAuditLogReplacesWarnAndKeepsTheRest(t *testing.T) {
	b, err := Generate(model.SingBoxVLESSReality, "DMIT", 10002, "/etc/fluxlite/singbox/27", "")
	if err != nil {
		t.Fatal(err)
	}
	out, changed, err := WithAuditLog(b.Config, 27)
	if err != nil || !changed {
		t.Fatalf("warn 级别的配置应被改写: changed=%v err=%v", changed, err)
	}
	var doc struct {
		Log      map[string]any   `json:"log"`
		Inbounds []map[string]any `json:"inbounds"`
	}
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Log["level"] != "info" || doc.Log["output"] != "/var/lib/fluxlite-singbox/27/access.log" || doc.Log["timestamp"] != true {
		t.Errorf("log 段不对: %v", doc.Log)
	}
	if len(doc.Inbounds) != 1 || doc.Inbounds[0]["listen_port"] != float64(10002) {
		t.Errorf("入站被改坏了: %v", doc.Inbounds)
	}

	// 已经是审计配置时不能再报「有变化」，否则每轮都会重启节点。
	if _, changed, err := WithAuditLog(out, 27); err != nil || changed {
		t.Errorf("已是审计配置却判为有变化: changed=%v err=%v", changed, err)
	}
}

func TestWithInfoLogKeepsOperatorsFieldOrder(t *testing.T) {
	in := []byte(`{"inbounds":[{"type":"shadowsocks","listen_port":22023}],"log":{"level":"warn","timestamp":true,"disabled":true},"outbounds":[{"type":"direct"}]}`)
	out, err := WithInfoLog(in)
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if !(strings.Index(s, `"inbounds"`) < strings.Index(s, `"log"`) && strings.Index(s, `"log"`) < strings.Index(s, `"outbounds"`)) {
		t.Errorf("顶层字段顺序被打乱了:\n%s", s)
	}
	var doc struct {
		Log map[string]any `json:"log"`
	}
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Log["level"] != "info" || doc.Log["timestamp"] != true || doc.Log["disabled"] != nil {
		t.Errorf("log 段应只改级别、去掉 disabled: %v", doc.Log)
	}

	added, err := WithInfoLog([]byte(`{"inbounds":[]}`))
	if err != nil || !strings.HasPrefix(strings.TrimSpace(string(added)), "{\n  \"log\"") {
		t.Errorf("没有 log 段时应加在最前面: %s %v", added, err)
	}
	if _, err := WithInfoLog([]byte(`[]`)); err == nil {
		t.Error("不是对象的配置应拒绝")
	}
}
