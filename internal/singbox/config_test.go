package singbox

import (
	"encoding/base64"
	"encoding/json"
	"testing"

	"github.com/kukumi1/fluxlite/internal/model"
)

func TestGenerateSupportsEveryProtocol(t *testing.T) {
	protocols := []model.SingBoxProtocol{
		model.SingBoxSS2022, model.SingBoxAnyTLS, model.SingBoxVLESSReality,
		model.SingBoxHysteria2, model.SingBoxTUIC, model.SingBoxTrojan,
		model.SingBoxVMess, model.SingBoxVLESSTLS,
	}
	for _, protocol := range protocols {
		t.Run(string(protocol), func(t *testing.T) {
			bundle, err := Generate(protocol, "Alice", 23456, "/etc/fluxlite/singbox/1", "2022-blake3-aes-256-gcm")
			if err != nil {
				t.Fatal(err)
			}
			var config map[string]any
			if err := json.Unmarshal(bundle.Config, &config); err != nil {
				t.Fatal(err)
			}
			if len(config["inbounds"].([]any)) != 1 {
				t.Fatalf("expected one inbound")
			}
			if bundle.Link("[2001:db8::1]") == "" {
				t.Fatal("empty client link")
			}
			if protocol == model.SingBoxSS2022 {
				inbound := config["inbounds"].([]any)[0].(map[string]any)
				password := inbound["password"].(string)
				if _, err := base64.StdEncoding.DecodeString(password); err != nil {
					t.Fatalf("SS2022 password is not standard base64: %v", err)
				}
			}
		})
	}
}
