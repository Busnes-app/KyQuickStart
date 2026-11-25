package catalog

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
)

// kitSHA256 is the suite's PostgreSQL 17 to 18 kit v2 (ky-postgres-18): every copy in this
// repository is that file byte for byte.
const kitSHA256 = "2992644109716cd34297fbcb76eba8907f4a5e3031a12c05aade010e5ea2b571"

func TestPostgresKitIsVendoredByteForByte(t *testing.T) {
	vendored, err := os.ReadFile("../../scripts/postgres-17-to-18.sh")
	if err != nil {
		t.Fatal(err)
	}
	if sum := sha256.Sum256(vendored); hex.EncodeToString(sum[:]) != kitSHA256 {
		t.Fatalf("scripts/postgres-17-to-18.sh is not kit v2: sha256 %x", sum)
	}
	if fi, err := os.Stat("../../scripts/postgres-17-to-18.sh"); err != nil || fi.Mode().Perm() != 0o755 {
		t.Fatalf("scripts/postgres-17-to-18.sh mode: %v %v", fi.Mode(), err)
	}
	cat, err := Load(Embedded())
	if err != nil {
		t.Fatal(err)
	}
	if got := cat["kymessage"].Files["pg-upgrade/postgres-17-to-18.sh"]; !bytes.Equal(got, vendored) {
		t.Fatal("kymessage's pg-upgrade/postgres-17-to-18.sh differs from scripts/postgres-17-to-18.sh")
	}
}

// PostgreSQL 18 everywhere the catalog runs its own Postgres, and 17 only for the dumps.
func TestPostgres18Images(t *testing.T) {
	const pg18 = "postgres:18.7-alpine3.24@sha256:0115267ab5a509022df9319c77be365cef335adb71556c75f60f5360ee247e71"
	const pg17 = "postgres:17.12-alpine3.24@sha256:f9cf3e1f0066f0eb0c48707d864f0ac5941e9ff454a072077d97a4d278d07d73"
	cat, err := Load(Embedded())
	if err != nil {
		t.Fatal(err)
	}
	for _, app := range []string{"kymessage", "kyidentity"} {
		if im := cat[app].Images; im["postgres"] != pg18 || im["postgres_17"] != pg17 {
			t.Errorf("%s: postgres %s, postgres_17 %s", app, im["postgres"], im["postgres_17"])
		}
	}
	if r := cat["kyidentity"].Kubernetes.Replicas; r.DatabaseImage != "postgres" || r.DumpImage != "postgres_17" {
		t.Errorf("replicas %+v", r)
	}
	_, services, _ := messageCompose(t)
	dump, pg := services["postgres-17-dump"], services["postgres"]
	if dump.NetworkMode != "none" || dump.Environment["OLD"] != "/v/pgdata" || dump.Environment["NEW"] != "/v" || !slices.Contains(asList(dump.Volumes), "./pgdata:/v") {
		t.Errorf("postgres-17-dump = %+v", dump)
	}
	if _, ok := pg.Environment["PGDATA"]; ok || !slices.Contains(asList(pg.Volumes), "./pgdata:/var/lib/postgresql") || !slices.Contains(asList(pg.Volumes), "./pg-upgrade/postgres-17-to-18.sh:/ky/postgres-17-to-18.sh:ro") {
		t.Errorf("postgres = %+v", pg)
	}
}

func TestOnceServices(t *testing.T) {
	const waits = "  init:\n    image: {{ .Images.web }}\n    restart: \"no\"\n  db:\n    image: {{ .Images.web }}\n    depends_on:\n      init: {condition: service_completed_successfully}\n  other:\n    image: {{ .Images.web }}\n    depends_on: [db]\n"
	cat, err := Load(fstest.MapFS{"a/manifest.yaml": {Data: []byte(manifest)}, "a/compose.yaml.tmpl": {Data: []byte(compose + waits)}})
	if err != nil {
		t.Fatal(err)
	}
	if a := cat["a"]; !slices.Equal(a.Once, []string{"init"}) || !slices.Equal(a.Services, []string{"db", "other", "web"}) {
		t.Fatalf("once %v, services %v", a.Once, a.Services)
	}
	_, err = Load(fstest.MapFS{"a/manifest.yaml": {Data: []byte(manifest)}, "a/compose.yaml.tmpl": {Data: []byte(compose + strings.Replace(waits, "    restart: \"no\"\n", "", 1))}})
	if err == nil || !strings.Contains(err.Error(), `needs restart: "no"`) {
		t.Fatalf("restarting one-shot: %v", err)
	}
}

// An app without native_mail binds its files/ in its one rendering, all of them.
func TestFilesOfPlainApp(t *testing.T) {
	bound := compose + "    volumes: [./conf/x.sh:/x.sh:ro]\n"
	cat, err := Load(fstest.MapFS{"a/manifest.yaml": {Data: []byte(manifest)}, "a/compose.yaml.tmpl": {Data: []byte(bound)}, "a/files/conf/x.sh": {Data: []byte("x")}})
	if err != nil || string(cat["a"].Files["conf/x.sh"]) != "x" || !slices.Equal(cat["a"].Nested, []string{"./conf/x.sh"}) {
		t.Fatalf("bound file: %v", err)
	}
	_, err = Load(fstest.MapFS{"a/manifest.yaml": {Data: []byte(manifest)}, "a/compose.yaml.tmpl": {Data: []byte(compose)}, "a/files/conf/x.sh": {Data: []byte("x")}})
	if err == nil || !strings.Contains(err.Error(), "files/conf/x.sh is bound by no service") {
		t.Fatalf("unbound file: %v", err)
	}
}

func TestMoveRejects(t *testing.T) {
	const binds = compose + "    volumes: [./pgdata:/v]\n"
	move := func(from, to string) string {
		return "upgrade:\n  move:\n    from: " + from + "\n    to: " + to + "\n    what: x\n"
	}
	for name, c := range map[string]struct{ manifest, compose string }{
		"outside binds": {manifest + move("other/a", "pgdata/b"), binds},
		"bind itself":   {manifest + move("pgdata", "pgdata/b"), binds},
		"same file":     {manifest + move("pgdata/a", "pgdata/a"), binds},
		"absolute":      {manifest + move("/pgdata/a", "pgdata/b"), binds},
		"no what":       {manifest + strings.Replace(move("pgdata/a", "pgdata/b"), "    what: x\n", "", 1), binds},
		"kubernetes":    {manifest + kube + move("pgdata/a", "pgdata/b"), binds},
	} {
		if _, err := Load(fstest.MapFS{"a/manifest.yaml": {Data: []byte(c.manifest)}, "a/compose.yaml.tmpl": {Data: []byte(c.compose)}}); err == nil || !strings.Contains(err.Error(), "move needs") {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := Load(fstest.MapFS{"a/manifest.yaml": {Data: []byte(manifest + move("pgdata/a", "pgdata/b"))}, "a/compose.yaml.tmpl": {Data: []byte(binds)}}); err != nil {
		t.Fatal(err)
	}
}
