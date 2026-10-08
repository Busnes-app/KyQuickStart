//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Busnes-app/kyquickstart/internal/cli"
	"github.com/Busnes-app/kyquickstart/internal/kube"
	"github.com/Busnes-app/kyquickstart/internal/managed"
	coordinationv1 "k8s.io/api/coordination/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/utils/ptr"
)

const kindNode = "kindest/node:v1.37.0@sha256:a1ed56cfb0e7b93589bdf97c8cd566405a265939e3620fc4f5de89adff580ae5"

func kind(t *testing.T, args ...string) {
	t.Helper()
	out, err := exec.Command("go", append([]string{"run", "sigs.k8s.io/kind@v0.33.0"}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("kind %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func TestKubernetesApply(t *testing.T) {
	ctx := context.Background()
	work := t.TempDir()
	state := filepath.Join(work, "state")
	os.MkdirAll(state, 0o700)
	kc := filepath.Join(work, "kubeconfig") // outside the state directory
	id := make([]byte, 4)
	rand.Read(id)
	name := "kyq-e2e-" + hex.EncodeToString(id)
	kind(t, "create", "cluster", "--name", name, "--image", kindNode, "--kubeconfig", kc, "--wait", "180s")
	t.Cleanup(func() {
		exec.Command("go", "run", "sigs.k8s.io/kind@v0.33.0", "delete", "cluster", "--name", name).Run()
	})

	cfg, err := clientcmd.BuildConfigFromFlags("", kc)
	if err != nil {
		t.Fatal(err)
	}
	cs := kubernetes.NewForConfigOrDie(cfg)

	os.WriteFile(filepath.Join(state, "stack.yaml"), []byte(
		"version: 1\ntargets:\n  - name: k8s\n    kubeconfig: "+kc+"\napps:\n  - name: hello\n    target: k8s\n"), 0o600)
	apply := func() (string, error) {
		var out bytes.Buffer
		o := cli.Options{Catalog: os.DirFS("../../internal/catalog/testdata/apps"), ReleaseSet: "e2e", Out: &out}
		err := cli.Run(ctx, []string{"apply", "--state", state}, o)
		t.Logf("apply:\n%s", out.String())
		return out.String(), err
	}

	// First apply installs.
	out, err := apply()
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	ns, err := cs.CoreV1().Namespaces().Get(ctx, "kyq-hello", metav1.GetOptions{})
	if err != nil || ns.Labels["pod-security.kubernetes.io/enforce"] != "restricted" || ns.Labels[managed.Label] != managed.Value {
		t.Fatalf("namespace = %+v, %v", ns, err)
	}
	sec, err := cs.CoreV1().Secrets("kyq-hello").Get(ctx, "kyq-secrets", metav1.GetOptions{})
	if err != nil || len(sec.Data["token"]) != 43 {
		t.Fatalf("secret: %v", err)
	}
	secret := string(sec.Data["token"])
	if strings.Contains(out, secret) {
		t.Error("secret printed")
	}
	filepath.WalkDir(state, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if b, _ := os.ReadFile(p); bytes.Contains(b, []byte(secret)) {
				t.Errorf("secret stored in %s", p)
			}
		}
		return nil
	})
	dep, err := cs.AppsV1().Deployments("kyq-hello").Get(ctx, "app", metav1.GetOptions{})
	if err != nil || dep.Labels[managed.ReleaseSetLabel] != "e2e" || dep.Status.ReadyReplicas != 1 {
		t.Fatalf("deployment = %+v, %v", dep.Status, err)
	}

	// Second apply changes nothing.
	out, err = apply()
	if err != nil || status(out, "hello.secrets") != "skipped" || status(out, "hello.deploy") != "skipped" || status(out, "hello.health") != "skipped" {
		t.Fatalf("second apply: %v", err)
	}

	// Scaled to zero by hand: redeployed.
	dep, _ = cs.AppsV1().Deployments("kyq-hello").Get(ctx, "app", metav1.GetOptions{})
	dep.Spec.Replicas = ptr.To(int32(0))
	if _, err := cs.AppsV1().Deployments("kyq-hello").Update(ctx, dep, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	out, err = apply()
	if err != nil || status(out, "hello.deploy") != "ok" || status(out, "hello.secrets") != "skipped" {
		t.Fatalf("apply after scale-down: %v", err)
	}
	dep, _ = cs.AppsV1().Deployments("kyq-hello").Get(ctx, "app", metav1.GetOptions{})
	if *dep.Spec.Replicas != 1 || dep.Status.ReadyReplicas != 1 {
		t.Fatalf("not restored: %+v", dep.Status)
	}

	// A Lease left by another run stops apply and survives.
	started := metav1.NewMicroTime(time.Now().Add(-time.Hour))
	other := "other run"
	_, err = cs.CoordinationV1().Leases("kyquickstart").Create(ctx, &coordinationv1.Lease{
		ObjectMeta: metav1.ObjectMeta{Name: "kyquickstart-lock", Namespace: "kyquickstart", Annotations: map[string]string{"kyquickstart/run": "x"}},
		Spec:       coordinationv1.LeaseSpec{HolderIdentity: &other, AcquireTime: &started},
	}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = apply()
	var le *kube.LockedError
	if !errors.As(err, &le) || le.Holder != "other run" {
		t.Fatalf("err = %v, want kube.LockedError", err)
	}
	if _, err := cs.CoordinationV1().Leases("kyquickstart").Get(ctx, "kyquickstart-lock", metav1.GetOptions{}); err != nil {
		t.Fatalf("stale lease removed: %v", err)
	}
}
