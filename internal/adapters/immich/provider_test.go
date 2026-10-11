package immich

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestProviderConfigurationIntent(t *testing.T) {
	for _, mode := range []string{"configure-repeat", "foreign-provider", "disabled-foreign-provider", "preflight-drift", "lost-write", "readback-drift", "missing-field", "invalid-intent"} {
		t.Run(mode, func(t *testing.T) {
			config := map[string]any{
				"oauth":     map[string]any{"enabled": false, "autoRegister": true, "allowInsecureRequests": false, "issuerUrl": "", "clientId": "", "clientSecret": "", "roleClaim": "immich_role", "scope": "openid email profile", "signingAlgorithm": "RS256", "tokenEndpointAuthMethod": "client_secret_post", "buttonText": "Keep my label"},
				"unrelated": map[string]any{"quota": json.Number("9007199254740993"), "label": "Keep this too"},
			}
			oauth := config["oauth"].(map[string]any)
			if mode == "foreign-provider" || mode == "disabled-foreign-provider" {
				oauth["issuerUrl"] = "https://foreign.example.test"
				oauth["enabled"] = mode == "foreign-provider"
			}
			if mode == "missing-field" {
				delete(oauth, "allowInsecureRequests")
			}
			writes := 0
			srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("x-api-key") != "private-test-key" {
					t.Error("missing credential")
				}
				if r.URL.Path == "/api/server/version" {
					_ = json.NewEncoder(w).Encode(map[string]int{"major": 3, "minor": 2, "patch": 0})
					return
				}
				if r.URL.Path != "/api/system-config" {
					t.Error("unexpected native route")
					w.WriteHeader(404)
					return
				}
				if r.Method == http.MethodPut {
					writes++
					decoder := json.NewDecoder(r.Body)
					decoder.UseNumber()
					if decoder.Decode(&config) != nil {
						t.Error("bad native configuration write")
					}
					if mode == "lost-write" {
						w.WriteHeader(503)
						return
					}
					if mode == "readback-drift" {
						config["unrelated"].(map[string]any)["label"] = "changed"
					}
				}
				_ = json.NewEncoder(w).Encode(config)
			}))
			defer srv.Close()
			c, _ := New(srv.URL, "private-test-key")
			c.http.Transport = srv.Client().Transport
			ctx := context.Background()
			digest, err := c.InspectProviderConfiguration(ctx)
			if mode == "missing-field" {
				if err == nil || writes != 0 {
					t.Fatal("incomplete provider proof accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if mode == "preflight-drift" {
				config["unrelated"].(map[string]any)["label"] = "changed"
			}
			spec := ProviderSpec{Issuer: "https://identity.example.test", ClientID: "photos", ClientSecret: "secret-must-not-leak", RolesClaim: "roles"}
			if mode == "invalid-intent" {
				spec.Issuer = "http://identity.example.test"
			}
			after, err := c.ConfigureProvider(ctx, digest, spec)
			switch mode {
			case "configure-repeat":
				if err != nil || writes != 1 || after == digest || !configDigest.MatchString(after) {
					t.Fatal("provider setup not verified", err, writes)
				}
				if config["unrelated"].(map[string]any)["quota"] != json.Number("9007199254740993") || config["oauth"].(map[string]any)["buttonText"] != "Keep my label" {
					t.Fatal("native unrelated configuration changed")
				}
				if repeated, err := c.ConfigureProvider(ctx, after, spec); err != nil || repeated != after || writes != 1 {
					t.Fatal("verified repeat wrote configuration")
				}
				if _, err := c.ConfigureProvider(ctx, digest, spec); err == nil || writes != 1 {
					t.Fatal("stale preflight authorized a repeat")
				}
			case "lost-write", "readback-drift":
				if err != ErrUncertain || after != "" || writes != 1 {
					t.Fatal("uncertain configuration reported success", err, writes)
				}
			default:
				if err == nil || after != "" || writes != 0 {
					t.Fatal("unverified configuration mutated", err, writes)
				}
			}
			if err != nil && strings.Contains(err.Error(), spec.ClientSecret) {
				t.Fatal("provider secret leaked")
			}
		})
	}
}

func TestProviderFingerprintExcludesSecrets(t *testing.T) {
	config := func(secret, password, label string) map[string]any {
		return map[string]any{
			"oauth":         map[string]any{"enabled": true, "clientSecret": secret, "buttonText": label},
			"notifications": map[string]any{"smtp": map[string]any{"transport": map[string]any{"host": "mail.example.test", "password": password}}},
		}
	}
	base := config("client-secret-1", "smtp-password-1", "Sign in")
	digest, err := providerDigest(base)
	if err != nil || !configDigest.MatchString(digest) {
		t.Fatal("invalid fingerprint", err)
	}
	if rotated, _ := providerDigest(config("client-secret-2", "smtp-password-2", "Sign in")); rotated != digest {
		t.Fatal("fingerprint depends on a secret value")
	}
	if relabeled, _ := providerDigest(config("client-secret-1", "smtp-password-1", "Log in")); relabeled == digest {
		t.Fatal("fingerprint ignores a non-secret field")
	}
	if base["oauth"].(map[string]any)["clientSecret"] != "client-secret-1" ||
		base["notifications"].(map[string]any)["smtp"].(map[string]any)["transport"].(map[string]any)["password"] != "smtp-password-1" {
		t.Fatal("fingerprinting removed secrets from the configuration that is written back")
	}
	if _, err := providerDigest(map[string]any{"notifications": "unexpected"}); err != nil {
		t.Fatal("unexpected shape must still fingerprint", err)
	}
}
