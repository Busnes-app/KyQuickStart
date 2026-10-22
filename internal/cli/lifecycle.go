package cli

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/Busnes-app/kyquickstart/internal/engine"
	"github.com/Busnes-app/kyquickstart/internal/stack"
	"github.com/Busnes-app/kyquickstart/internal/suite"
)

// stopped reports whether kyquickstart stop stopped the plan's app name.
func (s *session) stopped(name string) bool {
	return slices.ContainsFunc(s.plan.Apps, func(a stack.App) bool { return a.Name == name && a.Stopped })
}

// refuseStopped refuses to deploy s.order while it holds an app kyquickstart stop stopped:
// deploying would start it again.
func (s *session) refuseStopped() error {
	for _, name := range s.order {
		if s.stopped(name) {
			return suite.Errorf(suite.AppStopped, "%s was stopped with kyquickstart stop, and deploying would start it again; run kyquickstart start %s first", name, name)
		}
	}
	return nil
}

// lifecycle stops, starts or restarts one installed app under its target's lock. Stop and
// start record the intent in the plan, published once the app is in that state; on an app
// already there they change nothing on the target.
func lifecycle(ctx context.Context, state, op string, r Request, trust trustFlag, o Options) error {
	name, err := oneApp(r)
	if err != nil {
		return err
	}
	s, err := load(state, trust, o)
	if err != nil {
		return err
	}
	defer s.close()
	t, err := s.installed(name)
	if err != nil {
		return err
	}
	if err := s.refuseOwnPod(r.Apps); err != nil {
		return err
	}
	if op == "restart" && s.stopped(name) {
		return suite.Errorf(suite.AppStopped, "%s was stopped with kyquickstart stop; run kyquickstart start %s instead", name, name)
	}
	var publishPlan func() error
	if op == "restart" {
		s.deploys = true // as upgrade: refused while the runner in this app's pod owns the plan
	} else if stop := op == "stop"; s.stopped(name) != stop {
		next, data, err := changePlan(state, s.revision, func(st *stack.Stack) {
			st.Apps[slices.IndexFunc(st.Apps, func(a stack.App) bool { return a.Name == name })].Stopped = stop
		})
		if err != nil {
			return err
		}
		s.installation = next.InstallationID
		publishPlan = func() error { return publish(state, s.revision, data) }
	}
	s.changesPlan = op != "restart" // stop and start belong to the plan's owner
	s.order, s.targets, s.edges = []string{name}, []stack.Target{t}, nil
	if err := s.connect(ctx, trust); err != nil {
		return err
	}
	d := s.drivers[t.Name]
	return s.operate(ctx, r.Holder, func(ctx context.Context) ([]engine.Step, error) {
		if err := s.managed(ctx, d, name, t); err != nil {
			return nil, err
		}
		return []engine.Step{d.lifecycleStep(name, s.cat[name], op, time.Now().UTC().Format(time.RFC3339Nano))}, nil
	}, publishPlan)
}

// managed refuses an app its target does not hold as deployed by kyquickstart.
func (s *session) managed(ctx context.Context, d driver, name string, t stack.Target) error {
	dep, err := d.deployed(ctx, name)
	if err == nil && (!dep.Found || !dep.Managed) {
		err = suite.Errorf(suite.NotInstalled, "%s is not deployed by kyquickstart on %s", name, t.Name)
	}
	return err
}

// logs prints an installed app's recent log lines, with the secrets the installer generated
// for it redacted. It takes no lock and changes nothing.
func logs(ctx context.Context, state string, r Request, service string, tail int, trust trustFlag, o Options) error {
	name, err := oneApp(r)
	if err != nil {
		return err
	}
	if tail < 1 || tail > 10000 {
		return suite.Errorf(suite.InvalidRequest, "--tail takes 1 to 10000 lines")
	}
	s, err := load(state, trust, o)
	if err != nil {
		return err
	}
	defer s.close()
	t, err := s.installed(name)
	if err != nil {
		return err
	}
	if service != "" && !t.Kubernetes() && !slices.Contains(s.cat[name].Services, service) {
		return suite.Errorf(suite.InvalidRequest, "%s has no service %q; its services are %s", name, service, strings.Join(s.cat[name].Services, ", "))
	}
	s.targets, s.edges = []stack.Target{t}, nil
	if err := s.connect(ctx, trust); err != nil {
		return err
	}
	d := s.drivers[t.Name]
	m, err := d.marker(ctx)
	if err != nil {
		return fmt.Errorf("%s: %w", t.Name, err)
	}
	if err := s.guard(t.Name, m); err != nil {
		return err
	}
	if err := s.managed(ctx, d, name, t); err != nil {
		return err
	}
	for _, secret := range s.cat[name].Secrets {
		v, err := d.appSecret(ctx, name, secret)
		if err != nil {
			return fmt.Errorf("read %s's secret %s to redact its logs: %w", name, secret, err)
		}
		s.redact.Add(string(v))
		s.redact.Add(strings.TrimSpace(string(v)))
	}
	out, err := d.logs(ctx, name, service, tail)
	if err != nil {
		return fmt.Errorf("%s: %s", name, s.redact.Redact(err.Error()))
	}
	_, err = fmt.Fprint(o.Out, s.redact.Redact(string(out)))
	return err
}
