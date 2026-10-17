package runner

import (
	"archive/tar"
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"syscall"

	"github.com/Busnes-app/ky-primitives/keyfile"
	"github.com/Busnes-app/kyquickstart/internal/dockerhost"
	"github.com/Busnes-app/kyquickstart/internal/stack"
	"github.com/google/uuid"
	"go.yaml.in/yaml/v3"
	"golang.org/x/crypto/ssh"
	"k8s.io/client-go/tools/clientcmd"
)

// The state directory holds the runner's identity and credentials beside plan/, the CLI state
// directory the runner works in. plan/ stays secret-free, as on the workstation.
const (
	identityFile = "runner.json"
	keyFile      = "ssh/id_ed25519"
	planDir      = "plan"
	opsDir       = "operations"
	kubeDir      = "kubeconfigs"
)

// openState returns dir as an absolute path to a private directory of this user, creating it.
func openState(dir string, podVolume bool) (string, error) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	if err := privateDir("state", dir, podVolume); err != nil {
		return "", err
	}
	for _, d := range []string{planDir, opsDir, kubeDir, filepath.Dir(keyFile)} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o700); err != nil {
			return "", err
		}
	}
	return dir, nil
}

// privateDir creates dir 0700 and checks that it is this user's alone, under ancestors no
// other user can modify. With podVolume, dir's parent is a pod volume's root, which the
// kubelet creates world-writable: it must be a mount point owned by root or this user, and
// the ancestry check starts above it.
func privateDir(what, dir string, podVolume bool) error {
	trusted := dir
	if podVolume {
		root := filepath.Dir(dir)
		if err := volumeRoot(root); err != nil {
			return fmt.Errorf("%s volume %s: %w", what, root, err)
		}
		trusted = filepath.Dir(root)
	}
	if err := keyfile.RequireTrustedAncestry(trusted); err != nil {
		return fmt.Errorf("%s directory has an ancestor other users can modify", what)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("cannot create the private %s directory", what)
	}
	if err := keyfile.RequirePrivateDirectory(dir); err != nil {
		return fmt.Errorf("%s directory must be private (0700) and owned by the runner's user", what)
	}
	return nil
}

// volumeRoot refuses root unless it is a directory owned by root or this user and a mount
// point: its device differs from its parent's. Tests replace it.
var volumeRoot = func(root string) error {
	fi, err := os.Lstat(root)
	if err != nil {
		return err
	}
	parent, err := os.Stat(filepath.Dir(root))
	if err != nil {
		return err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	pst, pok := parent.Sys().(*syscall.Stat_t)
	switch {
	case !fi.IsDir() || !ok || !pok:
		return errors.New("not a directory")
	case st.Dev == pst.Dev:
		return errors.New("not a mount point")
	case st.Uid != 0 && st.Uid != uint32(os.Geteuid()):
		return errors.New("owned by another user")
	}
	return nil
}

// writeNew publishes data at file unless it exists: a rerun after a crash keeps the first.
func writeNew(file string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(file), ".kyq-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, err = f.Write(data)
	if err = errors.Join(err, f.Sync(), f.Close()); err != nil {
		return err
	}
	if err := os.Link(f.Name(), file); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	return nil
}

// writeFile replaces file atomically with a private copy of data.
func writeFile(file string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(file), ".kyq-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, err = f.Write(data)
	if err = errors.Join(err, f.Sync(), f.Close()); err != nil {
		return err
	}
	return os.Rename(f.Name(), file)
}

type identity struct {
	RunnerID string `json:"runner_id"`
}

func validUUID(s string) bool {
	u, err := uuid.Parse(s)
	return err == nil && u.String() == s && u.Version() == 4
}

// runnerID reads the runner's ID, choosing it on first use.
func runnerID(state string) (string, error) {
	file := filepath.Join(state, identityFile)
	b, err := json.Marshal(identity{uuid.NewString()})
	if err == nil {
		err = writeNew(file, b)
	}
	if err != nil {
		return "", err
	}
	b, err = keyfile.ReadPrivate(file, 4096)
	if err != nil {
		return "", fmt.Errorf("%s: %w", identityFile, err)
	}
	var id identity
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if d.Decode(&id) != nil || !validUUID(id.RunnerID) {
		return "", fmt.Errorf("%s is malformed", identityFile)
	}
	return id.RunnerID, nil
}

// keygen creates the runner's SSH key once and prints only its public line, commented with
// the runner's ID.
func keygen(state string, out io.Writer) error {
	id, err := runnerID(state)
	if err != nil {
		return err
	}
	file := filepath.Join(state, keyFile)
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		return err
	}
	if err := writeNew(file, pem.EncodeToMemory(block)); err != nil {
		return err
	}
	b, err := keyfile.ReadPrivate(file, 16<<10)
	if err != nil {
		return err
	}
	signer, err := ssh.ParsePrivateKey(b)
	clear(b)
	if err != nil {
		return fmt.Errorf("%s: %w", keyFile, err)
	}
	_, err = fmt.Fprintf(out, "%s %s%s\n", bytes.TrimSpace(ssh.MarshalAuthorizedKey(signer.PublicKey())), dockerhost.RunnerKeyComment, id)
	return err
}

// Import limits. A plan's results are one small JSON file per step.
const (
	maxImport  = 16 << 20
	maxEntries = 4096
	maxPlan    = 1 << 20
	maxSmall   = 64 << 10
)

var (
	resultRE     = regexp.MustCompile(`^results/[A-Za-z0-9][A-Za-z0-9._-]{0,127}\.json$`)
	kubeconfigRE = regexp.MustCompile(`^kubeconfigs/([a-z][a-z0-9-]{0,30})\.yaml$`)
)

func importLimit(name string) int64 {
	switch {
	case name == "stack.yaml", name == "known_hosts":
		return maxPlan
	case resultRE.MatchString(name), kubeconfigRE.MatchString(name):
		return maxSmall
	}
	return 0
}

// importPlan reads a tar of stack.yaml, known_hosts, results/*.json and kubeconfigs/<target>.yaml
// into state. It refuses anything else, and a plan older than the one the runner holds. Each
// Kubernetes target's kubeconfig becomes the runner's copy.
func importPlan(state string, r io.Reader) error {
	files := map[string][]byte{}
	lr := &io.LimitedReader{R: r, N: maxImport + 1}
	tooBig := fmt.Errorf("import exceeds %d bytes", maxImport)
	tr := tar.NewReader(lr)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if lr.N <= 0 {
			return tooBig
		}
		if err != nil {
			return fmt.Errorf("import: %w", err)
		}
		limit := importLimit(h.Name)
		switch {
		case limit == 0:
			return fmt.Errorf("import: unexpected entry %q", h.Name)
		case h.Typeflag != tar.TypeReg:
			return fmt.Errorf("import: %s is not a regular file", h.Name)
		case h.Size > limit:
			return fmt.Errorf("import: %s exceeds %d bytes", h.Name, limit)
		case files[h.Name] != nil:
			return fmt.Errorf("import: %s appears twice", h.Name)
		case len(files) == maxEntries:
			return errors.New("import: too many entries")
		}
		b, err := io.ReadAll(tr)
		if lr.N <= 0 {
			return tooBig
		}
		if err != nil {
			return fmt.Errorf("import: %w", err)
		}
		files[h.Name] = b
	}
	if lr.N <= 0 {
		return tooBig
	}
	st, err := stack.Parse(files["stack.yaml"])
	if err != nil {
		return fmt.Errorf("import: %w", err)
	}
	kubeconfigs := map[string][]byte{}
	for i, t := range st.Targets {
		if !t.Kubernetes() {
			continue
		}
		name := "kubeconfigs/" + t.Name + ".yaml"
		kc := files[name]
		if err := tokenOnly(kc); err != nil {
			return fmt.Errorf("import: %s: %w", name, err)
		}
		kubeconfigs[t.Name] = kc
		st.Targets[i].Kubeconfig, st.Targets[i].Context = filepath.Join(state, kubeDir, t.Name+".yaml"), ""
	}
	for name := range files {
		if m := kubeconfigRE.FindStringSubmatch(name); m != nil && kubeconfigs[m[1]] == nil {
			return fmt.Errorf("import: %s names no Kubernetes target", name)
		}
	}
	data, err := yaml.Marshal(st)
	if err != nil {
		return err
	}

	plan := filepath.Join(state, planDir)
	unlock, err := lockPlan(plan)
	if err != nil {
		return err
	}
	defer unlock()
	if old, err := stack.Load(filepath.Join(plan, "stack.yaml")); err == nil && old.Revision > st.Revision {
		return fmt.Errorf("import: the runner holds plan revision %d, newer than %d; run kyquickstart pull-plan", old.Revision, st.Revision)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("the runner's plan: %w", err)
	}
	for target, kc := range kubeconfigs {
		if err := writeFile(filepath.Join(state, kubeDir, target+".yaml"), kc); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Join(plan, "results"), 0o700); err != nil {
		return err
	}
	for name, b := range files {
		if name == "known_hosts" || resultRE.MatchString(name) {
			if err := writeFile(filepath.Join(plan, name), b); err != nil {
				return err
			}
		}
	}
	// stack.yaml last: the plan is published only with everything it needs.
	return writeFile(filepath.Join(plan, "stack.yaml"), data)
}

// tokenOnly accepts a kubeconfig that authenticates with a bearer token only and verifies
// the API server with an inline CA: nothing in it may run a program or read another file.
func tokenOnly(b []byte) error {
	if b == nil {
		return errors.New("missing")
	}
	cfg, err := clientcmd.Load(b)
	if err != nil {
		return err
	}
	for _, a := range cfg.AuthInfos {
		if a.Token == "" || a.Exec != nil || a.AuthProvider != nil || a.TokenFile != "" || a.ClientCertificate != "" || a.ClientKey != "" ||
			len(a.ClientCertificateData) > 0 || len(a.ClientKeyData) > 0 || a.Username != "" || a.Impersonate != "" {
			return errors.New("must authenticate with a token only")
		}
	}
	for _, c := range cfg.Clusters {
		if c.InsecureSkipTLSVerify || c.CertificateAuthority != "" || len(c.CertificateAuthorityData) == 0 || c.ProxyURL != "" {
			return errors.New("must verify the API server with an inline CA and no proxy")
		}
	}
	return nil
}

// lockPlan takes plan.lock, the CLI's plan editor lock.
func lockPlan(plan string) (func(), error) {
	lock := filepath.Join(plan, "plan.lock")
	f, err := os.OpenFile(lock, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, errors.New("the runner's plan is being written; if nothing is running, remove plan/plan.lock in the runner volume")
	}
	f.Close()
	return func() { os.Remove(lock) }, nil
}

// export prints the runner's stack.yaml.
func export(state string, out io.Writer) error {
	b, err := os.ReadFile(filepath.Join(state, planDir, "stack.yaml"))
	if errors.Is(err, os.ErrNotExist) {
		return errors.New("no plan has been handed to this runner")
	}
	if err != nil {
		return err
	}
	if _, err := stack.Parse(b); err != nil {
		return err
	}
	_, err = out.Write(b)
	return err
}

// hasPlan reports whether a plan has been handed to the runner.
func hasPlan(state string) bool {
	_, err := os.Stat(filepath.Join(state, planDir, "stack.yaml"))
	return err == nil
}
