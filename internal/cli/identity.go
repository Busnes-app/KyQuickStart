package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"path"
	"regexp"
	"strings"
	"unicode"

	"github.com/Busnes-app/kyquickstart/internal/remote"
	"github.com/Busnes-app/kyquickstart/internal/stack"
)

const identityBinary = "/usr/local/bin/kyidentity"

var workloadName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,252}$`)
var identityRoleReportName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}/[A-Za-z0-9][A-Za-z0-9_.:-]{0,63}$`)
var identityGroupReportName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 _.-]{0,63}$`)
var kubeWorkloadName = regexp.MustCompile(`^[a-z0-9]([a-z0-9.-]*[a-z0-9])?$`)

type identityExec func(context.Context, []string) ([]byte, error)

func dockerIdentityExec(r remote.Runner, container string) identityExec {
	return dockerProductExec(r, container, identityBinary)
}

func dockerProductExec(r remote.Runner, container, binary string, runtimeUser ...string) identityExec {
	exec := dockerProductInputExec(r, container, binary, runtimeUser...)
	return func(ctx context.Context, args []string) ([]byte, error) {
		return exec(ctx, args, nil)
	}
}

func dockerProductInputExec(r remote.Runner, container, binary string, runtimeUser ...string) func(context.Context, []string, []byte) ([]byte, error) {
	return func(ctx context.Context, args []string, input []byte) ([]byte, error) {
		if len(input) > 128<<10 {
			return nil, errors.New("product command input exceeds limit")
		}
		words := []string{"docker", "exec"}
		if len(input) > 0 {
			words = append(words, "-i")
		}
		if len(runtimeUser) > 0 && runtimeUser[0] != "" {
			words = append(words, "--user", remote.Quote(runtimeUser[0]))
		}
		words = append(words, remote.Quote(container), remote.Quote(binary))
		for _, arg := range args {
			words = append(words, remote.Quote(arg))
		}
		b, err := r.Run(ctx, strings.Join(words, " "), input)
		if err != nil {
			return nil, errors.New("product setup command failed; inspect product logs locally")
		}
		return b, nil
	}
}

// identitySetup checks the product's advertised contract before invoking the mutation.
func identitySetup(ctx context.Context, exec identityExec, bundle string, check bool) ([]byte, error) {
	if err := identityCapabilities(ctx, exec, false); err != nil {
		return nil, err
	}
	args := []string{"apply-setup", "--file", bundle}
	if check {
		args = append(args, "--check")
	}
	b, err := exec(ctx, args)
	if err != nil {
		return nil, err
	}
	// Render only explicitly permitted fields, never arbitrary container stdout.
	var report struct {
		Schema  string `json:"schema"`
		Created []struct {
			Kind string `json:"kind"`
			Name string `json:"name"`
			ID   string `json:"id"`
		} `json:"created"`
		Reused []struct {
			Kind string `json:"kind"`
			Name string `json:"name"`
			ID   string `json:"id"`
		} `json:"reused"`
	}
	if len(b) > 128*1024 || json.Unmarshal(b, &report) != nil || report.Schema != "kyidentity.setup/1" || report.Created == nil || report.Reused == nil ||
		len(report.Created)+len(report.Reused) > 162 || (check && len(report.Created) > 0) {
		return nil, errors.New("invalid KyIdentity setup report")
	}
	for _, items := range [][]struct {
		Kind string `json:"kind"`
		Name string `json:"name"`
		ID   string `json:"id"`
	}{report.Created, report.Reused} {
		for _, item := range items {
			validName := workloadName.MatchString(item.Name)
			if item.Kind == "role" {
				validName = identityRoleReportName.MatchString(item.Name)
			}
			if item.Kind == "group" {
				validName = identityGroupReportName.MatchString(item.Name) && strings.TrimSpace(item.Name) == item.Name
			}
			if (item.Kind != "user" && item.Kind != "client" && item.Kind != "role" && item.Kind != "connector" && item.Kind != "group") || !validName || !workloadName.MatchString(item.ID) {
				return nil, errors.New("invalid KyIdentity setup object report")
			}
		}
	}
	return json.Marshal(report)
}
func identityCapabilities(ctx context.Context, exec identityExec, prepare bool, extra ...string) error {
	b, err := exec(ctx, []string{"apply-setup", "--capabilities"})
	if err != nil {
		return err
	}
	var caps struct {
		Schema, Service, BundleSchema string
		Features                      []string
	}
	if len(b) > 128*1024 || json.Unmarshal(b, &caps) != nil || caps.Schema != "ky.setup-capabilities/1" || caps.Service != "kyidentity" || caps.BundleSchema != "kyidentity.setup/1" {
		return errors.New("selected product does not advertise the supported KyIdentity setup contract")
	}
	features := []string{"local-accounts", "confidential-oidc-clients", "assigned-only", "everyday-assignment", "app-role-assignments", "scim-connectors", "suite-webhook-connectors", "directory-groups", "app-group-assignments", "scim-groups", "check"}
	features = append(features, extra...)
	if prepare {
		features = append(features, "protected-setup-files", "bundle-preparation", "target-credentials")
	}
	for _, required := range features {
		found := false
		for _, f := range caps.Features {
			found = found || f == required
		}
		if !found {
			return fmt.Errorf("KyIdentity lacks setup capability %s; upgrade the product first", required)
		}
	}
	return nil
}

func integrateIdentity(ctx context.Context, args []string, o Options) error {
	return integrateProduct(ctx, args, o, "kyidentity")
}

func integrateProduct(ctx context.Context, args []string, o Options, fixedService string) error {
	return integrateProductResult(ctx, args, o, fixedService, func(report []byte) error {
		_, err := fmt.Fprintf(o.Out, "%s\n", report)
		return err
	})
}

func integrateProductResult(ctx context.Context, args []string, o Options, fixedService string, emit func([]byte) error) (retErr error) {
	f := flag.NewFlagSet("integrate-product", flag.ContinueOnError)
	f.SetOutput(o.Out)
	service := f.String("product", fixedService, "product adapter name")
	state := f.String("state", ".", "state directory holding stack.yaml")
	target := f.String("target", "", "target name in stack.yaml")
	container := f.String("container", "", "existing KyIdentity container name")
	namespace := f.String("namespace", "", "Kubernetes namespace")
	pod := f.String("pod", "", "existing Kubernetes pod name")
	bundle := f.String("bundle", "", "absolute protected bundle path inside the product container")
	check := f.Bool("check", false, "verify the saved setup without mutation")
	prepare := f.Bool("prepare", false, "prepare the bundle from matching saved secret-free settings")
	trust := trustFlag{}
	f.Var(trust, "trust-host-key", "accept an unknown host key")
	if err := f.Parse(args); err != nil {
		return err
	}
	adapter, ok := productAdapters[*service]
	if !ok || fixedService != "" && *service != fixedService {
		return errors.New("select a supported --product adapter")
	}
	if f.NArg() != 0 || *target == "" || !workloadName.MatchString(*container) || !path.IsAbs(*bundle) || path.Clean(*bundle) != *bundle || len(*bundle) > 4096 || strings.ContainsFunc(*bundle, unicode.IsControl) {
		return errors.New("integrate-identity requires --target, --container and an absolute container-local --bundle path")
	}
	s, err := load(*state, trust, o)
	if err != nil {
		return err
	}
	defer s.close()
	var preparation *stack.NotesPreparation
	var identityPreparation *stack.IdentityPreparation
	var selected, credentialSource stack.Integration
	if *prepare {
		for _, i := range s.integrations {
			if i.Product == *service && i.Target == *target && i.Container == *container && i.Namespace == *namespace && i.Pod == *pod && i.Bundle == *bundle {
				preparation = i.Notes
				identityPreparation = i.Identity
				selected = i
			}
		}
		if (*service != "kynotes" || preparation == nil) && (*service != "kyidentity" || identityPreparation == nil) {
			return errors.New("--prepare requires matching saved product preparation settings")
		}
		if selected.IdentityClient != "" {
			credentialSource, _, err = (&stack.Stack{Integrations: s.integrations}).IdentitySource(selected)
			if err != nil {
				return err
			}
		}
	}
	// Existing references grant no authority; this explicit command selects the mutation target.
	var t stack.Target
	for _, candidate := range s.allTargets {
		if candidate.Name == *target {
			t = candidate
		}
	}
	if t.Name == "" {
		return errors.New("integration target is not in stack.yaml")
	}
	if err := validateProductTarget(*service, t); err != nil {
		return err
	}
	if t.Kubernetes() {
		if !kubeWorkloadName.MatchString(*namespace) || len(*namespace) > 63 || !kubeWorkloadName.MatchString(*pod) || len(*pod) > 253 || !kubeWorkloadName.MatchString(*container) || len(*container) > 63 {
			return errors.New("Kubernetes integration requires valid --namespace, --pod and --container names")
		}
	} else if *namespace != "" || *pod != "" {
		return errors.New("--namespace and --pod require a Kubernetes target")
	}
	s.targets = []stack.Target{t}
	// Product setup has no edge reconciliation authority or need for NPM credentials.
	s.edges = nil
	if credentialSource.Product != "" && credentialSource.Target != t.Name {
		for _, candidate := range s.allTargets {
			if candidate.Name == credentialSource.Target {
				s.targets = append(s.targets, candidate)
			}
		}
	}
	if err := s.connect(ctx, trust); err != nil {
		return err
	}
	exec, inputExec, err := nativeProductExec(s.drivers[t.Name], stack.Integration{Container: *container, Namespace: *namespace, Pod: *pod}, adapter)
	if err != nil {
		return err
	}
	if !*check {
		done, err := s.lock(ctx, "kyquickstart product setup", []stack.Target{t})
		if err != nil {
			return err
		}
		defer func() { retErr = errors.Join(retErr, finish(ctx, done)) }()
	}
	if preparation != nil {
		if credentialSource.Product != "" {
			sourceExec, _, err := nativeProductExec(s.drivers[credentialSource.Target], credentialSource, productAdapters["kyidentity"])
			if err != nil {
				return err
			}
			if err := transferNotesCredentials(ctx, sourceExec, exec, inputExec, credentialSource, selected, *check); err != nil {
				return err
			}
		} else {
			if err := stageNotes(ctx, exec, inputExec, preparation, *bundle, *check); err != nil {
				return err
			}
		}
	}
	if identityPreparation != nil {
		if err := stageIdentity(ctx, exec, inputExec, identityPreparation, *bundle, *check); err != nil {
			return err
		}
	}
	report, err := adapter.setup(ctx, exec, *bundle, *check)
	if err != nil {
		return err
	}
	if !*check {
		if _, err := adapter.setup(ctx, exec, *bundle, true); err != nil {
			return fmt.Errorf("setup verification failed: %w", err)
		}
	}
	return emit(report)
}

func nativeProductExec(target driver, i stack.Integration, adapter productAdapter) (identityExec, func(context.Context, []string, []byte) ([]byte, error), error) {
	switch d := target.(type) {
	case sshDriver:
		return dockerProductExec(d.c, i.Container, adapter.binary, adapter.runtimeUser), dockerProductInputExec(d.c, i.Container, adapter.binary, adapter.runtimeUser), nil
	case kubeDriver:
		return func(ctx context.Context, args []string) ([]byte, error) {
				return d.c.Exec(ctx, i.Namespace, i.Pod, i.Container, append([]string{adapter.binary}, args...))
			},
			func(ctx context.Context, args []string, input []byte) ([]byte, error) {
				return d.c.ExecInput(ctx, i.Namespace, i.Pod, i.Container, append([]string{adapter.binary}, args...), input)
			}, nil
	}
	return nil, nil, errors.New("selected product target is not connected")
}
