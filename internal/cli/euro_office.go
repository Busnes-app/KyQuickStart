package cli

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/Busnes-app/kyquickstart/internal/catalog"
	"github.com/Busnes-app/kyquickstart/internal/engine"
	"github.com/Busnes-app/kyquickstart/internal/kube"
	"github.com/Busnes-app/kyquickstart/internal/stack"
)

// euroOffice is KyDrive's document editor, on a cluster beside KyDrive on Docker. They share
// a JWT secret: editorSecret, generated on KyDrive's target and copied in memory into the
// editor's Secret as jwt_secret.
const (
	euroOffice   = "euro-office"
	editorSecret = "editor_secret"
)

// editorPlan returns KyDrive connected to the planned euro-office, ok false without one. It
// runs after plan.Order, which refuses euro-office without kydrive (depends_on), and needs no
// connection, so plan and review run it too.
func editorPlan(st *stack.Stack, cat catalog.Catalog) (drive catalog.App, ok bool, err error) {
	if !slices.ContainsFunc(st.Apps, func(a stack.App) bool { return a.Name == euroOffice }) {
		return catalog.App{}, false, nil
	}
	var hosts []string
	for _, e := range st.Edges {
		for _, r := range e.Routes {
			if r.App == euroOffice {
				hosts = append(hosts, e.URLHost(r))
			}
		}
	}
	if len(hosts) != 1 {
		return catalog.App{}, false, fmt.Errorf("browsers and KyDrive reach %s at its route hostname; give it exactly one edge route", euroOffice)
	}
	drive, err = withEditor(cat["kydrive"], hosts[0])
	return drive, err == nil, err
}

// withEditor adds the editor's URL and KyDrive's editor_secret, which its entrypoint exports
// like the session secret (KyDrive reads it only from the environment), to KyDrive's Compose
// file. Each edit must match exactly once, so a changed template fails here, not on a target.
func withEditor(drive catalog.App, host string) (catalog.App, error) {
	compose := string(drive.Compose)
	for _, edit := range [][2]string{
		{"    secrets: [session_secret, scim_token]\n", "    secrets: [session_secret, scim_token, editor_secret]\n"},
		{"unset s; exec /app/kydrive-server", "unset s; e=$$(cat /run/secrets/editor_secret) || exit 1; KYDRIVE_EDITOR_SECRET=$$e; export KYDRIVE_EDITOR_SECRET; unset e; exec /app/kydrive-server"},
		{"      KY_SCIM_TOKEN_FILE: /run/secrets/scim_token\n", "      KY_SCIM_TOKEN_FILE: /run/secrets/scim_token\n      KYDRIVE_EDITOR_URL: \"https://" + host + "\"\n"},
		{"  scim_token:\n    file: ./secrets/scim_token\n", "  scim_token:\n    file: ./secrets/scim_token\n  editor_secret:\n    file: ./secrets/editor_secret\n"},
	} {
		if strings.Count(compose, edit[0]) != 1 {
			return catalog.App{}, fmt.Errorf("kydrive's Compose file no longer has %q; %s cannot connect to it", edit[0], euroOffice)
		}
		compose = strings.Replace(compose, edit[0], edit[1], 1)
	}
	drive.Compose = []byte(compose)
	drive.Secrets = append(slices.Clone(drive.Secrets), editorSecret)
	return drive, nil
}

// editorSteps are euro-office's install steps, its jwt_secret read from KyDrive's target.
func (s *session) editorSteps(d kubeDriver) []engine.Step {
	a := d.app(euroOffice, s.cat[euroOffice], s.o.ReleaseSet, s.redact)
	drive := s.drivers[s.placed["kydrive"].Name]
	a.SecretFrom = map[string]func(context.Context) ([]byte, error){
		"jwt_secret": func(ctx context.Context) ([]byte, error) { return drive.appSecret(ctx, "kydrive", editorSecret) },
	}
	return kube.Steps(a)
}
