package singbox

import (
	"encoding/json"
	"testing"

	"github.com/kukumi1/fluxlite/internal/model"
)

func TestWithPortMovesInboundAndLinkForEveryProtocol(t *testing.T) {
	for _, protocol := range []model.SingBoxProtocol{
		model.SingBoxSS2022, model.SingBoxAnyTLS, model.SingBoxVLESSReality, model.SingBoxHysteria2,
		model.SingBoxTUIC, model.SingBoxTrojan, model.SingBoxVMess, model.SingBoxVLESSTLS,
	} {
		t.Run(string(protocol), func(t *testing.T) {
			b, err := Generate(protocol, "测试节点", 20001, "/etc/fluxlite/singbox/1", "")
			if err != nil {
				t.Fatal(err)
			}
			before := b.Link("203.0.113.5")
			if err := b.WithPort(30002); err != nil {
				t.Fatal(err)
			}
			if b.Port != 30002 {
				t.Fatalf("port = %d, want 30002", b.Port)
			}
			var server struct {
				Inbounds []struct {
					ListenPort int `json:"listen_port"`
				} `json:"inbounds"`
			}
			if err := json.Unmarshal(b.Config, &server); err != nil {
				t.Fatal(err)
			}
			if len(server.Inbounds) != 1 || server.Inbounds[0].ListenPort != 30002 {
				t.Fatalf("inbounds = %+v, want one on 30002", server.Inbounds)
			}
			// The exported client must point at the new port, and nothing but
			// the port may change: credentials stay valid across the move.
			if _, client, err := b.ExportClient("203.0.113.5"); err != nil {
				t.Fatalf("export after move: %v", err)
			} else if !json.Valid(client) {
				t.Fatalf("export produced invalid JSON")
			}
			after := b.Link("203.0.113.5")
			if after == before {
				t.Fatalf("link unchanged: %s", after)
			}
		})
	}
}

func TestWithPortRefusesAConfigWithoutTheRecordedPort(t *testing.T) {
	b, err := Generate(model.SingBoxSS2022, "n", 20001, "/tmp", "")
	if err != nil {
		t.Fatal(err)
	}
	b.Port = 9
	if err := b.WithPort(30002); err == nil {
		t.Fatal("moved a bundle whose config has no inbound on its recorded port")
	}
}
