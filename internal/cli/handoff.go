package cli

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/Busnes-app/kyquickstart/internal/dockerhost"
	"github.com/Busnes-app/kyquickstart/internal/engine"
	"github.com/Busnes-app/kyquickstart/internal/kube"
	"github.com/Busnes-app/kyquickstart/internal/stack"
	"github.com/Busnes-app/kyquickstart/internal/suite"
	"github.com/google/uuid"
	"go.yaml.in/yaml/v3"
	"golang.org/x/crypto/ssh"
	"k8s.io/client-go/tools/clientcmd"
)

const (
	handoffFile = "handoff.json" // present while the runner owns this plan
	runnerState = "/state"       // the runner image's state volume
	// In a pod the state is a subdirectory of the volume (serve --pod-volumes).
	podRunnerState = "/state/runner"
)

var containerRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)

// handoffRecord is where the runner is: a container on KyYard's Docker target, or the pod a
// label selector picks on KyYard's Kubernetes target.
type handoffRecord struct {
	KyYardTarget string `json:"kyyard_target"`
	Container    string `json:"runner_container,omitempty"`
	Namespace    string `json:"runner_namespace,omitempty"`
	Selector     string `json:"runner_selector,omitempty"`
}

func (r handoffRecord) String() string {
	if r.Namespace != "" {
		return fmt.Sprintf("pod %s/%s on %s", r.Namespace, r.Selector, r.KyYardTarget)
	}
	return fmt.Sprintf("container %s on %s", r.Container, r.KyYardTarget)
}

func readRecord(state string) (handoffRecord, error) {
	var r handoffRecord
	b, err := os.ReadFile(filepath.Join(state, handoffFile))
	if errors.Is(err, os.ErrNotExist) {
		return r, nil
	}
	if err == nil {
		err = json.Unmarshal(b, &r)
	}
	return r, err
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

// handoffCommand parses handoff and pull-plan. Without --kyyard-target, pull-plan and
// --revoke use the runner recorded in handoff.json.
func handoffCommand(ctx context.Context, cmd string, args []string, o Options) error {
	fl := flag.NewFlagSet(cmd, flag.ContinueOnError)
	fl.SetOutput(o.Out)
	state := fl.String("state", ".", "state directory holding stack.yaml")
	kyyard := fl.String("kyyard-target", "", "target running KyYard and the runner")
	container := fl.String("runner-container", "kyquickstart-runner", "Docker target: the runner container's name")
	pod := fl.String("runner-pod", "kyq-runner/app=runner", "Kubernetes target: the runner pod, as NAMESPACE/LABEL_SELECTOR")
	revoke := false
	if cmd == "handoff" {
		fl.BoolVar(&revoke, "revoke", false, "take the plan back: remove the runner's access, record the CLI as owner")
	}
	trust := trustFlag{}
	fl.Var(trust, "trust-host-key", "accept an unknown host key: <target>=SHA256:<fingerprint>")
	if err := fl.Parse(args); err != nil {
		return err
	}
	set := map[string]bool{}
	fl.Visit(func(f *flag.Flag) { set[f.Name] = true })
	named := set["runner-container"] || set["runner-pod"]
	if fl.NArg() > 0 || set["runner-container"] && set["runner-pod"] || *kyyard == "" && (named || cmd == "handoff" && !revoke) {
		return ErrUsage
	}
	s, err := load(*state, trust, o)
	if err != nil {
		return err
	}
	defer s.close()
	r, err := readRecord(s.state)
	if err != nil {
		return err
	}
	if *kyyard != "" {
		r = handoffRecord{KyYardTarget: *kyyard, Container: *container}
		if t, ok := s.target(*kyyard); ok && t.Kubernetes() {
			if set["runner-container"] {
				return suite.Errorf(suite.InvalidRequest, "--kyyard-target %s is a Kubernetes target: name its runner with --runner-pod", *kyyard)
			}
			r.Container = ""
			r.Namespace, r.Selector, _ = strings.Cut(*pod, "/")
		} else if set["runner-pod"] {
			return suite.Errorf(suite.InvalidRequest, "--kyyard-target %s is not a Kubernetes target: name its runner with --runner-container", *kyyard)
		}
	}
	if revoke {
		return s.handoff(ctx, r, true)
	}
	if err := s.checkRunner(r); err != nil {
		return err
	}
	if cmd == "pull-plan" {
		return s.pullPlan(ctx, r)
	}
	return s.handoff(ctx, r, false)
}

// checkRunner refuses a runner location that does not fit its KyYard target.
func (s *session) checkRunner(r handoffRecord) error {
	t, ok := s.target(r.KyYardTarget)
	switch {
	case r.KyYardTarget == "":
		return suite.Errorf(suite.InvalidRequest, "no runner was handed this plan; name it with --kyyard-target")
	case !ok:
		return suite.Errorf(suite.UnknownTarget, "--kyyard-target %q is not in stack.yaml", r.KyYardTarget)
	case t.Kubernetes():
		if _, _, err := kube.ParseRunnerPod(r.Namespace + "/" + r.Selector); err != nil {
			return suite.Errorf(suite.InvalidRequest, "%v", err)
		}
	case !containerRE.MatchString(r.Container):
		return suite.Errorf(suite.InvalidRequest, "--runner-container %q is not a container name", r.Container)
	}
	return nil
}

func (s *session) target(name string) (stack.Target, bool) {
	for _, t := range s.allTargets {
		if t.Name == name {
			return t, true
		}
	}
	return stack.Target{}, false
}

// exec runs a serve-* command in the runner through KyYard's target.
func (s *session) exec(ctx context.Context, r handoffRecord, cmd string, stdin []byte) ([]byte, error) {
	var in io.Reader
	if stdin != nil {
		in = bytes.NewReader(stdin)
	}
	out, err := s.drivers[r.KyYardTarget].execRunner(ctx, r, cmd, in)
	if err != nil {
		return nil, fmt.Errorf("%s in the runner %s: %s", cmd, r, s.redact.Redact(err.Error()))
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
func (s *session) handoff(ctx context.Context, r handoffRecord, revoke bool) error {
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
					// One target per cluster: a runner on this target is in this cluster.
					kubeconfigs[t.Name], err = d.c.GrantRunner(ctx, owner.RunnerID, t.Name == r.KyYardTarget)
					s.redactTokens(kubeconfigs[t.Name])
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
			b, _ := json.Marshal(r)
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
			if r.KyYardTarget != "" {
				fmt.Fprintf(s.o.Out, "The runner in %s keeps its copy of the plan, which reaches no target.\n", r)
			}
			return nil
		}
		fmt.Fprintf(s.o.Out, "Handed the plan (revision %d) to runner %s in %s, with access to:\n  %s\nManage apps from KyYard's Suite page. Run kyquickstart pull-plan before any CLI command.\n",
			s.revision+1, owner.RunnerID, r, strings.Join(summary, "\n  "))
		return nil
	})
}

// redactTokens keeps a runner kubeconfig's token out of every message.
func (s *session) redactTokens(kubeconfig []byte) {
	if cfg, err := clientcmd.Load(kubeconfig); err == nil {
		for _, a := range cfg.AuthInfos {
			s.redact.Add(a.Token)
		}
	}
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
func (s *session) pullPlan(ctx context.Context, r handoffRecord) error {
	t, _ := s.target(r.KyYardTarget)
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
	b, _ := json.Marshal(r)
	if err := atomicFile(filepath.Join(s.state, handoffFile), b); err != nil {
		return err
	}
	fmt.Fprintf(s.o.Out, "Pulled plan revision %d from the runner in %s (previous plan in stack.yaml.bak).\n", st.Revision, r)
	return nil
}
