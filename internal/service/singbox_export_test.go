package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/kukumi1/fluxlite/internal/cryptox"
	"github.com/kukumi1/fluxlite/internal/model"
	"github.com/kukumi1/fluxlite/internal/singbox"
)

func seedExportBundle(t *testing.T, svc *Service, nodeID int64, bundle *singbox.Bundle, name string) *model.SingBoxUser {
	t.Helper()
	sealer, err := cryptox.NewSealer(bytes.Repeat([]byte{7}, cryptox.MasterKeySize))
	if err != nil {
		t.Fatal(err)
	}
	svc.sealer = sealer
	plain, err := json.Marshal(bundle)
	if err != nil {
		t.Fatal(err)
	}
	blob, err := sealer.Seal(plain)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	u := &model.SingBoxUser{NodeID: nodeID, Name: name, Protocol: bundle.Protocol, Port: bundle.Port,
		Source: model.SingBoxManaged, Enabled: true, Status: model.SingBoxStatusStopped,
		ServiceName: "fluxlite-singbox-test", ConfigPath: "/etc/fluxlite/singbox/test/config.json", ConfigBlob: blob,
		PeriodDays: 30, PeriodStartedAt: now, PeriodEndsAt: now.Add(30 * 24 * time.Hour)}
	if err := svc.store.CreateSingBoxUser(context.Background(), u); err != nil {
		t.Fatal(err)
	}
	return u
}

func TestExportLegacyAdoptedAnyTLSWithoutReAdoption(t *testing.T) {
	st, svc := newTestLookup(t)
	node := seedNode(t, st, "legacy")
	const secret = "test-private:+/#password"
	raw := json.RawMessage(`{"inbounds":[{"type":"anytls","listen":"0.0.0.0","listen_port":21200,"users":[{"name":"default","password":"test-private:+/#password"}],"tls":{"enabled":true,"certificate_path":"/etc/sing-box/cert.pem","key_path":"/etc/sing-box/key.pem"}}]}`)
	bundle := &singbox.Bundle{Config: raw, Protocol: model.SingBoxAnyTLS, Name: "Old name", Port: 21200}
	u := seedExportBundle(t, svc, node.ID, bundle, "Current name 中文")
	before, err := st.SingBoxUserByID(context.Background(), u.ID)
	if err != nil {
		t.Fatal(err)
	}
	result, err := svc.ExportSingBoxBundle(context.Background(), u.ID, "203.0.113.10")
	if err != nil {
		t.Fatal(err)
	}
	link, ok := result["link"].(string)
	if !ok || link == "" {
		t.Fatal("adopted export has no link")
	}
	parsed, err := url.Parse(link)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Scheme != "anytls" || parsed.User.Username() != secret || parsed.Host != "203.0.113.10:21200" || parsed.Fragment != u.Name {
		t.Fatal("link does not preserve client identity, address or current name")
	}
	config, ok := result["config"].(json.RawMessage)
	if !ok {
		t.Fatal("client JSON missing")
	}
	var client struct {
		Outbounds []struct {
			Type     string `json:"type"`
			Password string `json:"password"`
		} `json:"outbounds"`
	}
	if err := json.Unmarshal(config, &client); err != nil {
		t.Fatal(err)
	}
	if len(client.Outbounds) != 1 || client.Outbounds[0].Type != "anytls" || client.Outbounds[0].Password != secret {
		t.Fatal("client outbound credentials changed")
	}
	for _, field := range []string{"inbounds", "certificate_path", "key_path", "listen_port", "private_key"} {
		if strings.Contains(string(config), `"`+field+`"`) {
			t.Fatalf("server-only field exported: %s", field)
		}
	}
	if len(result["warnings"].([]string)) == 0 {
		t.Fatal("unknown imported certificate SNI not disclosed")
	}
	after, err := st.SingBoxUserByID(context.Background(), u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before.ConfigBlob, after.ConfigBlob) || !before.UpdatedAt.Equal(after.UpdatedAt) || before.ServiceName != after.ServiceName {
		t.Fatal("export mutated stored service credentials")
	}
}

func TestExportGeneratedSingBoxClients(t *testing.T) {
	for _, protocol := range []model.SingBoxProtocol{model.SingBoxSS2022, model.SingBoxAnyTLS, model.SingBoxVLESSReality, model.SingBoxHysteria2, model.SingBoxTUIC, model.SingBoxTrojan, model.SingBoxVMess, model.SingBoxVLESSTLS} {
		t.Run(string(protocol), func(t *testing.T) {
			st, svc := newTestLookup(t)
			node := seedNode(t, st, "generated")
			bundle, err := singbox.Generate(protocol, "Alice", 23001, "/etc/fluxlite/singbox/test", "2022-blake3-aes-256-gcm")
			if err != nil {
				t.Fatal(err)
			}
			u := seedExportBundle(t, svc, node.ID, bundle, "Alice")
			result, err := svc.ExportSingBoxBundle(context.Background(), u.ID, "[2001:db8::5]")
			if err != nil {
				t.Fatal(err)
			}
			if result["link"] == "" {
				t.Fatal("generated client has no link")
			}
			var config map[string]json.RawMessage
			if err := json.Unmarshal(result["config"].(json.RawMessage), &config); err != nil {
				t.Fatal(err)
			}
			if len(config["outbounds"]) == 0 || len(config["inbounds"]) != 0 {
				t.Fatal("server config returned as client config")
			}
		})
	}
}

func TestExportRejectsExternalAndAmbiguousCredentials(t *testing.T) {
	st, svc := newTestLookup(t)
	node := seedNode(t, st, "ambiguous")
	raw := json.RawMessage(`{"inbounds":[{"type":"anytls","listen_port":21200,"users":[{"password":"secret-one"},{"password":"secret-two"}],"tls":{"enabled":true}}]}`)
	u := seedExportBundle(t, svc, node.ID, &singbox.Bundle{Config: raw, Protocol: model.SingBoxAnyTLS, Name: "test", Port: 21200}, "test")
	if _, err := svc.ExportSingBoxBundle(context.Background(), u.ID, node.Host); !errors.Is(err, singbox.ErrClientExport) {
		t.Fatalf("ambiguous credentials accepted: %v", err)
	}
	u.Source = model.SingBoxExternal
	if err := st.UpdateSingBoxUser(context.Background(), u); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ExportSingBoxBundle(context.Background(), u.ID, node.Host); !errors.Is(err, ErrSingBoxNotManaged) {
		t.Fatalf("external credential export allowed: %v", err)
	}
}
