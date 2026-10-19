package cli

import (
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/Busnes-app/kyquickstart/internal/catalog"
	"github.com/Busnes-app/kyquickstart/internal/remote"
	"github.com/Busnes-app/kyquickstart/internal/stack"
	"github.com/Busnes-app/kyquickstart/internal/suite"
)

// yardUsername is the rule KyYard's setup applies to the accounts it creates.
var yardUsername = regexp.MustCompile(`^[a-z0-9._-]{3,64}$`)

// yardRunnerPod is handoff's --runner-pod for the runner beside the catalog KyYard.
const yardRunnerPod = "kyq-kyyard/app.kubernetes.io/name=kyyard"

// runnerSelfNote replaces the workload steps of the app the runner runs in.
const runnerSelfNote = "not redeployed: the suite runner runs in this pod; change it from the workstation with handoff --revoke, apply, then handoff"

func hostsRunner(a catalog.App) bool { return a.Kubernetes != nil && a.Kubernetes.SuiteRunner }

// yardPlan refuses a plan installing KyYard that its setup or its runner could not take:
// KyYard off Kubernetes, or an owner admin username KyYard's setup would refuse.
func yardPlan(st *stack.Stack) error {
	i := slices.IndexFunc(st.Apps, func(a stack.App) bool { return a.Name == "kyyard" })
	if i < 0 {
		return nil
	}
	if t, _ := st.Target(st.Apps[i].Target); !t.Kubernetes() {
		return fmt.Errorf("KyYard runs on Kubernetes only, with the suite runner in its pod; place kyyard on a Kubernetes target, not %s", t.Name)
	}
	for _, i := range st.Integrations {
		if i.Product != "kyidentity" || i.Identity == nil || !i.Installed(st) {
			continue
		}
		if u := i.Identity.Admin.Username; strings.EqualFold(u, "admin") || !yardUsername.MatchString(u) {
			return fmt.Errorf(`KyYard cannot take the KyIdentity admin username %q: its usernames are 3 to 64 lowercase letters, digits, ".", "_" or "-", and "admin" is its own break-glass account. Change identity.admin.username in the kyidentity integration`, u)
		}
	}
	return nil
}

// runnerImage refuses deploying an app that runs the suite runner from a workstation build
// without a digest-pinned runner image. The runner itself never deploys that app.
func (s *session) runnerImage() error {
	return checkRunnerImage(slices.ContainsFunc(s.order, func(a string) bool { return hostsRunner(s.cat[a]) }), s.o)
}

func checkRunnerImage(deploys bool, o Options) error {
	switch {
	case !deploys || o.RunnerID != "":
		return nil
	case o.RunnerImage == "":
		return fmt.Errorf("KyYard's suite runner has no attested release image in this build, so it cannot install KyYard; remove kyyard from the plan or use a kyquickstart release that pins one (docs/guided-setup.md, Catalog KyYard on Kubernetes)")
	case !catalog.Pinned(o.RunnerImage):
		return fmt.Errorf("suite runner image %q is not pinned by digest", o.RunnerImage)
	}
	return nil
}

// refuseOwnPod refuses the runner a request naming the app whose pod it runs in.
func (s *session) refuseOwnPod(apps []string) error {
	if s.o.RunnerID == "" {
		return nil
	}
	for _, a := range apps {
		if hostsRunner(s.cat[a]) {
			return suite.Errorf(suite.RunnerHost, "%s runs this suite runner, which never installs, removes or upgrades its own pod; from the workstation run kyquickstart handoff --revoke, apply, then handoff", a)
		}
	}
	return nil
}

// yardHandover is printed after every apply that installs KyYard.
func yardHandover(state, host, admin string, t stack.Target) string {
	if abs, err := filepath.Abs(state); err == nil {
		state = abs
	}
	return fmt.Sprintf(`
KyYard at https://%[1]s/: sign in through KyIdentity as %[2]s (administration only). Everyday
  accounts are not given KyYard access; KyIdentity refuses them.
  Outstanding human steps:
  1. Its local "admin" break-glass account. Read its one-time password:
       %[3]s -n kyq-kyyard exec deployment/app -c app -- cat /data/initial-admin-password
     Sign in with it once at https://%[1]s/ and change it. Only after that, delete the file:
       %[3]s -n kyq-kyyard exec deployment/app -c app -- rm /data/initial-admin-password
  2. Hand the plan to the suite runner in KyYard's pod:
       kyquickstart handoff --state %[4]s --kyyard-target %[5]s --runner-pod %[6]s
     After that, workstation apply is refused until kyquickstart handoff --revoke. To upgrade
     KyYard: handoff --revoke, apply, then handoff again. KyYard 0.4.2 has no Suite page yet,
     so manage apps from the workstation until a KyYard release that uses the runner.
  3. On its backup screen, as %[2]s, pair it with KyRecovery using a one-time code from
     KyRecovery's administrator, or pin the suite public key by hand. It makes no backups
     until then.
`, host, admin, kubectl(state, t), remote.Quote(state), t.Name, yardRunnerPod)
}
