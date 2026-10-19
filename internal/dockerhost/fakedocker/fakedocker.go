// Package fakedocker is a test-only docker CLI: restic in the pinned image against a
// repository directory, and one Compose project. Build compiles it once (a small binary
// starts fast, unlike a race-built test binary) and Install puts it on PATH as docker, so the
// commands around it run in a real sh. It is imported by tests only.
package fakedocker

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// Dir holds the fake's state: log (one JSON Call per line), repo/, compose.json, and the
// dump the Compose service prints (dump) and receives (loaded).
const dirEnv = "KYQ_FAKE_DOCKER"

// Call is one docker invocation as logged: its argv and, for docker run, its -e values.
type Call struct {
	Args []string `json:"args"`
	Env  []string `json:"env,omitempty"`
}

// Main runs the fake.
func Main() {
	code, err := run(os.Getenv(dirEnv), os.Args[1:], os.Stdin, os.Stdout)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
	}
	os.Exit(code)
}

// bin is the directory Build compiled docker into.
var bin string

// Build compiles the fake into a temporary directory, for TestMain; clean removes it.
func Build() (clean func(), err error) {
	if bin, err = os.MkdirTemp("", "fakedocker"); err != nil {
		return nil, err
	}
	clean = func() { os.RemoveAll(bin) }
	out, err := exec.Command("go", "build", "-o", filepath.Join(bin, "docker"), "github.com/Busnes-app/kyquickstart/internal/dockerhost/fakedocker/docker").CombinedOutput()
	if err != nil {
		clean()
		return nil, fmt.Errorf("build fake docker: %w\n%s", err, out)
	}
	return clean, nil
}

// Install puts the built docker on PATH for this test and returns the fake's state
// directory. The project's services start running.
func Install(t *testing.T, services ...string) string {
	t.Helper()
	if bin == "" {
		t.Fatal("fakedocker.Build did not run in TestMain")
	}
	dir := t.TempDir()
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv(dirEnv, dir)
	state := map[string]bool{}
	for _, s := range services {
		state[s] = true
	}
	writeJSON(filepath.Join(dir, "compose.json"), state)
	return dir
}

// Calls reads the log.
func Calls(t *testing.T, dir string) []Call {
	t.Helper()
	b, _ := os.ReadFile(filepath.Join(dir, "log"))
	var out []Call
	for _, line := range bytes.Split(bytes.TrimSpace(b), []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var c Call
		if err := json.Unmarshal(line, &c); err != nil {
			t.Fatal(err)
		}
		out = append(out, c)
	}
	return out
}

// Running reads which services run.
func Running(t *testing.T, dir string) map[string]bool {
	t.Helper()
	m := map[string]bool{}
	b, err := os.ReadFile(filepath.Join(dir, "compose.json"))
	if err != nil || json.Unmarshal(b, &m) != nil {
		t.Fatal("compose.json", err)
	}
	return m
}

func writeJSON(file string, v any) {
	b, _ := json.Marshal(v)
	os.WriteFile(file, b, 0o600)
}

type snapshot struct {
	ID    string    `json:"id"`
	Short string    `json:"short_id"`
	Time  time.Time `json:"time"`
	Host  string    `json:"hostname"`
	Paths []string  `json:"paths"`
	Tags  []string  `json:"tags"`
}

func run(dir string, args []string, stdin io.Reader, stdout io.Writer) (int, error) {
	c := Call{Args: args}
	defer func() {
		f, err := os.OpenFile(filepath.Join(dir, "log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err == nil {
			b, _ := json.Marshal(c)
			f.Write(append(b, '\n'))
			f.Close()
		}
	}()
	switch {
	case len(args) > 0 && args[0] == "run":
		return restic(dir, args[1:], &c, stdout)
	case len(args) > 0 && args[0] == "compose":
		return compose(dir, args[1:], stdin, stdout)
	case len(args) > 0 && args[0] == "ps":
		state := map[string]bool{}
		b, _ := os.ReadFile(filepath.Join(dir, "compose.json"))
		json.Unmarshal(b, &state)
		for _, up := range state {
			if up || !slices.Contains(args, "status=running") {
				fmt.Fprintln(stdout, "c0ffee")
				break
			}
		}
		return 0, nil
	}
	return 1, fmt.Errorf("fake docker: unexpected %v", args)
}

func compose(dir string, args []string, stdin io.Reader, stdout io.Writer) (int, error) {
	for len(args) > 0 && (args[0] == "-p" || args[0] == "-f") {
		args = args[2:]
	}
	file := filepath.Join(dir, "compose.json")
	state := map[string]bool{}
	b, _ := os.ReadFile(file)
	json.Unmarshal(b, &state)
	set := func(up bool, only ...string) {
		for s := range state {
			if len(only) == 0 || slices.Contains(only, s) {
				state[s] = up
			}
		}
		writeJSON(file, state)
	}
	switch args[0] {
	case "ps":
		for _, s := range slices.Sorted(func(yield func(string) bool) {
			for s := range state {
				if !yield(s) {
					return
				}
			}
		}) {
			st := "exited"
			if state[s] {
				st = "running"
			}
			fmt.Fprintf(stdout, `{"Service":%q,"State":%q,"Health":""}`+"\n", s, st)
		}
	case "stop":
		set(false)
	case "start":
		set(true)
	case "pull":
	case "up":
		var only []string
		for _, a := range args[1:] {
			if !strings.HasPrefix(a, "-") {
				only = append(only, a)
			}
		}
		set(true, only...)
	case "exec":
		// exec -T <service> <command...>: dump prints <dir>/dump, load writes <dir>/loaded.
		if len(args) < 4 || args[1] != "-T" || !state[args[2]] {
			return 1, fmt.Errorf("fake compose: exec %v into a stopped service", args)
		}
		switch args[3] {
		case "dump":
			b, err := os.ReadFile(filepath.Join(dir, "dump"))
			if err != nil {
				return 1, err
			}
			stdout.Write(b)
		case "load":
			b, _ := io.ReadAll(stdin)
			os.WriteFile(filepath.Join(dir, "loaded"), b, 0o600)
		default:
			return 1, fmt.Errorf("fake compose: %s fails", args[3])
		}
	default:
		return 1, fmt.Errorf("fake compose: unexpected %v", args)
	}
	return 0, nil
}

// restic is docker run of the pinned restic image: it checks the confinement flags, maps the
// container's binds to host paths and acts on <dir>/repo/<repository>.
func restic(dir string, args []string, c *Call, stdout io.Writer) (int, error) {
	mounts := map[string]string{} // container path -> host path
	env := map[string]string{}
	var rest []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch a {
		case "-e":
			c.Env = append(c.Env, args[i+1])
			k, v, _ := strings.Cut(args[i+1], "=")
			env[k] = v
			i++
		case "-v":
			parts := strings.Split(args[i+1], ":")
			mounts[parts[1]] = parts[0]
			i++
		case "--user", "--network", "--tmpfs", "--memory", "--memory-swap", "--cpus", "--pids-limit", "--security-opt", "--cap-drop", "--entrypoint":
			i++
		case "--rm", "--read-only":
		default:
			rest = args[i:]
			i = len(args)
		}
	}
	// IMAGE -c WRAPPER restic ARGS...
	if len(rest) < 4 || rest[1] != "-c" || rest[3] != "restic" {
		return 1, fmt.Errorf("fake restic: unexpected %v", rest)
	}
	if _, err := os.ReadFile(mounts["/run/kyq/rest_password"]); err != nil {
		return 1, fmt.Errorf("fake restic: rest-server password unreadable: %w", err)
	}
	cmd := rest[4:]
	if len(cmd) > 1 && cmd[0] == "--cacert" {
		cmd = cmd[2:]
	}
	password, err := os.ReadFile(mounts[env["RESTIC_PASSWORD_FILE"]])
	if err != nil {
		return 1, fmt.Errorf("fake restic: password unreadable: %w", err)
	}
	repo := filepath.Join(dir, "repo", strings.NewReplacer("/", "_", ":", "_").Replace(env["RESTIC_REPOSITORY"]))
	config := filepath.Join(repo, "config")
	if cmd[0] == "init" {
		if _, err := os.Stat(config); err == nil {
			return 1, fmt.Errorf("fake restic: repository exists")
		}
		os.MkdirAll(filepath.Join(repo, "snapshots"), 0o700)
		return 0, os.WriteFile(config, password, 0o600)
	}
	stored, err := os.ReadFile(config)
	if err != nil {
		return 10, fmt.Errorf("Fatal: repository does not exist")
	}
	if !bytes.Equal(stored, password) {
		return 12, fmt.Errorf("Fatal: wrong password or no key found")
	}
	host := func(p string) string {
		for ctr, h := range mounts {
			if p == ctr || strings.HasPrefix(p, ctr+"/") {
				return h + strings.TrimPrefix(p, ctr)
			}
		}
		return ""
	}
	switch cmd[0] {
	case "cat", "check":
	case "backup":
		s := snapshot{Time: time.Now().UTC()}
		var excludes []string
		for i := 1; i < len(cmd); i++ {
			switch cmd[i] {
			case "--json":
			case "--host":
				s.Host, i = cmd[i+1], i+1
			case "--tag":
				s.Tags, i = append(s.Tags, cmd[i+1]), i+1
			case "--exclude":
				excludes, i = append(excludes, cmd[i+1]), i+1
			default:
				s.Paths = append(s.Paths, cmd[i])
			}
		}
		s.ID = fmt.Sprintf("%x", sha256.Sum256([]byte(time.Now().String())))
		s.Short = s.ID[:8]
		root := filepath.Join(repo, "snapshots", s.ID)
		for _, p := range s.Paths {
			if err := copyTree(host(p), filepath.Join(root, p), func(rel string) bool {
				return slices.ContainsFunc(excludes, func(x string) bool { return x == p+rel || strings.HasPrefix(p+rel, x+"/") })
			}); err != nil {
				return 1, err
			}
		}
		writeJSON(root+".json", s)
		fmt.Fprintf(stdout, `{"message_type":"status"}`+"\n"+`{"message_type":"summary","snapshot_id":%q}`+"\n", s.ID)
	case "snapshots":
		var list []snapshot
		metas, _ := filepath.Glob(filepath.Join(repo, "snapshots", "*.json"))
		for _, m := range metas {
			var s snapshot
			b, _ := os.ReadFile(m)
			json.Unmarshal(b, &s)
			match := true
			for i := 2; i < len(cmd); i++ {
				if cmd[i] == "--tag" {
					for _, t := range strings.Split(cmd[i+1], ",") {
						match = match && slices.Contains(s.Tags, t)
					}
					i++
				} else {
					match = match && strings.HasPrefix(s.ID, cmd[i])
				}
			}
			if match {
				list = append(list, s)
			}
		}
		if list == nil {
			list = []snapshot{}
		}
		b, _ := json.Marshal(list)
		stdout.Write(b)
	case "restore":
		matches, _ := filepath.Glob(filepath.Join(repo, "snapshots", cmd[1]+"*.json"))
		if len(matches) != 1 || len(cmd) != 4 || cmd[2] != "--target" {
			return 1, fmt.Errorf("fake restic: restore %v", cmd)
		}
		return 0, copyTree(strings.TrimSuffix(matches[0], ".json"), host(cmd[3]), func(string) bool { return false })
	default:
		return 1, fmt.Errorf("fake restic: unexpected %v", cmd)
	}
	return 0, nil
}

// copyTree copies src to dst with modes, skipping the paths (relative, /-led) skip reports.
func copyTree(src, dst string, skip func(string) bool) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel := strings.TrimPrefix(p, src)
		if skip(rel) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		target := dst + rel
		if d.IsDir() {
			if err := os.MkdirAll(target, 0o700); err != nil {
				return err
			}
			return os.Chmod(target, info.Mode().Perm())
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, b, info.Mode().Perm())
	})
}
