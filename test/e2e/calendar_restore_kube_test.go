//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Busnes-app/kyquickstart/internal/cli"
	"github.com/Busnes-app/kyquickstart/internal/kube"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// Only the test operator holds these throwaway custodian shares. The live
// products receive the suite public key, never a recovery private key/share.
type calendarRestoreKit struct {
	PublicKey, KeyID string
	Shares           []string
}

// restoreNativeCalendar follows KyCalendar's native capsule runbook, not the
// installer's Docker-only restic command. The old live data is moved aside.
func restoreNativeCalendar(t *testing.T, ctx context.Context, cs kubernetes.Interface, c *kube.Client, state, catalogDir string, trust []string, certPEM []byte, nodeIP string, kit calendarRestoreKit, verify func(*http.Client)) {
	t.Helper()
	if kit.PublicKey == "" || kit.KeyID == "" || len(kit.Shares) != 2 {
		t.Fatal("Calendar restore has no two-share suite ceremony kit")
	}
	// Later unrelated apply calls replace the fixture CA. Restore test trust,
	// then use fresh native sign-ins for the real step-up-protected export.
	trustTestCA(t, ctx, cs, c, "kycalendar", certPEM)
	admin, everyday := calendarSignOnClients(t, ctx, c, certPEM, nodeIP)
	origin := "https://" + catalogCalendarURLHost
	call := func(client *http.Client, path string, body []byte, want int) (http.Header, []byte) {
		t.Helper()
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, origin+path, bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Origin", origin)
		req.Header.Set("Content-Type", "application/json")
		u, _ := url.Parse(origin)
		for _, cookie := range client.Jar.Cookies(u) {
			if cookie.Name == "ky_csrf" {
				req.Header.Set("X-CSRF-Token", cookie.Value)
			}
		}
		r, err := client.Do(req)
		if err != nil {
			t.Fatal("Calendar recovery API", err)
		}
		defer r.Body.Close()
		raw, err := io.ReadAll(io.LimitReader(r.Body, (16<<20)+1))
		if err != nil || len(raw) > 16<<20 || r.StatusCode != want {
			t.Fatalf("Calendar recovery %s: status %d, read %v", path, r.StatusCode, err)
		}
		return r.Header, raw
	}
	call(everyday, "/api/backup/export-capsule", nil, http.StatusForbidden)
	pin, _ := json.Marshal(map[string]any{"public_key": kit.PublicKey, "threshold": 2, "total_shares": 3})
	_, raw := call(admin, "/api/backup/pin-key", pin, http.StatusOK)
	var pinned struct {
		KeyID string `json:"recovery_key_id"`
	}
	if json.Unmarshal(raw, &pinned) != nil || pinned.KeyID != kit.KeyID {
		t.Fatal("Calendar did not pin the suite ceremony key")
	}
	ns, appPod, container, err := c.AppPod(ctx, "kycalendar")
	if err != nil {
		t.Fatal(err)
	}
	live, err := cs.CoreV1().Pods(ns).Get(ctx, appPod, metav1.GetOptions{})
	if err != nil || len(live.Spec.Containers) != 1 {
		t.Fatal("Calendar live pod", err)
	}
	keyArgs := []string{"sha256sum", "/app/data/encryption.key", "/app/data/recovery.pub", calendarSetupDir + "/client.secret", calendarSetupDir + "/directory.secret"}
	beforeKeys, err := c.Exec(ctx, ns, appPod, container, keyArgs)
	if err != nil {
		t.Fatal("Calendar pre-restore key checkpoint", err)
	}
	header, capsule := call(admin, "/api/backup/export-capsule", nil, http.StatusOK)
	defer clear(capsule)
	if header.Get("X-Recovery-Key-ID") != kit.KeyID || len(capsule) == 0 {
		t.Fatal("Calendar exported no suite-key capsule")
	}
	lifecycle := func(op string) {
		t.Helper()
		var out bytes.Buffer
		err := cli.Run(ctx, append([]string{op, "kycalendar", "--state", state}, trust...), cli.Options{Catalog: os.DirFS(catalogDir), ReleaseSet: "e2e", Out: &out})
		t.Logf("Calendar native restore %s: %v\n%s", op, err, out.String())
		if err != nil {
			t.Fatal("Calendar lifecycle", op, err)
		}
	}
	lifecycle("stop")
	spec := *live.Spec.DeepCopy()
	spec.InitContainers = nil
	spec.RestartPolicy = corev1.RestartPolicyNever
	spec.Containers[0].Command = []string{"/bin/sh", "-c", "sleep 600"}
	spec.Containers[0].Args = nil
	spec.Containers[0].StartupProbe = nil
	spec.Containers[0].LivenessProbe = nil
	spec.Containers[0].ReadinessProbe = nil
	helper, err := cs.CoreV1().Pods(ns).Create(ctx, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{GenerateName: "calendar-native-restore-", Labels: map[string]string{"test": "calendar-native-restore"}}, Spec: spec}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal("create private offline restore pod", err)
	}
	zero := int64(0)
	remove := func() error {
		return cs.CoreV1().Pods(ns).Delete(ctx, helper.Name, metav1.DeleteOptions{GracePeriodSeconds: &zero, Preconditions: &metav1.Preconditions{UID: &helper.UID}})
	}
	t.Cleanup(func() { _ = remove() })
	deadline := time.Now().Add(90 * time.Second)
	for {
		p, err := cs.CoreV1().Pods(ns).Get(ctx, helper.Name, metav1.GetOptions{})
		if err != nil {
			t.Fatal("offline restore pod", err)
		}
		if p.Status.Phase == corev1.PodRunning && len(p.Status.ContainerStatuses) == 1 && p.Status.ContainerStatuses[0].Ready {
			break
		}
		if p.Status.Phase == corev1.PodFailed || time.Now().After(deadline) {
			t.Fatal("offline restore pod did not become ready")
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(time.Second):
		}
	}
	exec := func(args ...string) []byte {
		t.Helper()
		out, err := c.Exec(ctx, ns, helper.Name, "app", args)
		if err != nil {
			t.Fatal("offline Calendar command", err)
		}
		return out
	}
	exec("sh", "-c", "umask 077; test ! -e /app/data/.lifecycle-capsule.kycap; : > /app/data/.lifecycle-capsule.kycap")
	for offset := 0; offset < len(capsule); offset += 32 << 10 {
		end := min(offset+(32<<10), len(capsule))
		if _, err := c.ExecInput(ctx, ns, helper.Name, "app", []string{"sh", "-c", "cat >> /app/data/.lifecycle-capsule.kycap"}, capsule[offset:end]); err != nil {
			t.Fatal("transfer sealed Calendar capsule", err)
		}
	}
	sum := sha256.Sum256(capsule)
	got := strings.Fields(string(exec("sha256sum", "/app/data/.lifecycle-capsule.kycap")))
	if len(got) != 2 || got[0] != hex.EncodeToString(sum[:]) {
		t.Fatal("Calendar capsule transfer digest mismatch")
	}
	// A single custodian may not publish a restored database.
	if _, err := c.ExecInput(ctx, ns, helper.Name, "app", []string{"/app/kycalendar", "restore", "-capsule", "/app/data/.lifecycle-capsule.kycap", "-to", "/app/data/.lifecycle-refused", "-service", "kycalendar"}, []byte(kit.Shares[0]+"\n")); err == nil {
		t.Fatal("one custodian share restored a two-share capsule")
	}
	exec("sh", "-c", "test ! -f /app/data/.lifecycle-refused/data/kycalendar.db")
	input := []byte(strings.Join(kit.Shares, "\n") + "\n")
	defer clear(input)
	out, err := c.ExecInput(ctx, ns, helper.Name, "app", []string{"sh", "-c", "/app/kycalendar restore -capsule /app/data/.lifecycle-capsule.kycap -to /app/data/.lifecycle-restored -service kycalendar 2>/app/data/.lifecycle-restore.stderr"}, input)
	if err != nil || !strings.Contains(string(out), "Sessions, MFA challenges and app passwords revoked") {
		t.Fatal("native Calendar capsule restore/reset failed", err)
	}
	var paths map[string]string
	if json.Unmarshal(exec("cat", "/app/data/.lifecycle-restored/installer/credential-paths.json"), &paths) != nil || len(paths) != 2 || paths["installer/client.secret"] != calendarSetupDir+"/client.secret" || paths["installer/directory.secret"] != calendarSetupDir+"/directory.secret" {
		t.Fatal("restored Calendar credential paths differ from its managed setup")
	}
	// Runbook: restore sealed installer credentials to the recorded absolute paths,
	// privately, before startup. Preserve the entire original tree instead of merging it.
	exec("sh", "-c", `set -eu
mkdir -m 700 /app/data/.lifecycle-before
for entry in /app/data/* /app/data/.[!.]* /app/data/..?*; do
 [ -e "$entry" ] || [ -L "$entry" ] || continue
 case "$entry" in /app/data/.lifecycle-*) continue ;; esac
 mv "$entry" /app/data/.lifecycle-before/
done
cp -a /app/data/.lifecycle-restored/data/. /app/data/
mkdir -p -m 700 `+calendarSetupDir+`
chmod 700 /app/data/installer `+calendarSetupDir+`
cp /app/data/.lifecycle-restored/installer/client.secret `+calendarSetupDir+`/client.secret
cp /app/data/.lifecycle-restored/installer/directory.secret `+calendarSetupDir+`/directory.secret
chmod 600 `+calendarSetupDir+`/client.secret `+calendarSetupDir+`/directory.secret`)
	if got := exec(keyArgs...); !bytes.Equal(got, beforeKeys) {
		t.Fatal("native Calendar restore changed data/recovery/OIDC/directory key bytes")
	}
	if err := remove(); err != nil {
		t.Fatal("remove offline Calendar restore pod", err)
	}
	deadline = time.Now().Add(30 * time.Second)
	for {
		_, err := cs.CoreV1().Pods(ns).Get(ctx, helper.Name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			break
		}
		if err != nil || time.Now().After(deadline) {
			t.Fatal("offline restore pod did not terminate", err)
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(time.Second):
		}
	}
	lifecycle("start")
	for _, client := range []*http.Client{admin, everyday} {
		r, err := client.Get(origin + "/api/calendars")
		if err != nil {
			t.Fatal("Calendar old-session check", err)
		}
		r.Body.Close()
		if r.StatusCode != http.StatusUnauthorized {
			t.Fatalf("Calendar accepted or misclassified a pre-restore session: HTTP%d", r.StatusCode)
		}
	}
	_, restoredEveryday := calendarSignOnClients(t, ctx, c, certPEM, nodeIP)
	verify(restoredEveryday)
	t.Log("candidate Calendar native capsule restore passed with two custodian shares; one share refused, keys retained, old sessions revoked and fresh everyday event access/edit passed")
}
