package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/Busnes-app/kyquickstart/internal/managed"
	"github.com/Busnes-app/kyquickstart/internal/remote"
	"github.com/Busnes-app/kyquickstart/internal/stack"
)

func calendarRuntimeHandover(state, host, admin, everyday string, t stack.Target) string {
	if t.Kubernetes() {
		return fmt.Sprintf(calendarHandover, host, admin, everyday, kubectl(state, t))
	}
	text := fmt.Sprintf(calendarHandover, host, admin, everyday, "docker")
	text = strings.ReplaceAll(text, "docker -n kyq-kycalendar exec deployment/app --", "docker exec kyq-kycalendar-app-1")
	return text + fmt.Sprintf("  Run the Docker commands on target %s.\n", t.Name)
}

func dockerYardHandover(state, host, admin string, t stack.Target) string {
	return fmt.Sprintf(`
KyYard at https://%s/: sign in through KyIdentity as %s (administration only).
  Everyday accounts have no KyYard grant. On target %s, read its break-glass password:
    docker exec kyq-kyyard-app-1 cat /data/initial-admin-password
  Sign in once as local "admin", change the password, then remove the file:
    docker exec kyq-kyyard-app-1 rm /data/initial-admin-password
  Hand the plan to its separate, private suite runner:
    kyquickstart handoff --state %s --kyyard-target %s --runner-container kyq-kyyard-runner-1
  Workstation apply is then refused until handoff --revoke. To upgrade KyYard,
  revoke, apply from the workstation, then hand off again. The owner's KyIdentity
  administrator can view Suite; only local "admin" can start its operations.
  Pair backups with KyRecovery or pin the suite key on its Recovery screen.
`, host, admin, t.Name, remote.Quote(state), t.Name)
}

// installedIdentityIssuer reads only the public issuer, never other environment
// values. Managed Compose service identity excludes the runner sidecar.
func installedIdentityIssuer(ctx context.Context, d driver) (string, bool, error) {
	switch d := d.(type) {
	case kubeDriver:
		return d.c.AppEnv(ctx, "kyidentity", issuerEnv)
	case sshDriver:
		command := "c=$(docker ps -aq --filter " + remote.Quote("label=com.docker.compose.project=kyq-kyidentity") + " --filter " + remote.Quote("label=com.docker.compose.service=app") + " --filter " + remote.Quote("label="+managed.Label+"="+managed.Value) + ") && " +
			"case $c in '') exit 0;; *[!0-9a-f]*) exit 1;; esac && kyq_environment=$(docker inspect --format " + remote.Quote("{{range .Config.Env}}{{println .}}{{end}}") + ` "$c") && printf '%s\n' "$kyq_environment" | sed -n 's/^KYIDENTITY_ISSUER_URL=//p'`
		out, err := d.c.Run(ctx, command, nil)
		if err != nil {
			return "", false, err
		}
		value := strings.TrimSpace(string(out))
		if len(value) > 2048 || strings.ContainsAny(value, "\r\n") {
			return "", false, fmt.Errorf("invalid KyIdentity issuer readback")
		}
		return value, value != "", nil
	}
	return "", false, fmt.Errorf("KyIdentity's target is not connected")
}
