package cli

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/Busnes-app/kyquickstart/internal/remote"
)

const identityCaps = `{"schema":"ky.setup-capabilities/1","service":"kyidentity","bundleSchema":"kyidentity.setup/1","features":["local-accounts","confidential-oidc-clients","assigned-only","everyday-assignment","app-role-assignments","scim-connectors","suite-webhook-connectors","directory-groups","app-group-assignments","scim-groups","check"]}`

func TestIdentitySetupRequiresProductContract(t *testing.T) {
	for _, caps := range []string{`{"schema":"wrong"}`, strings.Replace(identityCaps, "\"assigned-only\",", "", 1), strings.Replace(identityCaps, "\"app-role-assignments\",", "", 1), strings.Replace(identityCaps, "\"scim-connectors\",", "", 1), strings.Replace(identityCaps, "\"suite-webhook-connectors\",", "", 1), strings.Replace(identityCaps, "\"directory-groups\",", "", 1), strings.Replace(identityCaps, "\"app-group-assignments\",", "", 1), strings.Replace(identityCaps, "\"scim-groups\",", "", 1)} {
		calls := 0
		_, err := identitySetup(context.Background(), func(_ context.Context, args []string) ([]byte, error) { calls++; return []byte(caps), nil }, "/data/setup.json", false)
		if err == nil || calls != 1 {
			t.Fatalf("unsupported product mutated: calls %d error %v", calls, err)
		}
	}
}

func TestIdentityGroupReportsAreSanitized(t *testing.T) {
	for _, name := range []string{"Suite users", " Suite users", "bad\ngroup", strings.Repeat("x", 65)} {
		calls := 0
		out, err := identitySetup(context.Background(), func(_ context.Context, _ []string) ([]byte, error) {
			calls++
			if calls == 1 {
				return []byte(identityCaps), nil
			}
			return json.Marshal(map[string]any{"schema": "kyidentity.setup/1", "created": []any{}, "reused": []any{map[string]string{"kind": "group", "name": name, "id": "group-id", "secret": "must-not-print"}}})
		}, "/data/setup.json", true)
		if name == "Suite users" {
			if err != nil || strings.Contains(string(out), "must-not-print") {
				t.Fatal("valid group refused or leaked", err)
			}
		} else if err == nil {
			t.Fatal("invalid group report accepted")
		}
	}
}

func TestIdentityRoleReportsAreBoundedAndSanitized(t *testing.T) {
	for _, name := range []string{"drive/owner:admin", "bad\nrole", strings.Repeat("x", 130)} {
		calls := 0
		out, err := identitySetup(context.Background(), func(_ context.Context, _ []string) ([]byte, error) {
			calls++
			if calls == 1 {
				return []byte(identityCaps), nil
			}
			return json.Marshal(map[string]any{"schema": "kyidentity.setup/1", "created": []any{}, "reused": []any{map[string]string{"kind": "role", "name": name, "id": "role-id", "secret": "must-not-print"}}})
		}, "/data/setup.json", true)
		if name == "drive/owner:admin" {
			if err != nil || strings.Contains(string(out), "must-not-print") {
				t.Fatal("valid role receipt rejected or leaked", err)
			}
		} else if err == nil {
			t.Fatal("invalid role report accepted")
		}
	}
}

func TestIdentityInspectionRefusesMutatingReport(t *testing.T) {
	calls := 0
	_, err := identitySetup(context.Background(), func(_ context.Context, _ []string) ([]byte, error) {
		calls++
		if calls == 1 {
			return []byte(identityCaps), nil
		}
		return []byte(`{"schema":"kyidentity.setup/1","created":[{"kind":"role","name":"drive.admin","id":"role-id"}],"reused":[]}`), nil
	}, "/data/setup.json", true)
	if err == nil {
		t.Fatal("read-only setup accepted a mutating report")
	}
}
func TestIdentitySetupCheckAndSafeReport(t *testing.T) {
	calls := 0
	out, err := identitySetup(context.Background(), func(_ context.Context, args []string) ([]byte, error) {
		calls++
		if calls == 1 {
			return []byte(identityCaps), nil
		}
		if strings.Join(args, " ") != "apply-setup --file /data/setup.json --check" {
			t.Fatalf("wrong invocation %v", args)
		}
		return []byte(`{"schema":"kyidentity.setup/1","created":[],"reused":[{"kind":"user","name":"owner-admin","id":"example-id","password":"must-not-print"}],"token":"must-not-print"}`), nil
	}, "/data/setup.json", true)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "must-not-print") || calls != 2 {
		t.Fatal("unsafe report", string(out))
	}
}

type identityRunner func(context.Context, string, []byte) ([]byte, error)

func (f identityRunner) Run(ctx context.Context, cmd string, in []byte) ([]byte, error) {
	return f(ctx, cmd, in)
}
func TestDockerIdentityExecQuotesAndSanitizesErrors(t *testing.T) {
	r := identityRunner(func(_ context.Context, cmd string, _ []byte) ([]byte, error) {
		actual, err := (remote.Local{}).Run(context.Background(), `docker() { printf '%s\n' "$@"; }; `+cmd, nil)
		if err != nil || string(actual) != "exec\nidentity\n/usr/local/bin/kyidentity\napply-setup\n--file\n/data/o'wner.json\n" {
			t.Fatalf("shell arguments %q: %v", actual, err)
		}

		return nil, &remote.ExitError{Code: 1, Stderr: []byte("credential-must-not-print")}
	})
	_, err := dockerIdentityExec(r, "identity")(context.Background(), []string{"apply-setup", "--file", "/data/o'wner.json"})
	if err == nil || strings.Contains(err.Error(), "credential-must-not-print") {
		t.Fatal("unredacted command error", err)
	}
	if _, err := identitySetup(context.Background(), func(context.Context, []string) ([]byte, error) { return nil, errors.New("disconnected") }, "/data/bundle", false); err == nil {
		t.Fatal("disconnect accepted")
	}
}

func TestDockerProductInputExecUsesStdinAndBoundsInput(t *testing.T) {
	calls := 0
	r := identityRunner(func(ctx context.Context, cmd string, input []byte) ([]byte, error) {
		calls++
		if strings.Contains(cmd, "metadata-marker") || !strings.Contains(cmd, "docker exec -i") {
			t.Fatal("body entered command arguments", cmd)
		}
		return (remote.Local{}).Run(ctx, `docker() { cat; }; `+cmd, input)
	})
	exec := dockerProductInputExec(r, "notes", "/kynotes-server")
	out, err := exec(context.Background(), []string{"apply-setup", "--stage", "--file", "/data/installer/setup.json"}, []byte("metadata-marker"))
	if err != nil || string(out) != "metadata-marker" {
		t.Fatal("stdin not delivered", err)
	}
	if _, err := exec(context.Background(), nil, make([]byte, (128<<10)+1)); err == nil || calls != 1 {
		t.Fatal("unbounded input reached target")
	}
}
