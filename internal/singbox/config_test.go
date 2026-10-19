package singbox

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"golang.org/x/crypto/curve25519"
	"net/url"
	"strings"
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

// Reality uses the URL-safe unpadded alphabet on both ends. The generic
// export decoder deliberately accepts legacy alphabets, so test the strict
// sing-box decoder separately instead of relying on a round trip through it.
func TestRealityGeneratedKeysUseURLSafeEncoding(t *testing.T) {
	for i := 0; i < 64; i++ {
		bundle, err := Generate(model.SingBoxVLESSReality, "Reality test", 23456, "/tmp/test-reality", "")
		if err != nil {
			t.Fatal(err)
		}
		var cfg struct {
			Inbounds []struct {
				TLS struct {
					Reality struct {
						PrivateKey string `json:"private_key"`
					} `json:"reality"`
				} `json:"tls"`
			} `json:"inbounds"`
		}
		if err := json.Unmarshal(bundle.Config, &cfg); err != nil {
			t.Fatal(err)
		}
		key := cfg.Inbounds[0].TLS.Reality.PrivateKey
		private, err := base64.RawURLEncoding.DecodeString(key)
		if err != nil || len(private) != 32 || strings.ContainsAny(key, "+/=") {
			t.Fatal("Reality private key is not unpadded base64url")
		}
		link, err := url.Parse(bundle.Link("192.0.2.1"))
		if err != nil {
			t.Fatal(err)
		}
		encoded := link.Query().Get("pbk")
		public, err := base64.RawURLEncoding.DecodeString(encoded)
		if err != nil || len(public) != 32 || strings.ContainsAny(encoded, "+/= ") {
			t.Fatal("Reality link public key is not unpadded base64url")
		}
		want, err := curve25519.X25519(private, curve25519.Basepoint)
		if err != nil || !bytes.Equal(want, public) {
			t.Fatal("Reality public/private key pair mismatch")
		}
	}
}
