//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Busnes-app/kyquickstart/internal/cli"
	"github.com/Busnes-app/kyquickstart/internal/kube"
	"github.com/google/uuid"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/utils/ptr"
)

// The test-only client beside the runner, standing in for KyYard: distroless has no client.
const curlImage = "curlimages/curl:8.17.0@sha256:935d9100e9ba842cdb060de42472c7ca90cfe9a7c96e4dacb55e79e560b3ff40"

// testRunnerPod deploys docs/kyyard-runner.k8s.yaml with an image built from this checkout,
// hands the plan to it, installs and uninstalls hello through its socket, pulls the plan back,
// checks that the CLI may not change it, and revokes the runner's access.
func testRunnerPod(t *testing.T, ctx context.Context, cs kubernetes.Interface, cluster, kc string) {
	docker(t, "build", "-q", "--build-arg", "VERSION=e2e", "-t", "kyq-e2e-runner", "../..")
	kind(t, "load", "docker-image", "kyq-e2e-runner", "--name", cluster)
	deployRunnerPod(t, ctx, cs)
	connected, err := kube.Connect(kc, "")
	if err != nil {
		t.Fatal(err)
	}
	var pod string
	t.Cleanup(func() {
		if t.Failed() && pod != "" {
			logs, _ := cs.CoreV1().Pods("kyq-runner").GetLogs(pod, &corev1.PodLogOptions{Container: "runner"}).Do(ctx).Raw()
			t.Logf("runner logs:\n%s", logs)
		}
	})
	call := func(method, path string, body any) (int, map[string]any) {
		t.Helper()
		args := []string{"curl", "-sS", "--max-time", "15", "--unix-socket", "/run/kyquickstart/sock/runner.sock", "-X", method, "-w", "\n%{http_code}"}
		if body != nil {
			b, _ := json.Marshal(body)
			args = append(args, "-H", "Content-Type: application/json", "--data-binary", string(b))
		}
		out, err := connected.Exec(ctx, "kyq-runner", pod, "client", append(args, "http://runner"+path))
		if err != nil {
			return 0, nil
		}
		i := bytes.LastIndexByte(out, '\n')
		code, _ := strconv.Atoi(string(out[i+1:]))
		var res map[string]any
		json.Unmarshal(out[:i], &res)
		return code, res
	}
	for i := 0; ; i++ {
		pods, err := cs.CoreV1().Pods("kyq-runner").List(ctx, metav1.ListOptions{LabelSelector: "app=runner"})
		if err == nil && len(pods.Items) == 1 && pods.Items[0].Status.Phase == corev1.PodRunning {
			pod = pods.Items[0].Name
			if code, _ := call("GET", "/v1/status", nil); code == 409 { // no plan yet
				break
			}
		}
		if i == 180 {
			t.Fatalf("runner pod never answered: %v %+v", err, pods)
		}
		time.Sleep(time.Second)
	}
	operate := func(kind string, apps ...string) map[string]any {
		t.Helper()
		id := uuid.NewString()
		req := map[string]any{"id": id, "kind": kind, "apps": apps, "acknowledge_no_backup": false, "actor": "e2e-admin"}
		if kind == "install" {
			req["target"] = "k8s"
		}
		if code, out := call("POST", "/v1/operations", req); code != 202 {
			t.Fatalf("%s: %d %v", kind, code, out)
		}
		for range 300 {
			if _, op := call("GET", "/v1/operations/"+id, nil); op != nil && op["state"] != "running" {
				t.Logf("%s: %v", kind, op)
				return op
			}
			time.Sleep(time.Second)
		}
		t.Fatalf("%s never finished", kind)
		return nil
	}
	state := t.TempDir()
	run := func(args ...string) error {
		var out bytes.Buffer
		err := cli.Run(ctx, append(args, "--state", state), cli.Options{Catalog: os.DirFS("../../internal/catalog/testdata/apps"), ReleaseSet: "e2e", Out: &out})
		t.Logf("%s:\n%s", strings.Join(args, " "), out.String())
		return err
	}
	deployed := func() bool {
		_, err := cs.AppsV1().Deployments("kyq-hello").Get(ctx, "app", metav1.GetOptions{})
		return err == nil
	}
	os.WriteFile(filepath.Join(state, "stack.yaml"), []byte("version: 1\ndemo: true\ninstallation_id: "+kubeInstallation+"\ntargets:\n  - name: k8s\n    kubeconfig: "+kc+"\napps: []\n"), 0o600)

	for range 2 { // a rerun reconciles
		if err := run("handoff", "--kyyard-target", "k8s", "--runner-pod", "kyq-runner/app=runner"); err != nil {
			t.Fatalf("handoff: %v", err)
		}
	}
	// The workstation reaches kind at 127.0.0.1, which a pod cannot: an install that works went
	// through kubernetes.default.svc.
	if op := operate("install", "hello"); op["state"] != "succeeded" || !deployed() {
		t.Fatalf("install through the runner pod: %v", op)
	}
	code, st := call("GET", "/v1/status", nil)
	if r, _ := st["runner"].(map[string]any); code != 200 || r["plan_owner"] != "runner" || r["plan_revision"] != float64(3) {
		t.Fatalf("status: %d %v", code, st)
	}
	if op := operate("uninstall", "hello"); op["state"] != "succeeded" || deployed() {
		t.Fatalf("uninstall through the runner pod: %v", op)
	}

	if err := run("pull-plan"); err != nil { // the runner from handoff.json
		t.Fatalf("pull-plan: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(state, "stack.yaml")); !strings.Contains(string(b), "revision: 4") || !strings.Contains(string(b), "kubeconfig: "+kc) {
		t.Fatalf("pulled plan:\n%s", b)
	}
	if err := run("install", "hello", "--target", "k8s"); err == nil || !strings.Contains(err.Error(), "owned_by_runner") {
		t.Fatalf("CLI install of a runner-owned plan: %v", err)
	}

	if err := run("handoff", "--revoke"); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if _, err := cs.CoreV1().ServiceAccounts("kyquickstart").Get(ctx, "kyquickstart-runner", metav1.GetOptions{}); err == nil {
		t.Fatal("runner service account left after revoke")
	}
	if m, err := connected.Marker(ctx); err != nil || m.Owner != "cli" || m.RunnerID != "" {
		t.Fatalf("marker after revoke: %+v %v", m, err)
	}
	if op := operate("install", "hello"); op["state"] != "failed" || deployed() {
		t.Fatalf("runner acted after revoke: %v", op)
	}
}

// deployRunnerPod creates the documented manifest with this checkout's image, the cluster's
// API server address in the NetworkPolicy and, for the test only, a curl client sharing the
// socket volume.
func deployRunnerPod(t *testing.T, ctx context.Context, cs kubernetes.Interface) {
	t.Helper()
	b, err := os.ReadFile("../../docs/kyyard-runner.k8s.yaml")
	if err != nil {
		t.Fatal(err)
	}
	slices, err := cs.DiscoveryV1().EndpointSlices("default").List(ctx, metav1.ListOptions{LabelSelector: "kubernetes.io/service-name=kubernetes"})
	if err != nil || len(slices.Items) != 1 {
		t.Fatalf("API server endpoints: %v", err)
	}
	var api []networkingv1.NetworkPolicyPeer
	for _, e := range slices.Items[0].Endpoints {
		for _, a := range e.Addresses {
			api = append(api, networkingv1.NetworkPolicyPeer{IPBlock: &networkingv1.IPBlock{CIDR: a + "/32"}})
		}
	}
	for _, doc := range strings.Split(string(b), "\n---\n") {
		obj, _, err := scheme.Codecs.UniversalDeserializer().Decode([]byte(doc), nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		switch o := obj.(type) {
		case *corev1.Namespace:
			_, err = cs.CoreV1().Namespaces().Create(ctx, o, metav1.CreateOptions{})
		case *corev1.ServiceAccount:
			_, err = cs.CoreV1().ServiceAccounts(o.Namespace).Create(ctx, o, metav1.CreateOptions{})
		case *corev1.PersistentVolumeClaim:
			_, err = cs.CoreV1().PersistentVolumeClaims(o.Namespace).Create(ctx, o, metav1.CreateOptions{})
		case *appsv1.Deployment:
			spec := &o.Spec.Template.Spec
			runner := &spec.Containers[0]
			runner.Image, runner.ImagePullPolicy = "kyq-e2e-runner", corev1.PullNever
			client := runner.DeepCopy()
			client.Name, client.Image, client.ImagePullPolicy, client.Command = "client", curlImage, corev1.PullIfNotPresent, []string{"sleep", "3600"}
			client.VolumeMounts = []corev1.VolumeMount{{Name: "socket", MountPath: "/run/kyquickstart"}}
			spec.Containers = append(spec.Containers, *client)
			_, err = cs.AppsV1().Deployments(o.Namespace).Create(ctx, o, metav1.CreateOptions{})
		case *networkingv1.NetworkPolicy:
			o.Spec.Egress[1].To = api
			o.Spec.Egress[1].Ports[0].Port = ptr.To(intstr.FromInt32(*slices.Items[0].Ports[0].Port))
			_, err = cs.NetworkingV1().NetworkPolicies(o.Namespace).Create(ctx, o, metav1.CreateOptions{})
		default:
			t.Fatalf("unexpected %T in the runner manifest", obj)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
}
