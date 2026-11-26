package catalog

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"testing"
)

// kitSHA256 is the suite's PostgreSQL 17 to 18 kit v4 (ky-postgres-18): scripts/ holds that
// file byte for byte, and internal/kube embeds the same bytes (TestRenderReplicas).
const kitSHA256 = "195ae8684e34dd515158a88b1f76b918cf6e3f3c854ce83fe4fb190a5178cf06"

func TestPostgresKitIsVendoredByteForByte(t *testing.T) {
	vendored, err := os.ReadFile("../../scripts/postgres-17-to-18.sh")
	if err != nil {
		t.Fatal(err)
	}
	if sum := sha256.Sum256(vendored); hex.EncodeToString(sum[:]) != kitSHA256 {
		t.Fatalf("scripts/postgres-17-to-18.sh is not kit v4: sha256 %x", sum)
	}
	if fi, err := os.Stat("../../scripts/postgres-17-to-18.sh"); err != nil || fi.Mode().Perm() != 0o755 {
		t.Fatalf("scripts/postgres-17-to-18.sh mode: %v %v", fi.Mode(), err)
	}
}

// The replicated KyIdentity's database pod runs PostgreSQL 18, and 17 only for its dump.
func TestPostgres18Images(t *testing.T) {
	const pg18 = "postgres:18.7-alpine3.24@sha256:0115267ab5a509022df9319c77be365cef335adb71556c75f60f5360ee247e71"
	const pg17 = "postgres:17.12-alpine3.24@sha256:f9cf3e1f0066f0eb0c48707d864f0ac5941e9ff454a072077d97a4d278d07d73"
	cat, err := Load(Embedded())
	if err != nil {
		t.Fatal(err)
	}
	if im := cat["kyidentity"].Images; im["postgres"] != pg18 || im["postgres_17"] != pg17 {
		t.Errorf("kyidentity: postgres %s, postgres_17 %s", im["postgres"], im["postgres_17"])
	}
	if r := cat["kyidentity"].Kubernetes.Replicas; r.DatabaseImage != "postgres" || r.DumpImage != "postgres_17" {
		t.Errorf("replicas %+v", r)
	}
}
