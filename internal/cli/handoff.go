package cli

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/Busnes-app/kyquickstart/internal/dockerhost"
	"github.com/Busnes-app/kyquickstart/internal/engine"
	"github.com/Busnes-app/kyquickstart/internal/remote"
	"github.com/Busnes-app/kyquickstart/internal/stack"
	"github.com/Busnes-app/kyquickstart/internal/suite"
	"github.com/google/uuid"
	"go.yaml.in/yaml/v3"
	"golang.org/x/crypto/ssh"
)

const (
	handoffFile = "handoff.json" // present while the runner owns this plan
	runnerState = "/state"       // the runner image's state volume
)

var containerRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)

type handoffRecord struct {
	KyYardTarget string `json:"kyyard_target"`
	Container    string `json:"runner_container"`
}

// runnerOwned refuses a local plan edit once the plan was handed to the runner.
func runnerOwned(state string) error {
	_, err := os.Stat(filepath.Join(state, handoffFile))
	if err == nil {
		return suite.Errorf(suite.OwnedByRunner, "this plan was handed to the KyYard suite runner; change it from KyYard's Suite page, or run kyquickstart handoff --revoke")
	}
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// handoffCommand parses handoff and pull-plan.
func handoffCommand(ctx context.Context, cmd string, args []string, o Options) error {
	fl := flag.NewFlagSet(cmd, flag.ContinueOnError)
	fl.SetOutput(o.Out)
	state := fl.String("state", ".", "state directory holding stack.yaml")
	kyyard := fl.String("kyyard-target", "", "Docker target running KyYard and the runner container")
	container := fl.String("runner-container", "kyquickstart-runner", "the runner container's name")
	revoke := false
	if cmd == "handoff" {
		fl.BoolVar(&revoke, "revoke", false, "take the plan back: remove the runner's access, record the CLI as owner")
	}
	trust := trustFlag{}
	fl.Var(trust, "trust-host-key", "accept an unknown host key: <target>=SHA256:<fingerprint>")
	if err := fl.Parse(args); err != nil {
		return err
	}
	if fl.NArg() > 0 || !containerRE.MatchString(*container) || *kyyard == "" && !revoke {
		return ErrUsage
	}
	s, err := load(*state, trust, o)
	if err != nil {
		return err
	}
	defer s.close()
	r := runnerHost{target: *kyyard, container: *container}
	if *kyyard != "" {
		t, ok := s.target(*kyyard)
		if !ok {
			return suite.Errorf(suite.UnknownTarget, "--kyyard-target %q is not in stack.yaml", *kyyard)
		}
		if t.Kubernetes() {
			return suite.Errorf(suite.InvalidRequest, "--kyyard-target %s must be a Docker target reached over SSH", *kyyard)
		}
	}
	if cmd == "pull-plan" {
		return s.pullPlan(ctx, r)
	}
	return s.handoff(ctx, r, revoke)
}

func (s *session) target(name string) (stack.Target, bool) {
	for _, t := range s.allTargets {
		if t.Name == name {
			return t, true
		}
	}
	return stack.Target{}, false
}

// runnerHost is the runner container on KyYard's Docker target.
type runnerHost struct{ target, container string }

// exec runs a serve-* command in the runner container over the target's SSH connection.
func (s *session) exec(ctx context.Context, r runnerHost, cmd string, stdin []byte) ([]byte, error) {
	out, err := s.drivers[r.target].(sshDriver).c.Run(ctx, fmt.Sprintf("docker exec -i %s kyquickstart %s --state %s", remote.Quote(r.container), cmd, runnerState), stdin)
	if err != nil {
		return nil, fmt.Errorf("%s in container %s on %s: %w", cmd, r.container, r.target, err)
	}
	return out, nil
}

// runnerKey turns serve-keygen's output into a restricted authorized_keys line.
func runnerKey(out []byte) (line, id string, err error) {
	pub, comment, _, rest, err := ssh.ParseAuthorizedKey(out)
	if err == nil && (pub.Type() != ssh.KeyAlgoED25519 || len(bytes.TrimSpace(rest)) > 0) {
		err = errors.New("not one ed25519 key")
	}
	if err != nil {
		return "", "", fmt.Errorf("serve-keygen: %w", err)
	}
	id, ok := strings.CutPrefix(comment, dockerhost.RunnerKeyComment)
	if u, err := uuid.Parse(id); !ok || err != nil || u.String() != id {
		return "", "", errors.New("serve-keygen: key comment holds no runner ID")
	}
	return "restrict " + strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pub))) + " " + comment, id, nil
}

// handoff gives the runner access to every target and the plan, then records it as the
// plan's owner; revoke reverses it. Every step converges, so a rerun finishes an
// interrupted one.
func (s *session) handoff(ctx context.Context, r runnerHost, revoke bool) error {
	s.targets, s.edges = s.allTargets, nil
	if err := s.connect(ctx, s.trust); err != nil {
		return err
	}
	owner := stack.Marker{Owner: "cli"}
	var line string
	if !revoke {
		out, err := s.exec(ctx, r, "serve-keygen", nil)
		if err != nil {
			return err
		}
		if line, owner.RunnerID, err = runnerKey(out); err != nil {
			return err
		}
		owner.Owner = "runner"
	}
	_, data, err := changePlan(s.state, s.revision, func(*stack.Stack) {})
	if err != nil {
		return err
	}
	var summary []string
	return s.operate(ctx, "", func(ctx context.Context) ([]engine.Step, error) {
		kubeconfigs := map[string][]byte{}
		for _, t := range s.targets {
			var err error
			switch d := s.drivers[t.Name].(type) {
			case sshDriver:
				err = dockerhost.AuthorizeRunner(ctx, d.c, line)
				summary = append(summary, fmt.Sprintf("%s: authorized_keys of %s", t.Name, t.User))
			case kubeDriver:
				if revoke {
					err = d.c.RevokeRunner(ctx)
				} else {
					kubeconfigs[t.Name], err = d.c.GrantRunner(ctx, owner.RunnerID)
				}
				summary = append(summary, t.Name+": service account kyquickstart-runner")
			}
			if err != nil {
				return nil, fmt.Errorf("%s: %w", t.Name, err)
			}
		}
		if revoke {
			return nil, nil
		}
		b, err := planTar(s.state, data, kubeconfigs)
		if err == nil {
			_, err = s.exec(ctx, r, "serve-import", b)
		}
		return nil, err
	}, func() error {
		file := filepath.Join(s.state, handoffFile)
		if !revoke {
			b, _ := json.Marshal(handoffRecord{r.target, r.container})
			if err := atomicFile(file, b); err != nil {
				return err
			}
		}
		if err := publish(s.state, s.revision, data); err != nil {
			return err
		}
		s.owner = &owner
		if revoke {
			if err := os.Remove(file); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
			fmt.Fprintf(s.o.Out, "Took the plan back (revision %d); the runner's access is removed from:\n  %s\n", s.revision+1, strings.Join(summary, "\n  "))
			return nil
		}
		fmt.Fprintf(s.o.Out, "Handed the plan (revision %d) to runner %s in %s on %s, with access to:\n  %s\nManage apps from KyYard's Suite page. Run kyquickstart pull-plan before any CLI command.\n",
			s.revision+1, owner.RunnerID, r.container, r.target, strings.Join(summary, "\n  "))
		return nil
	})
}

// planTar packs what serve-import accepts: the plan, pinned host keys, step results and the
// runner's kubeconfigs.
func planTar(state string, plan []byte, kubeconfigs map[string][]byte) ([]byte, error) {
	files := map[string][]byte{"stack.yaml": plan}
	for t, kc := range kubeconfigs {
		files["kubeconfigs/"+t+".yaml"] = kc
	}
	names, _ := filepath.Glob(filepath.Join(state, "results", "*.json"))
	names = append(names, filepath.Join(state, "known_hosts"))
	for _, n := range names {
		b, err := os.ReadFile(n)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		rel, _ := filepath.Rel(state, n)
		files[filepath.ToSlash(rel)] = b
	}
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for name, b := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(b)), Typeflag: tar.TypeReg}); err != nil {
			return nil, err
		}
		if _, err := tw.Write(b); err != nil {
			return nil, err
		}
	}
	return buf.Bytes(), tw.Close()
}

// pullPlan replaces the local plan with the runner's when it is at least as new, keeping
// the previous one as stack.yaml.bak. Kubeconfig paths stay this workstation's.
func (s *session) pullPlan(ctx context.Context, r runnerHost) error {
	t, _ := s.target(r.target)
	s.targets, s.edges = []stack.Target{t}, nil
	if err := s.connect(ctx, s.trust); err != nil {
		return err
	}
	out, err := s.exec(ctx, r, "serve-export", nil)
	if err != nil {
		return err
	}
	st, err := stack.Parse(out)
	if err != nil {
		return fmt.Errorf("the runner's plan: %w", err)
	}
	if st.Revision < s.revision {
		return suite.Errorf(suite.PlanStale, "the runner holds plan revision %d, older than this plan's %d; nothing was pulled", st.Revision, s.revision)
	}
	for i, t := range st.Targets {
		if !t.Kubernetes() {
			continue
		}
		local, ok := s.target(t.Name)
		if !ok || !local.Kubernetes() {
			return fmt.Errorf("the runner's plan has Kubernetes target %s, which this plan lacks", t.Name)
		}
		st.Targets[i].Kubeconfig, st.Targets[i].Context = local.Kubeconfig, local.Context
	}
	data, err := yaml.Marshal(st)
	if err != nil {
		return err
	}
	file := filepath.Join(s.state, "stack.yaml")
	old, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	if err := atomicFile(file+".bak", old); err != nil {
		return err
	}
	if err := publish(s.state, s.revision, data); err != nil {
		return err
	}
	b, _ := json.Marshal(handoffRecord{r.target, r.container})
	if err := atomicFile(filepath.Join(s.state, handoffFile), b); err != nil {
		return err
	}
	fmt.Fprintf(s.o.Out, "Pulled plan revision %d from the runner on %s (previous plan in stack.yaml.bak).\n", st.Revision, r.target)
	return nil
}
