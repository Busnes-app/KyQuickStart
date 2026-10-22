package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"path"

	"github.com/Busnes-app/kyquickstart/internal/catalog"
	"github.com/Busnes-app/kyquickstart/internal/dockerhost"
	"github.com/Busnes-app/kyquickstart/internal/edge"
	"github.com/Busnes-app/kyquickstart/internal/engine"
	"github.com/Busnes-app/kyquickstart/internal/kube"
	"github.com/Busnes-app/kyquickstart/internal/managed"
	"github.com/Busnes-app/kyquickstart/internal/remote"
	"github.com/Busnes-app/kyquickstart/internal/stack"
)

// finding has the fields of dockerhost.Finding and kube.Finding, so both convert to it.
type finding struct {
	Check  string
	OK     bool
	Detail string
}

// driver is what apply and preflight need from one target.
type driver interface {
	preflight(ctx context.Context) []finding
	acquire(ctx context.Context, holder string) (func(context.Context) error, error)
	marker(context.Context) (stack.Marker, error)
	writeMarker(context.Context, stack.Marker) error
	steps(name string, app catalog.App, releaseSet string, redact *engine.Redactor) []engine.Step
	deployed(ctx context.Context, name string) (managed.Deployed, error)
	removeSteps(name string, app catalog.App) []engine.Step
	unmanageSteps(name string, app catalog.App) []engine.Step
	lifecycleStep(name string, app catalog.App, op, run string) engine.Step // stop, start or restart
	workload(ctx context.Context, name string, app catalog.App) (string, error)
	logs(ctx context.Context, name, service string, tail int) ([]byte, error)
	edgeAccess(context.Context, stack.Edge) ([]byte, edge.Dial, error) // NPM token, and tunnel for kube://
	edgeSecrets() edge.Secrets                                         // referenced edge credentials; nil on Docker
	appSecret(ctx context.Context, app, name string) ([]byte, error)   // a secret the target generated for app
	checkConnector(context.Context, stack.Edge) error
	connectorSteps(context.Context, stack.Edge, string, *engine.Redactor) ([]engine.Step, error)
	execRunner(ctx context.Context, r handoffRecord, serve string, stdin io.Reader) ([]byte, error)
	close() error
}

type sshDriver struct {
	t stack.Target
	c interface {
		remote.Runner
		Close() error
	}
}

func (d sshDriver) preflight(ctx context.Context) []finding {
	var out []finding
	for _, f := range dockerhost.Preflight(ctx, d.c, d.t) {
		out = append(out, finding(f))
	}
	return out
}

func (d sshDriver) acquire(ctx context.Context, holder string) (func(context.Context) error, error) {
	return dockerhost.Acquire(ctx, d.c, d.t.Root, holder)
}

func (d sshDriver) marker(ctx context.Context) (stack.Marker, error) {
	return dockerhost.ReadMarker(ctx, d.c, d.t.Root)
}

func (d sshDriver) writeMarker(ctx context.Context, m stack.Marker) error {
	return dockerhost.WriteMarker(ctx, d.c, d.t.Root, m)
}

func (d sshDriver) steps(name string, app catalog.App, releaseSet string, redact *engine.Redactor) []engine.Step {
	return dockerhost.Steps(dockerhost.App{Name: name, Root: d.t.Root, Runner: d.c, Catalog: app, ReleaseSet: releaseSet, Redact: redact})
}

func (d sshDriver) app(name string, app catalog.App, releaseSet string) dockerhost.App {
	return dockerhost.App{Name: name, Root: d.t.Root, Runner: d.c, Catalog: app, ReleaseSet: releaseSet}
}

func (d sshDriver) deployed(ctx context.Context, name string) (managed.Deployed, error) {
	return dockerhost.Deployed(ctx, d.app(name, catalog.App{}, ""))
}

func (d sshDriver) removeSteps(name string, app catalog.App) []engine.Step {
	return dockerhost.RemoveSteps(d.app(name, app, ""))
}

func (d sshDriver) unmanageSteps(name string, app catalog.App) []engine.Step {
	return dockerhost.UnmanageSteps(d.app(name, app, ""))
}

func (d sshDriver) lifecycleStep(name string, app catalog.App, op, run string) engine.Step {
	a := d.app(name, app, "")
	switch op {
	case "stop":
		return dockerhost.StopStep(a)
	case "start":
		return dockerhost.StartStep(a)
	}
	return dockerhost.RestartStep(a, run)
}

func (d sshDriver) workload(ctx context.Context, name string, app catalog.App) (string, error) {
	return dockerhost.State(ctx, d.app(name, app, ""))
}

func (d sshDriver) logs(ctx context.Context, name, service string, tail int) ([]byte, error) {
	return dockerhost.Logs(ctx, d.app(name, catalog.App{}, ""), service, tail)
}

func (d sshDriver) close() error { return d.c.Close() }

// execRunner runs kyquickstart serve-* in the runner container with docker exec.
func (d sshDriver) execRunner(ctx context.Context, r handoffRecord, serve string, stdin io.Reader) ([]byte, error) {
	var in []byte
	if stdin != nil {
		var err error
		if in, err = io.ReadAll(stdin); err != nil {
			return nil, err
		}
	}
	return d.c.Run(ctx, "docker exec -i "+remote.Quote(r.Container)+" kyquickstart "+remote.Quote(serve)+" --state "+runnerState, in)
}

// edgeSecrets is nil: stack validation keeps Secret references to Kubernetes edges.
func (d sshDriver) edgeSecrets() edge.Secrets { return nil }

func (d sshDriver) appSecret(ctx context.Context, app, name string) ([]byte, error) {
	return remote.ReadSecret(ctx, d.c, path.Join(d.t.Root, app, "secrets", name))
}

func (d sshDriver) edgeAccess(ctx context.Context, _ stack.Edge) ([]byte, edge.Dial, error) {
	token, err := remote.ReadSecret(ctx, d.c, d.t.Root+"/edge/secrets/npm_token")
	return token, nil, err
}

func (d sshDriver) checkConnector(ctx context.Context, e stack.Edge) error {
	return edge.CheckConnector(ctx, d.c, d.t.Root, e)
}

func (d sshDriver) connectorSteps(ctx context.Context, e stack.Edge, release string, redact *engine.Redactor) ([]engine.Step, error) {
	return edge.DockerConnector(ctx, d.c, d.t.Root, release, e, redact)
}

type kubeDriver struct {
	c            *kube.Client
	needsStorage bool
	storageClass string
	edge         string           // the edge app on this cluster, admitted to the other apps' ports
	nodePorts    map[int]string   // NodePort -> the app here publishing it
	upstreams    []netip.AddrPort // where the edge's routes forward off the cluster
	runnerImage  string           // the suite runner beside KyYard
}

func (d kubeDriver) preflight(ctx context.Context) []finding {
	var out []finding
	for _, f := range kube.Preflight(ctx, d.c, d.needsStorage, d.storageClass, d.nodePorts) {
		out = append(out, finding(f))
	}
	return out
}

func (d kubeDriver) acquire(ctx context.Context, holder string) (func(context.Context) error, error) {
	return kube.Acquire(ctx, d.c, holder)
}

func (d kubeDriver) marker(ctx context.Context) (stack.Marker, error) { return d.c.Marker(ctx) }

func (d kubeDriver) writeMarker(ctx context.Context, m stack.Marker) error {
	return d.c.WriteMarker(ctx, m)
}

func (d kubeDriver) steps(name string, app catalog.App, releaseSet string, redact *engine.Redactor) []engine.Step {
	a := kube.App{Name: name, Client: d.c, Catalog: app, ReleaseSet: releaseSet, Redact: redact, StorageClass: d.storageClass, Edge: d.edge, RunnerImage: d.runnerImage}
	if name == d.edge {
		a.Upstreams = d.upstreams
	}
	return kube.Steps(a)
}

func (d kubeDriver) deployed(ctx context.Context, name string) (managed.Deployed, error) {
	return d.c.Deployed(ctx, name)
}

func (d kubeDriver) removeSteps(name string, app catalog.App) []engine.Step {
	return kube.RemoveSteps(kube.App{Name: name, Client: d.c, Catalog: app})
}

func (d kubeDriver) unmanageSteps(name string, app catalog.App) []engine.Step {
	return kube.UnmanageSteps(kube.App{Name: name, Client: d.c, Catalog: app})
}

func (d kubeDriver) lifecycleStep(name string, app catalog.App, op, run string) engine.Step {
	a := kube.App{Name: name, Client: d.c, Catalog: app}
	switch op {
	case "stop":
		return kube.StopStep(a)
	case "start":
		return kube.StartStep(a)
	}
	return kube.RestartStep(a, run)
}

func (d kubeDriver) workload(ctx context.Context, name string, _ catalog.App) (string, error) {
	return d.c.State(ctx, name)
}

func (d kubeDriver) logs(ctx context.Context, name, service string, tail int) ([]byte, error) {
	return d.c.Logs(ctx, name, service, int64(tail))
}

func (d kubeDriver) close() error { return nil }

// execRunner runs kyquickstart serve-* in the runner pod over pods/exec.
func (d kubeDriver) execRunner(ctx context.Context, r handoffRecord, serve string, stdin io.Reader) ([]byte, error) {
	return d.c.ExecRunner(ctx, r.Namespace, r.Selector, []string{"kyquickstart", serve, "--state", podRunnerState, "--pod-volumes"}, stdin)
}

func (d kubeDriver) edgeSecrets() edge.Secrets { return d.c }

func (d kubeDriver) appSecret(ctx context.Context, app, name string) ([]byte, error) {
	return d.c.AppSecret(ctx, app, name)
}

func (d kubeDriver) edgeAccess(ctx context.Context, e stack.Edge) ([]byte, edge.Dial, error) {
	ns, svc, port, ok := stack.KubeService(e.NPMURL)
	if !ok {
		token, err := d.c.EdgeToken(ctx)
		return token, nil, err
	}
	token, dial, err := d.c.NPMAccess(ctx, ns, svc, port)
	if err != nil {
		return nil, nil, fmt.Errorf("in-cluster NPM is not reachable or not bootstrapped (%w); apply it once without this edge, then add its certificate", err)
	}
	return token, dial, nil
}

func (d kubeDriver) checkConnector(context.Context, stack.Edge) error {
	return errors.New("Kubernetes connector deployment is not implemented")
}

func (d kubeDriver) connectorSteps(context.Context, stack.Edge, string, *engine.Redactor) ([]engine.Step, error) {
	return nil, errors.New("Kubernetes connector deployment is not implemented")
}
