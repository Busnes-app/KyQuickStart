//go:build e2e

package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Busnes-app/kyquickstart/internal/catalog"
	"go.yaml.in/yaml/v3"
)

// relaysOffEnv blanks the push relay URLs for a KyIdentity started directly. Set and empty
// turns registration off; unset, KyIdentity registers with its production relays.
var relaysOffEnv = []string{"PUSH_RELAY_URL=", "APNS_RELAY_URL="}

// relayPatches is test plumbing for copied catalog apps: a disposable KyIdentity or KyPost
// must not register on the production push relays, which keep one key per address and a
// daily write budget shared with every real installation.
var relayPatches = map[string][]struct{ file, from, to string }{
	"kyidentity": {
		{"manifest.yaml", "    KYIDENTITY_DATA_DIR: /data\n", "    KYIDENTITY_DATA_DIR: /data\n    PUSH_RELAY_URL: \"\"\n    APNS_RELAY_URL: \"\"\n"},
		{"compose.yaml.tmpl", "      KYIDENTITY_DATA_DIR: \"/data\"\n", "      KYIDENTITY_DATA_DIR: \"/data\"\n      PUSH_RELAY_URL: \"\"\n      APNS_RELAY_URL: \"\"\n"},
	},
	"kypost": {
		{"compose.yaml.tmpl", "      PUSH_RELAY_URL: https://fcm.relay.kypost.busnes.app\n", "      PUSH_RELAY_URL: \"\"\n"},
		{"compose.yaml.tmpl", "      APNS_RELAY_URL: https://apn.relay.kypost.busnes.app\n", "      APNS_RELAY_URL: \"\"\n"},
	},
}

// copyCatalogApp copies the catalog's app into catalogDir, relays off. Each patch is asserted
// present, so a moved line fails the test instead of registering.
func copyCatalogApp(t *testing.T, catalogDir, app string) {
	t.Helper()
	if err := os.CopyFS(filepath.Join(catalogDir, app), os.DirFS("../../internal/catalog/apps/"+app)); err != nil {
		t.Fatal(err)
	}
	relaysOff(t, filepath.Join(catalogDir, app))
}

// relaysOff applies relayPatches to the copied app in dir; other apps are left as they are.
func relaysOff(t *testing.T, dir string) {
	t.Helper()
	for _, p := range relayPatches[filepath.Base(dir)] {
		path := filepath.Join(dir, p.file)
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Count(string(b), p.from) != 1 {
			t.Fatalf("%s changed; test plumbing (push relays off) expects %q once", path, p.from)
		}
		if err := os.WriteFile(path, []byte(strings.Replace(string(b), p.from, p.to, 1)), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// TestRelaysOffCatalog proves the plumbing without a cluster: the patched catalog loads, and
// both products' Docker and Kubernetes environments name the relays empty.
func TestRelaysOffCatalog(t *testing.T) {
	cat, err := catalog.Load(os.DirFS(relaysOffCatalog(t)))
	if err != nil {
		t.Fatal(err)
	}
	for _, app := range []string{"kyidentity", "kypost"} {
		var compose struct {
			Services map[string]struct{ Environment map[string]*string }
		}
		if err := yaml.Unmarshal(cat[app].Compose, &compose); err != nil {
			t.Fatal(err)
		}
		env := compose.Services["app"].Environment
		for _, name := range []string{"PUSH_RELAY_URL", "APNS_RELAY_URL"} {
			if v, ok := env[name]; !ok || v == nil || *v != "" {
				t.Fatalf("%s compose: %s not set empty", app, name)
			}
			if k := cat[app].Kubernetes; k != nil {
				if v, ok := k.Env[name]; !ok || v != "" {
					t.Fatalf("%s kubernetes: %s not set empty", app, name)
				}
			}
		}
	}
}

// relaysOffCatalog is a copy of the whole catalog with relayPatches applied.
func relaysOffCatalog(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.CopyFS(dir, os.DirFS("../../internal/catalog/apps")); err != nil {
		t.Fatal(err)
	}
	for app := range relayPatches {
		relaysOff(t, filepath.Join(dir, app))
	}
	return dir
}
