package cli

import (
	"context"
	"errors"

	"github.com/Busnes-app/kyquickstart/internal/catalog"
	"github.com/Busnes-app/kyquickstart/internal/dockerhost"
	"github.com/Busnes-app/kyquickstart/internal/edge"
	"github.com/Busnes-app/kyquickstart/internal/engine"
	"github.com/Busnes-app/kyquickstart/internal/kube"
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
	steps(name string, app catalog.App, releaseSet string, redact *engine.Redactor) []engine.Step
	edgeToken(context.Context) ([]byte, error)
	checkConnector(context.Context, stack.Edge) error
	connectorSteps(context.Context, stack.Edge, string, *engine.Redactor) ([]engine.Step, error)
	close() error
}

type sshDriver struct {
	t stack.Target
	c *remote.SSH
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

func (d sshDriver) steps(name string, app catalog.App, releaseSet string, redact *engine.Redactor) []engine.Step {
	return dockerhost.Steps(dockerhost.App{Name: name, Root: d.t.Root, Runner: d.c, Catalog: app, ReleaseSet: releaseSet, Redact: redact})
}

func (d sshDriver) close() error { return d.c.Close() }

func (d sshDriver) edgeToken(ctx context.Context) ([]byte, error) {
	return remote.ReadSecret(ctx, d.c, d.t.Root+"/edge/secrets/npm_token")
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
}

func (d kubeDriver) preflight(ctx context.Context) []finding {
	var out []finding
	for _, f := range kube.Preflight(ctx, d.c, d.needsStorage) {
		out = append(out, finding(f))
	}
	return out
}

func (d kubeDriver) acquire(ctx context.Context, holder string) (func(context.Context) error, error) {
	return kube.Acquire(ctx, d.c, holder)
}

func (d kubeDriver) steps(name string, app catalog.App, releaseSet string, redact *engine.Redactor) []engine.Step {
	return kube.Steps(kube.App{Name: name, Client: d.c, Catalog: app, ReleaseSet: releaseSet, Redact: redact})
}

func (d kubeDriver) close() error { return nil }

func (d kubeDriver) edgeToken(ctx context.Context) ([]byte, error) { return d.c.EdgeToken(ctx) }

func (d kubeDriver) checkConnector(context.Context, stack.Edge) error {
	return errors.New("Kubernetes connector deployment is not implemented")
}

func (d kubeDriver) connectorSteps(context.Context, stack.Edge, string, *engine.Redactor) ([]engine.Step, error) {
	return nil, errors.New("Kubernetes connector deployment is not implemented")
}
