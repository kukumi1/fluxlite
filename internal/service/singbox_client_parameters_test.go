package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/kukumi1/fluxlite/internal/model"
	"github.com/kukumi1/fluxlite/internal/singbox"
	"github.com/kukumi1/fluxlite/internal/store"
)

func TestClientParametersPersistEncryptedWithoutClobberingState(t *testing.T) {
	st, svc := newTestLookup(t)
	node := seedNode(t, st, "parameters")
	raw := json.RawMessage(`{"inbounds":[{"type":"anytls","listen_port":21200,"users":[{"password":"test-profile-secret"}],"tls":{"enabled":true,"certificate_path":"/server/cert.pem"}}]}`)
	u := seedExportBundle(t, svc, node.ID, &singbox.Bundle{Port: 21200, Config: raw, Protocol: model.SingBoxAnyTLS}, "Profile test")
	ctx := context.Background()
	stale, err := st.SingBoxUserByID(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	link := url.URL{Scheme: "anytls", Host: net.JoinHostPort(node.Host, strconv.Itoa(u.Port)), User: url.User("test-profile-secret"), RawQuery: "sni=developer.apple.com&insecure=1&fp=chrome"}
	original := link.String()
	if err := svc.SetSingBoxClientParameters(ctx, u.ID, SingBoxClientParametersInput{OriginalLink: &original}); err != nil {
		t.Fatal(err)
	}
	saved, err := st.SingBoxUserByID(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(saved.ConfigBlob, u.ConfigBlob) || bytes.Contains(saved.ConfigBlob, []byte("developer.apple.com")) || bytes.Contains(saved.ConfigBlob, []byte("test-profile-secret")) {
		t.Fatal("profile not encrypted with bundle")
	}
	plain, err := svc.sealer.Open(saved.ConfigBlob)
	if err != nil {
		t.Fatal(err)
	}
	var b singbox.Bundle
	if err := json.Unmarshal(plain, &b); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(b.Config, raw) || b.LinkTemplate != "" {
		t.Fatal("original server bundle rewritten")
	}
	stale.UsedIn = 1234
	stale.Status = model.SingBoxStatusRunning
	if err := st.UpdateSingBoxUser(ctx, stale); err != nil {
		t.Fatal(err)
	}
	after, err := st.SingBoxUserByID(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(saved.ConfigBlob, after.ConfigBlob) || after.UsedIn != 1234 || after.Status != stale.Status {
		t.Fatal("stale collector clobbered profile or its live state lost")
	}
	if err := st.CompareAndSwapSingBoxConfig(ctx, u.ID, u.ConfigBlob, []byte("stale")); !errors.Is(err, store.ErrConflict) {
		t.Fatal("stale profile edit overwrote newer profile")
	}
	restarted := New(st, svc.sealer, nil, nil, nil, nil, svc.log)
	p, err := restarted.SingBoxClientParameters(ctx, u.ID)
	if err != nil || p.Parameters == nil || p.EffectiveParameters != *p.Parameters {
		t.Fatal("restarted service loses profile", err)
	}
	result, err := restarted.ExportSingBoxBundle(ctx, u.ID, node.Host)
	if err != nil {
		t.Fatal(err)
	}
	for _, warning := range result["warnings"].([]string) {
		if strings.Contains(warning, "未提供可恢复") {
			t.Fatal("saved SNI still triggers missing SNI warning")
		}
	}
	manual := singbox.ClientParameters{SNI: "example.test", Insecure: false}
	if err := svc.SetSingBoxClientParameters(ctx, u.ID, SingBoxClientParametersInput{Parameters: &manual}); err != nil {
		t.Fatal(err)
	}
	p, err = svc.SingBoxClientParameters(ctx, u.ID)
	if err != nil || p.EffectiveParameters != manual {
		t.Fatal("manual preferences differ", err)
	}
	if err := svc.ResetSingBoxClientParameters(ctx, u.ID); err != nil {
		t.Fatal(err)
	}
	p, err = svc.SingBoxClientParameters(ctx, u.ID)
	if err != nil || p.Parameters != nil || p.EffectiveParameters != (singbox.ClientParameters{}) {
		t.Fatal("reset does not restore legacy defaults", err)
	}
}

func TestInvalidAdoptionClientParametersNeverAcquireSSH(t *testing.T) {
	_, svc := newTestLookup(t)
	badLink := "anytls://test-secret@203.0.113.11:21200?insecure=true"
	for _, in := range []SingBoxClientParametersInput{
		{Parameters: &singbox.ClientParameters{SNI: "https://bad.test"}},
		{OriginalLink: &badLink},
		{OriginalLink: &badLink, Parameters: &singbox.ClientParameters{}},
	} {
		if err := svc.AdoptSingBoxUserWithClientParameters(context.Background(), 1, 100, in); err == nil {
			t.Fatal("invalid adoption settings accepted")
		}
	}
}
