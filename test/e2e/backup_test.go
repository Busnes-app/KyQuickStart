//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io/fs"
	"math/big"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Busnes-app/kyquickstart/internal/cli"
	"github.com/Busnes-app/kyquickstart/internal/dockerhost"
	"github.com/Busnes-app/kyquickstart/internal/stack"
	"go.yaml.in/yaml/v3"
	"golang.org/x/crypto/bcrypt"
)

// restServerImage is rest-server 0.14.0's linux/amd64 manifest (index sha256:d2aff06f47eb…),
// from `docker buildx imagetools inspect docker.io/restic/rest-server:0.14.0` on 2026-10-19.
const restServerImage = "docker.io/restic/rest-server:0.14.0@sha256:e117e428e511e3ffdeb6d0e578adf4b468803c77ad500993bb8f15038b2d9db3"

// restServer is a disposable append-only rest-server: TLS from a test CA, one htpasswd user
// per target. It sits on its own network and is published on the default bridge's gateway:
// Docker forwards a published port into another bridge, never back into the one it came from.
type restServer struct {
	addr      string // gateway:port, named by the certificate
	caPEM     []byte
	passwords map[string]string
}

func (r restServer) repository(user string) string { return "rest:https://" + r.addr + "/" + user }

func startRestServer(t *testing.T, work string, users ...string) restServer {
	t.Helper()
	gw := ""
	for _, g := range strings.Fields(docker(t, "network", "inspect", "bridge", "--format", "{{range .IPAM.Config}}{{.Gateway}} {{end}}")) {
		if strings.Contains(g, ".") {
			gw = g
		}
	}
	if gw == "" {
		t.Fatal("Docker's default bridge has no IPv4 gateway")
	}
	dir, data := filepath.Join(work, "rest-server"), filepath.Join(work, "rest-server-data")
	os.Mkdir(dir, 0o700)
	os.Mkdir(data, 0o700)
	caKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "kyquickstart e2e rest-server CA"}, IsCA: true, BasicConstraintsValid: true,
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour), KeyUsage: x509.KeyUsageCertSign}
	caDER, err := x509.CreateCertificate(rand.Reader, ca, ca, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	ca, _ = x509.ParseCertificate(caDER)
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	leaf := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: gw}, IPAddresses: []net.IP{net.ParseIP(gw)},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	leafDER, err := x509.CreateCertificate(rand.Reader, leaf, ca, &key.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, _ := x509.MarshalECPrivateKey(key)
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})
	r := restServer{caPEM: caPEM, passwords: map[string]string{}}
	var htpasswd bytes.Buffer
	for _, u := range users {
		b := make([]byte, 24)
		rand.Read(b)
		r.passwords[u] = hex.EncodeToString(b)
		h, err := bcrypt.GenerateFromPassword([]byte(r.passwords[u]), bcrypt.DefaultCost)
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&htpasswd, "%s:%s\n", u, h)
	}
	for name, b := range map[string][]byte{"cert.pem": pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER}), "key.pem": pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), "htpasswd": htpasswd.Bytes()} {
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	port := freePort(t, gw)
	r.addr = net.JoinHostPort(gw, strconv.Itoa(port))
	id := make([]byte, 4)
	rand.Read(id)
	name := "kyq-e2e-rest-" + hex.EncodeToString(id)
	docker(t, "network", "create", name)
	t.Cleanup(func() { exec.Command("docker", "network", "rm", name).Run() })
	t.Cleanup(func() { exec.Command("docker", "rm", "-fv", name).Run() })
	docker(t, "run", "-d", "--name", name, "--network", name, "--memory", "256m", "--cpus", "1", "--pids-limit", "64", "--read-only", "--cap-drop", "ALL",
		"--user", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()), "-p", r.addr+":8000", "-v", dir+":/conf:ro", "-v", data+":/data", restServerImage,
		"rest-server", "--path", "/data", "--listen", ":8000", "--append-only", "--private-repos", "--htpasswd-file", "/conf/htpasswd",
		"--tls", "--tls-cert", "/conf/cert.pem", "--tls-key", "/conf/key.pem")
	pool := x509.NewCertPool()
	pool.AddCert(ca)
	for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(200 * time.Millisecond) {
		c, err := tls.DialWithDialer(&net.Dialer{Timeout: 2 * time.Second}, "tcp", r.addr, &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12})
		if err == nil {
			c.Close()
			break
		}
		if time.Now().After(deadline) {
			logs, _ := exec.Command("docker", "logs", name).CombinedOutput()
			t.Fatalf("rest-server at %s: %v\n%s", r.addr, err, logs)
		}
	}
	return r
}

// targetFile writes content to path on the sshd target, created through it so its user owns it.
func targetFile(t *testing.T, sshd, path, mode string, content []byte) {
	t.Helper()
	cmd := exec.Command("docker", "exec", "-i", sshd, "sh", "-c", `umask 077; mkdir -p "$(dirname "$1")" && cat > "$1" && chmod "$2" "$1"`, "sh", path, mode)
	cmd.Stdin = bytes.NewReader(content)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("write %s on the target: %v\n%s", path, err, out)
	}
}

// destination gives the sshd target the rest-server user's credential and CA in dir.
func (r restServer) destination(t *testing.T, sshd, dir, user string) *stack.BackupDestination {
	t.Helper()
	d := &stack.BackupDestination{Repository: r.repository(user), CredentialFile: dir + "/rest-password", CAFile: dir + "/rest-ca.pem"}
	targetFile(t, sshd, d.CredentialFile, "600", []byte(r.passwords[user]))
	targetFile(t, sshd, d.CAFile, "644", r.caPEM)
	return d
}

// restic runs restic as the test, outside kyquickstart, against app's repository of d with
// the repository password file pw on the target (whose paths the host shares). No password
// is in its argv or env.
func restic(d *stack.BackupDestination, app, pw string, args ...string) (string, error) {
	all := []string{"run", "--rm", "--memory", "256m", "--memory-swap", "256m", "--cpus", "1", "--pids-limit", "64", "--network", "bridge",
		"-e", "RESTIC_REPOSITORY=" + d.Repository + "/" + app, "-e", "RESTIC_REST_USERNAME=" + d.RestUser(), "-e", "RESTIC_PASSWORD_FILE=/run/pw",
		"-v", pw + ":/run/pw:ro", "-v", d.CredentialFile + ":/run/rest:ro", "-v", d.CAFile + ":/run/ca.pem:ro",
		"--entrypoint", "/bin/sh", dockerhost.ResticImage, "-c", `RESTIC_REST_PASSWORD=$(cat /run/rest) exec restic --no-cache --cacert /run/ca.pem "$@"`, "restic"}
	out, err := exec.Command("docker", append(all, args...)...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

type resticSnapshot struct {
	ID    string   `json:"id"`
	Short string   `json:"short_id"`
	Paths []string `json:"paths"`
	Tags  []string `json:"tags"`
}

func snapshots(t *testing.T, d *stack.BackupDestination, app, pw string) []resticSnapshot {
	t.Helper()
	out, err := restic(d, app, pw, "snapshots", "--json")
	var list []resticSnapshot
	if err != nil || json.Unmarshal([]byte(out), &list) != nil {
		t.Fatalf("restic snapshots: %v\n%s", err, out)
	}
	return list
}

// resticWatch records, while it runs, every restic container's Config.Env and argv as
// docker inspect shows them, and every restic container created.
type resticWatch struct {
	stop    chan struct{}
	done    sync.WaitGroup
	mu      sync.Mutex
	inspect []string
	since   string
}

func watchRestic() *resticWatch {
	now := time.Now()
	w := &resticWatch{stop: make(chan struct{}), since: fmt.Sprintf("%d.%09d", now.Unix(), now.Nanosecond())}
	w.done.Add(1)
	go func() {
		defer w.done.Done()
		seen := map[string]bool{}
		for {
			select {
			case <-w.stop:
				return
			case <-time.After(50 * time.Millisecond):
			}
			ids, _ := exec.Command("docker", "ps", "-q", "--no-trunc", "--filter", "ancestor="+dockerhost.ResticImage).Output()
			for _, id := range strings.Fields(string(ids)) {
				if seen[id] {
					continue
				}
				if b, err := exec.Command("docker", "inspect", id, "--format", "{{json .Config.Env}} {{json .Config.Cmd}} {{json .Args}} {{json .Config.Entrypoint}}").Output(); err == nil {
					seen[id] = true
					w.mu.Lock()
					w.inspect = append(w.inspect, string(b))
					w.mu.Unlock()
				}
			}
		}
	}()
	return w
}

// end stops the watch and returns the inspections and the restic containers Docker created
// since it began.
func (w *resticWatch) end(t *testing.T) (inspect []string, created int) {
	t.Helper()
	close(w.stop)
	w.done.Wait()
	out := docker(t, "events", "--since", w.since, "--until", strconv.FormatInt(time.Now().Unix()+1, 10),
		"--filter", "type=container", "--filter", "event=create", "--format", "{{.Actor.Attributes.image}}")
	for _, image := range strings.Fields(out) {
		if strings.Contains(image, "restic/restic") {
			created++
		}
	}
	return w.inspect, created
}

// dirDigest lists every path under dir on the target with each file's sha256.
func dirDigest(t *testing.T, sshd, dir string) string {
	t.Helper()
	return docker(t, "exec", sshd, "sh", "-c", `cd "$1" && find . | sort && find . -type f -exec sha256sum {} + | sort -k2`, "sh", dir)
}

// noSecrets fails when any secret is in any output or under the state directory.
func noSecrets(t *testing.T, state string, outputs []string, secrets map[string]string) {
	t.Helper()
	for name, s := range secrets {
		if len(s) < 16 {
			t.Fatalf("secret %s is %d bytes", name, len(s))
		}
		for _, o := range outputs {
			if strings.Contains(o, s) {
				t.Errorf("%s printed", name)
			}
		}
	}
	filepath.WalkDir(state, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			b, _ := os.ReadFile(p)
			for name, s := range secrets {
				if bytes.Contains(b, []byte(s)) {
					t.Errorf("%s stored in %s", name, p)
				}
			}
		}
		return nil
	})
}

// checkResticInspect fails when a restic container's env or argv holds a secret, and when
// none was caught.
func checkResticInspect(t *testing.T, inspect []string, secrets map[string]string) {
	t.Helper()
	if len(inspect) == 0 {
		t.Fatal("no restic container was caught running")
	}
	for _, i := range inspect {
		// Config.Env comes first; the wrapper in argv names RESTIC_REST_PASSWORD, not its value.
		if env, _, _ := strings.Cut(i, "] "); strings.Contains(env, "RESTIC_PASSWORD=") || strings.Contains(env, "RESTIC_REST_PASSWORD=") {
			t.Errorf("restic container carries a password variable: %s", i)
		}
		for name, s := range secrets {
			if strings.Contains(i, s) {
				t.Errorf("restic container's env or argv holds %s", name)
			}
		}
	}
	t.Logf("restic container caught during backup: %s", inspect[0])
}

func sha(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// testDumpBackup backs up and restores the dumpdemo fixture (a Postgres dump, a file volume
// with a skip path and a capsule secret) from a non-demo plan in its own root on the sshd
// target, to a disposable append-only rest-server.
func testDumpBackup(t *testing.T, ctx context.Context, demoState, port, base, sshd string) {
	work := t.TempDir()
	state, cat := filepath.Join(work, "state"), filepath.Join(work, "apps")
	failing := filepath.Join(work, "failing")
	os.MkdirAll(state, 0o700)
	for _, c := range []string{cat, failing} {
		if err := os.CopyFS(filepath.Join(c, "dumpdemo"), os.DirFS("../../internal/catalog/testdata/apps/dumpdemo")); err != nil {
			t.Fatal(err)
		}
	}
	// A dump command that fails: no snapshot may follow.
	manifest := filepath.Join(failing, "dumpdemo", "manifest.yaml")
	b, _ := os.ReadFile(manifest)
	const dump = "command: [pg_dump, -U, postgres, --clean, --if-exists, postgres]"
	if !bytes.Contains(b, []byte(dump)) {
		t.Fatal("dumpdemo's dump command changed; update the failing variant")
	}
	os.WriteFile(manifest, bytes.Replace(b, []byte(dump), []byte("command: [pg_dump, -U, postgres, no_such_database]"), 1), 0o600)
	kh, _ := os.ReadFile(filepath.Join(demoState, "known_hosts"))
	os.WriteFile(filepath.Join(state, "known_hosts"), kh, 0o600)

	srv := startRestServer(t, work, "e2e")
	dest := srv.destination(t, sshd, base+"/backup-files", "e2e")
	root := base + "/backup-root"
	t.Cleanup(func() { exec.Command("docker", "compose", "-p", "kyq-dumpdemo", "down", "-v").Run() })
	st := stack.Stack{Version: 1, Targets: []stack.Target{{Name: "e2e", Host: "127.0.0.1", User: "root", Root: root, Backup: dest}}, Apps: []stack.App{{Name: "dumpdemo", Target: "e2e"}}}
	st.Targets[0].Port, _ = strconv.Atoi(port)
	raw, _ := yaml.Marshal(st)
	os.WriteFile(filepath.Join(state, "stack.yaml"), raw, 0o600)

	var outputs []string
	run := func(catalogDir string, stdin string, args ...string) (string, error) {
		t.Helper()
		var out bytes.Buffer
		o := cli.Options{Catalog: os.DirFS(catalogDir), ReleaseSet: "e2e", Out: &out}
		if stdin != "" {
			o.Stdin = strings.NewReader(stdin)
		}
		start := time.Now()
		err := cli.Run(ctx, append(args, "--state", state), o)
		t.Logf("%s (%s): %v\n%s", strings.Join(args, " "), time.Since(start).Round(time.Second), err, out.String())
		outputs = append(outputs, out.String())
		return out.String(), err
	}
	out, err := run(cat, "", "apply")
	if err != nil || status(out, "dumpdemo.deploy") != "ok" {
		t.Fatalf("apply dumpdemo: %v", err)
	}
	app := root + "/dumpdemo"
	checkBackupHandover(t, out, "dumpdemo", st.Targets[0])
	db := "kyq-dumpdemo-db-1"
	psql := func(sql string) string {
		return docker(t, "exec", db, "psql", "-U", "postgres", "-At", "-v", "ON_ERROR_STOP=1", "-c", sql)
	}
	psql("CREATE TABLE notes (id int PRIMARY KEY, body text NOT NULL)")
	psql("INSERT INTO notes VALUES (1, 'before the backup'), (2, 'also before')")
	docker(t, "exec", sshd, "sh", "-c", `cd "$1" && mkdir -p files/docs files/cache && printf 'kept file\n' > files/docs/a.txt && head -c 200000 /dev/urandom > files/docs/b.bin && printf 'cache\n' > files/cache/skipped`, "sh", app)
	files := func() string {
		return docker(t, "exec", sshd, "sh", "-c", `cd "$1" && find files -type f -exec sha256sum {} + | sort -k2`, "sh", app)
	}
	filesBefore := files()
	rows := psql("SELECT id, body FROM notes ORDER BY id")
	dbPassword := docker(t, "exec", sshd, "cat", app+"/secrets/db_password")
	containers := func() string {
		return docker(t, "ps", "-q", "--no-trunc", "--filter", "label=com.docker.compose.project=kyq-dumpdemo")
	}
	before := containers()

	// Two backups: two snapshots; the password and the repository are created once.
	w := watchRestic()
	start := time.Now()
	out, err = run(cat, "", "backup", "dumpdemo")
	firstBackup := time.Since(start)
	inspect, _ := w.end(t)
	pwFile := app + "/secrets/restic_password"
	created := "This run created dumpdemo's repository password. Read it once into your printed record:\n  ssh -p " + port + " root@127.0.0.1 cat " + pwFile + "\n"
	if err != nil || !strings.Contains(out, created) {
		t.Fatalf("first backup: %v", err)
	}
	password := docker(t, "exec", sshd, "cat", pwFile)
	secrets := map[string]string{"restic_password": password, "the rest-server password": srv.passwords["e2e"], "db_password": dbPassword}
	checkResticInspect(t, inspect, secrets)
	if out, err = run(cat, "", "backup", "dumpdemo"); err != nil || strings.Contains(out, "repository password") {
		t.Fatalf("second backup: %v", err)
	}
	if got := docker(t, "exec", sshd, "cat", pwFile); got != password {
		t.Fatal("the second backup changed restic_password")
	}
	if containers() != before {
		t.Fatal("a dump backup restarted the app")
	}
	list := snapshots(t, dest, "dumpdemo", pwFile)
	if len(list) != 2 {
		t.Fatalf("%d snapshots after two backups", len(list))
	}
	snap := list[1]
	if strings.Join(snap.Paths, " ") != "/kyq/dump /kyq/secrets/db_password /kyq/volumes/files" {
		t.Errorf("snapshot paths %v", snap.Paths)
	}
	if !strings.Contains(out, "dumpdemo: snapshot "+snap.ID+" in "+dest.Repository+"/dumpdemo (restic check passed") {
		t.Fatalf("second backup did not print snapshot %s", snap.ID)
	}
	if mode := docker(t, "exec", sshd, "stat", "-c", "%a %u", pwFile); mode != "600 0" {
		t.Fatalf("restic_password is %s", mode)
	}

	// What the snapshot holds, and what it does not.
	ls, err := restic(dest, "dumpdemo", pwFile, "ls", snap.ID)
	if err != nil {
		t.Fatalf("restic ls: %v\n%s", err, ls)
	}
	for _, p := range []string{"/kyq/dump", "/kyq/volumes/files/docs/a.txt", "/kyq/volumes/files/docs/b.bin", "/kyq/secrets/db_password"} {
		if !strings.Contains("\n"+ls+"\n", "\n"+p+"\n") {
			t.Errorf("snapshot lacks %s:\n%s", p, ls)
		}
	}
	for _, p := range []string{"cache", "pgdata", "postgresql", "restic_password", "PG_VERSION"} {
		if strings.Contains(ls, p) {
			t.Errorf("snapshot holds %s:\n%s", p, ls)
		}
	}
	for _, tag := range []string{"kyquickstart", "app=dumpdemo", "version=1.0.0"} {
		if !strings.Contains(strings.Join(snap.Tags, " "), tag) {
			t.Errorf("snapshot tags %v lack %s", snap.Tags, tag)
		}
	}

	// A failing dump takes no snapshot.
	if _, err := run(failing, "", "backup", "dumpdemo"); err == nil || !strings.Contains(err.Error(), "no snapshot taken") {
		t.Fatalf("backup with a failing dump: %v", err)
	}
	if n := len(snapshots(t, dest, "dumpdemo", pwFile)); n != 2 {
		t.Fatalf("a failing dump left %d snapshots", n)
	}

	// Written after the backup: gone after the restore.
	psql("INSERT INTO notes VALUES (3, 'after the backup')")
	docker(t, "exec", sshd, "sh", "-c", `printf 'late\n' > "$1"/files/docs/late.txt`, "sh", app)

	// Disaster: the project, its database volume and its whole directory are gone.
	docker(t, "compose", "-p", "kyq-dumpdemo", "down", "-v")
	docker(t, "exec", sshd, "rm", "-rf", app)
	start = time.Now()
	out, err = run(cat, password+"\ndumpdemo\n", "restore", "dumpdemo", "--snapshot", snap.ID)
	restoreTook := time.Since(start)
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	for _, want := range []string{"Restore dumpdemo from snapshot " + snap.ID + "\n  taken ", " on e2e\n  paths " + strings.Join(snap.Paths, " ") + "\nType dumpdemo to restore it: ", "dumpdemo: dump restored into db.\n", "dumpdemo: restored from snapshot " + snap.ID + " and healthy.\n"} {
		if !strings.Contains(out, want) {
			t.Errorf("restore output lacks %q", want)
		}
	}
	if got := psql("SELECT id, body FROM notes ORDER BY id"); got != rows {
		t.Fatalf("rows after restore:\n%s\nwant:\n%s", got, rows)
	}
	if got, want := files(), dropLines(filesBefore, "files/cache/"); got != want {
		t.Fatalf("files after restore:\n%s\nwant:\n%s", got, want)
	}
	if got := docker(t, "exec", sshd, "sh", "-c", `test -e "$1"/files/cache && echo present || echo absent`, "sh", app); got != "absent" {
		t.Fatal("the skip path came back")
	}
	if got := docker(t, "exec", sshd, "cat", app+"/secrets/db_password"); got != dbPassword {
		t.Fatal("db_password differs after restore")
	}
	if got := docker(t, "exec", sshd, "cat", pwFile); got != password {
		t.Fatal("restic_password differs after restore")
	}
	if got := docker(t, "exec", sshd, "stat", "-c", "%n %a %u", pwFile, app+"/secrets/db_password"); got != pwFile+" 600 0\n"+app+"/secrets/db_password 600 0" {
		t.Fatalf("restored secrets:\n%s", got)
	}
	if got := docker(t, "exec", sshd, "sh", "-c", `ls -A "$1"`, "sh", app); strings.Contains(got, ".restore") || strings.Contains(got, ".backup") {
		t.Fatalf("restore left scratch behind: %s", got)
	}
	noSecrets(t, state, outputs, secrets)
	t.Logf("dumpdemo: first backup %s, restore %s", firstBackup.Round(time.Second), restoreTook.Round(time.Second))
}

// checkBackupHandover asserts apply's backup lines for app on target t, line by line.
func checkBackupHandover(t *testing.T, out, app string, target stack.Target) {
	t.Helper()
	want := []string{
		app + " backups: kyquickstart backup " + app + " takes an encrypted snapshot to " + target.Backup.Repository + "/" + app + "; nothing runs on a schedule.",
		"  After the first backup, read its repository password once into your printed record (a lost",
		"  target cannot be restored without it): ssh -p " + strconv.Itoa(target.Port) + " " + target.User + "@" + target.Host + " cat " + target.Root + "/" + app + "/secrets/restic_password",
		"  Restore: kyquickstart restore " + app + " lists its snapshots; add --snapshot ID to restore one.",
	}
	lines := strings.Split(out, "\n")
	for i, l := range lines {
		if l == want[0] {
			if len(lines) < i+len(want) || strings.Join(lines[i:i+len(want)], "\n") != strings.Join(want, "\n") {
				t.Fatalf("%s's backup handover:\n%s\nwant:\n%s", app, strings.Join(lines[i:min(len(lines), i+len(want))], "\n"), strings.Join(want, "\n"))
			}
			return
		}
	}
	t.Fatalf("no backup handover for %s", app)
}

// dropLines removes the lines containing s.
func dropLines(text, s string) string {
	var kept []string
	for _, l := range strings.Split(text, "\n") {
		if !strings.Contains(l, s) {
			kept = append(kept, l)
		}
	}
	return strings.Join(kept, "\n")
}
