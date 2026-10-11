package librechat

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNativeInspectionModule(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("Node required for native module checks")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "node", "--max-old-space-size=256", "--test", "module/account.test.cjs", "module/writers.test.cjs").CombinedOutput()
	if err != nil {
		t.Fatalf("native inspection module: %v\n%s", err, output)
	}
	cmd := exec.CommandContext(ctx, "node", "--max-old-space-size=256", "module/command.cjs", "--capabilities")
	cmd.Env = append(os.Environ(), "KYQ_LIBRECHAT_APP_ROOT=/nonexistent-native-root")
	output, err = cmd.Output()
	var cap struct {
		Contract string   `json:"contract"`
		Version  string   `json:"module_version"`
		Features []string `json:"features"`
	}
	if err != nil || !decode(output, &cap) || cap.Contract != inspectionContract || cap.Version != moduleVersion ||
		len(cap.Features) != 1 || cap.Features[0] != "account-inspection" {
		t.Fatal("read-only capability discovery", err)
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "package.json"), []byte(`{"name":"LibreChat","version":"foreign"}`), 0600); err != nil {
		t.Fatal(err)
	}
	cmd = exec.CommandContext(ctx, "node", "--max-old-space-size=256", "module/command.cjs", "--file", "/missing-request")
	cmd.Env = append(os.Environ(), "KYQ_LIBRECHAT_APP_ROOT="+root)
	output, err = cmd.CombinedOutput()
	if err == nil || string(output) != "LibreChat native inspection refused\n" {
		t.Fatal("foreign release was not generically refused", err)
	}
}

func TestNativeReportBindingAndRole(t *testing.T) {
	b := Binding{ID: "0123456789abcdef01234567", Issuer: "https://identity.example.test", Subject: "subject-雪"}
	wire := map[string]any{"contract": "ky.librechat-account-report/1", "module_version": moduleVersion,
		"user_id": b.ID, "binding_digest": bindingDigest(b), "role": "ADMIN"}
	data, _ := json.Marshal(wire)
	a, err := readReport(data, b)
	if err != nil || a.Role != "ADMIN" || a.Binding != b {
		t.Fatal("valid identity report refused", err)
	}
	for _, key := range []string{"contract", "module_version", "user_id", "binding_digest", "role"} {
		old := wire[key]
		wire[key] = "wrong"
		data, _ := json.Marshal(wire)
		if _, err := readReport(data, b); err == nil {
			t.Fatalf("accepted invalid %s", key)
		}
		delete(wire, key)
		data, _ = json.Marshal(wire)
		if _, err := readReport(data, b); err == nil {
			t.Fatalf("accepted missing %s", key)
		}
		wire[key] = old
	}
	data, _ = json.Marshal(wire)
	for _, bad := range [][]byte{append(data, []byte(" {}")...), []byte(strings.Repeat("x", 16385)),
		bytes.Replace(data, []byte(`"role":"ADMIN"`), []byte(`"role":"ADMIN","email":"private"`), 1)} {
		if _, err := readReport(bad, b); err == nil {
			t.Fatal("accepted malformed/oversized report")
		}
	}
}

func TestInspectStagesPrivateInputAndCleansUp(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("Node required for native module unit transport")
	}
	dir, err := filepath.EvalSymlinks(t.TempDir()) // macOS /var is a symlink
	if err != nil {
		t.Fatal(err)
	}
	module, err := filepath.Abs("module/account.cjs")
	if err != nil {
		t.Fatal(err)
	}
	quoted, _ := json.Marshal(module)
	command := filepath.Join(dir, "command.cjs")
	source := `const account=require(` + string(quoted) + `);
if(process.argv[2]==='--capabilities') {
 console.log(JSON.stringify({contract:account.contract,module_version:account.version,features:['account-inspection']}));
} else {
 const request=account.readRequest(process.argv[3],[]);
 const native={runAsSystem:fn=>fn(),getUserById:async()=>({_id:'0123456789abcdef01234567',provider:'openid',
 openidId:'subject-雪',openidIssuer:'https://identity.example.test',role:'USER'})};
 account.inspect(request,native,'https://identity.example.test').then(report=>console.log(JSON.stringify(report))).catch(()=>process.exitCode=1);
}`
	if err := os.WriteFile(command, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	// Run the real shell staging and module file reader. Native model storage is
	// substituted here; production command/MongoDB qualification remains separate.
	execute := func(ctx context.Context, argv []string, input []byte) ([]byte, error) {
		for _, arg := range argv {
			if strings.Contains(arg, "subject-雪") {
				t.Fatal("subject leaked into argv")
			}
		}
		cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
		cmd.Stdin = bytes.NewReader(input)
		return cmd.Output()
	}
	c, err := New(execute, dir, command)
	if err != nil {
		t.Fatal(err)
	}
	b := Binding{ID: "0123456789abcdef01234567", Issuer: "https://identity.example.test", Subject: "subject-雪"}
	for range 2 {
		a, err := c.Inspect(context.Background(), b)
		if err != nil || a.Role != "USER" || a.Binding != b {
			t.Fatal("native module unit transport", err)
		}
	}
	b.Subject = "foreign"
	if _, err := c.Inspect(context.Background(), b); err == nil {
		t.Fatal("foreign subject admitted")
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 || entries[0].Name() != "command.cjs" {
		t.Fatal("private request staging was not cleaned", err)
	}
}

func TestInspectRejectsBeforeNativeLookup(t *testing.T) {
	calls := 0
	c, err := New(func(context.Context, []string, []byte) ([]byte, error) {
		calls++
		return nil, errors.New("private native diagnostic")
	}, "/private", "/app/kyquickstart/command.cjs")
	if err != nil {
		t.Fatal(err)
	}
	b := Binding{ID: "0123456789abcdef01234567", Issuer: "https://identity.example.test", Subject: "subject"}
	for _, invalid := range []Binding{{}, {ID: "email", Issuer: b.Issuer, Subject: b.Subject},
		{ID: b.ID, Issuer: "http://identity.test", Subject: b.Subject},
		{ID: b.ID, Issuer: b.Issuer + "?", Subject: b.Subject},
		{ID: b.ID, Issuer: b.Issuer, Subject: "\x00"},
		{ID: b.ID, Issuer: b.Issuer, Subject: string([]byte{0xff})}} {
		if _, err := c.Inspect(context.Background(), invalid); err == nil {
			t.Fatal("invalid binding admitted")
		}
	}
	if calls != 0 {
		t.Fatal("invalid input reached native command")
	}
	if _, err := c.Inspect(context.Background(), b); err == nil || strings.Contains(err.Error(), "private") {
		t.Fatal("native diagnostics escaped")
	}
	c.execute = func(context.Context, []string, []byte) ([]byte, error) {
		calls++
		return []byte(`{"contract":"ky.librechat-account-inspect/1","module_version":"0.1.0","features":["revoke"]}`), nil
	}
	before := calls
	if _, err := c.Inspect(context.Background(), b); err == nil || calls != before+1 {
		t.Fatal("unsupported capability reached lookup")
	}
}
