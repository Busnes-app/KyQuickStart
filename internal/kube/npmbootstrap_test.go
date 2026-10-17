package kube

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/Busnes-app/kyquickstart/internal/edge"
	"github.com/Busnes-app/kyquickstart/internal/engine"
	"github.com/Busnes-app/kyquickstart/internal/managed"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes/fake"
)

const npmPassword = "generated-admin-password-0123456789abcdefgh"

// fakeNPM keeps NPM's account rules: setup mode until the first user, tokens rejected after
// their user's password changes or when expired.
type fakeNPM struct {
	mu      sync.Mutex
	users   map[string]string // email -> password
	tokens  map[string]string // token -> email
	issued  int
	failAll bool
}

func (f *fakeNPM) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failAll {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, npmPassword)
		return
	}
	var body map[string]any
	json.NewDecoder(r.Body).Decode(&body)
	str := func(k string) string { s, _ := body[k].(string); return s }
	me := f.tokens[strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")]
	reply := func(status int, v any) {
		w.WriteHeader(status)
		json.NewEncoder(w).Encode(v)
	}
	switch r.Method + " " + r.URL.Path {
	case "GET /api/":
		reply(200, map[string]any{"status": "OK", "setup": len(f.users) > 0})
	case "POST /api/users":
		if len(f.users) > 0 && me == "" {
			reply(403, nil)
			return
		}
		auth, _ := body["auth"].(map[string]any)
		f.users[str("email")] = auth["secret"].(string)
		reply(201, map[string]any{})
	case "POST /api/tokens":
		if pw, ok := f.users[str("identity")]; !ok || pw != str("secret") {
			reply(400, map[string]any{"error": "Invalid email or password"})
			return
		}
		f.issued++
		token := fmt.Sprintf("fake-npm-token-%d", f.issued)
		f.tokens[token] = str("identity")
		reply(200, map[string]any{"token": token})
	case "GET /api/users/me":
		if me == "" {
			reply(400, nil)
			return
		}
		reply(200, map[string]any{"email": me})
	case "PUT /api/users/me":
		if me == "" {
			reply(400, nil)
			return
		}
		f.users[str("email")] = f.users[me]
		delete(f.users, me)
		for t, e := range f.tokens {
			if e == me {
				f.tokens[t] = str("email")
			}
		}
		reply(200, map[string]any{})
	case "PUT /api/users/me/auth":
		if me == "" || f.users[me] != str("current") {
			reply(400, nil)
			return
		}
		f.users[me] = str("secret")
		f.expire(me)
		reply(200, true)
	default:
		reply(404, nil)
	}
}

// expire rejects every token issued to email.
func (f *fakeNPM) expire(email string) {
	for t, e := range f.tokens {
		if e == email {
			delete(f.tokens, t)
		}
	}
}

func (f *fakeNPM) login(email, password string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	pw, ok := f.users[email]
	return ok && pw == password
}

// npmFixture is an npm app on a fake cluster whose port-forward reaches f.
func npmFixture(t *testing.T, f *fakeNPM) (App, *fake.Clientset) {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	ctx := context.Background()
	cs := fake.NewClientset()
	labels := workloadLabels("rs-1")
	cs.CoreV1().Services("kyq-npm").Create(ctx, &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: appName, Namespace: "kyq-npm", Labels: labels},
		Spec:       corev1.ServiceSpec{Selector: selector("npm"), Ports: []corev1.ServicePort{{Port: 81, TargetPort: intstr.FromInt32(81)}}},
	}, metav1.CreateOptions{})
	cs.CoreV1().Pods("kyq-npm").Create(ctx, &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "app-1", Namespace: "kyq-npm", Labels: podLabels("npm", "rs-1")},
		Status:     corev1.PodStatus{Phase: corev1.PodRunning, Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}},
	}, metav1.CreateOptions{})
	cs.CoreV1().Secrets("kyq-npm").Create(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: "kyq-npm", Labels: labels},
		Data:       map[string][]byte{npmPasswordKey: []byte(npmPassword)},
	}, metav1.CreateOptions{})
	c := New(cs)
	c.portForward = func(ctx context.Context, namespace, pod string, port int) (net.Conn, error) {
		if namespace != "kyq-npm" || pod != "app-1" || port != 81 {
			t.Errorf("port-forward to %s/%s:%d", namespace, pod, port)
		}
		return net.Dial("tcp", srv.Listener.Addr().String())
	}
	return App{Name: "npm", Client: c, Catalog: npmApp(t), ReleaseSet: "rs-1", Redact: &engine.Redactor{}}, cs
}

func bootstrapOf(t *testing.T, a App) npmBootstrapStep {
	t.Helper()
	steps := Steps(a)
	b, ok := steps[len(steps)-1].(npmBootstrapStep)
	if !ok || b.ID() != "npm.bootstrap" {
		t.Fatalf("steps = %v", steps)
	}
	return b
}

func storedToken(t *testing.T, cs *fake.Clientset) string {
	t.Helper()
	ctx := context.Background()
	ns, err := cs.CoreV1().Namespaces().Get(ctx, edgeNamespace, metav1.GetOptions{})
	if err != nil || !owned(ns) || ns.Labels[podSecurity] != "restricted" {
		t.Fatalf("edge namespace = %+v, %v", ns, err)
	}
	sec, err := cs.CoreV1().Secrets(edgeNamespace).Get(ctx, edgeSecret, metav1.GetOptions{})
	if err != nil || sec.Labels[managed.Label] != managed.Value {
		t.Fatalf("edge secret = %+v, %v", sec, err)
	}
	return string(sec.Data[edgeTokenKey])
}

func TestNPMBootstrapFreshAndRerun(t *testing.T) {
	ctx := context.Background()
	f := &fakeNPM{users: map[string]string{}, tokens: map[string]string{}}
	a, cs := npmFixture(t, f)
	b := bootstrapOf(t, a)
	if done, err := b.Inspect(ctx); err != nil || done {
		t.Fatalf("Inspect before = %v %v", done, err)
	}
	if err := b.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	if err := b.Verify(ctx); err != nil {
		t.Fatal(err)
	}
	token := storedToken(t, cs)
	if len(f.users) != 1 || !f.login(npmEmail, npmPassword) || f.login(edge.DefaultEmail, edge.DefaultPassword) || f.tokens[token] != npmEmail {
		t.Fatalf("users = %v, token %q", f.users, token)
	}
	if a.Redact.Redact(npmPassword+" "+token) != "[redacted] [redacted]" {
		t.Fatal("password or token not registered for redaction")
	}
	// Rerun: done, and a forced Apply creates no second user and keeps a working token.
	if done, err := b.Inspect(ctx); err != nil || !done {
		t.Fatalf("rerun Inspect = %v %v", done, err)
	}
	if err := b.Apply(ctx); err != nil || len(f.users) != 1 {
		t.Fatalf("forced rerun: %v, users %v", err, f.users)
	}
	if err := b.Verify(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestNPMBootstrapRotatesDefaultLogin(t *testing.T) {
	ctx := context.Background()
	f := &fakeNPM{users: map[string]string{edge.DefaultEmail: edge.DefaultPassword}, tokens: map[string]string{}}
	a, cs := npmFixture(t, f)
	b := bootstrapOf(t, a)
	if err := b.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	if f.login(edge.DefaultEmail, edge.DefaultPassword) || !f.login(npmEmail, npmPassword) || len(f.users) != 1 {
		t.Fatalf("default login survived: %v", f.users)
	}
	if f.tokens[storedToken(t, cs)] != npmEmail {
		t.Fatal("stored token is not the installer's")
	}
	if err := b.Verify(ctx); err != nil {
		t.Fatal(err)
	}
	// A default login that reappears beside the installer's is not done, and is refused.
	f.users[edge.DefaultEmail] = edge.DefaultPassword
	if done, err := b.Inspect(ctx); err != nil || done {
		t.Fatalf("default login beside the installer's counted as done: %v %v", done, err)
	}
	if err := b.Apply(ctx); err == nil || !strings.Contains(err.Error(), "another administrator") {
		t.Fatalf("second default administrator: %v", err)
	}
}

func TestNPMBootstrapRefreshesRejectedToken(t *testing.T) {
	ctx := context.Background()
	f := &fakeNPM{users: map[string]string{}, tokens: map[string]string{}}
	a, cs := npmFixture(t, f)
	b := bootstrapOf(t, a)
	if err := b.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	old := storedToken(t, cs)
	f.expire(npmEmail) // expired, or revoked by a password change
	if done, err := b.Inspect(ctx); err != nil || done {
		t.Fatalf("expired token counted as done: %v %v", done, err)
	}
	// Reads between applies mint a token in memory and store nothing.
	token, dial, err := a.Client.NPMAccess(ctx, "kyq-npm", appName, 81)
	if err != nil || dial == nil || string(token) == old || f.tokens[string(token)] != npmEmail || storedToken(t, cs) != old {
		t.Fatalf("NPMAccess = %q, %v", token, err)
	}
	if err := b.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	if fresh := storedToken(t, cs); fresh == old || f.tokens[fresh] != npmEmail {
		t.Fatalf("token not refreshed: %q", fresh)
	}
	if done, err := b.Inspect(ctx); err != nil || !done {
		t.Fatalf("after refresh = %v %v", done, err)
	}
	stored, _, err := a.Client.NPMAccess(ctx, "kyq-npm", appName, 81)
	if err != nil || string(stored) != storedToken(t, cs) {
		t.Fatalf("NPMAccess did not reuse the stored token: %v", err)
	}
	for _, bad := range [][3]any{{"kyq-other", appName, 81}, {"kyq-npm", "other", 81}, {"kyq-npm", appName, 80}} {
		if _, _, err := a.Client.NPMAccess(ctx, bad[0].(string), bad[1].(string), bad[2].(int)); err == nil {
			t.Errorf("NPMAccess %v accepted", bad)
		}
	}
}

func TestNPMBootstrapErrorsHoldNoSecret(t *testing.T) {
	ctx := context.Background()
	f := &fakeNPM{users: map[string]string{"someone@example.test": "theirs"}, tokens: map[string]string{}}
	a, _ := npmFixture(t, f)
	b := bootstrapOf(t, a)
	if err := b.Apply(ctx); err == nil || !strings.Contains(err.Error(), "reset its administrator by hand") {
		t.Fatalf("foreign administrator: %v", err)
	}
	f.failAll = true
	err := b.Apply(ctx)
	if err == nil || strings.Contains(err.Error(), npmPassword) {
		t.Fatalf("error = %v", err)
	}
}

func TestServiceDialerPicksReadyOwnedPod(t *testing.T) {
	ctx := context.Background()
	a, cs := npmFixture(t, &fakeNPM{users: map[string]string{}, tokens: map[string]string{}})
	dial := a.Client.ServiceDialer("kyq-npm", appName, 81)
	if _, err := a.Client.ServiceDialer("kyq-npm", appName, 82)(ctx, "tcp", "x"); err == nil {
		t.Fatal("dialled a port the Service lacks")
	}
	pod, _ := cs.CoreV1().Pods("kyq-npm").Get(ctx, "app-1", metav1.GetOptions{})
	pod.Status.Conditions = nil
	cs.CoreV1().Pods("kyq-npm").Update(ctx, pod, metav1.UpdateOptions{})
	if _, err := dial(ctx, "tcp", "x"); err == nil || !strings.Contains(err.Error(), "no ready pod") {
		t.Fatalf("unready pod: %v", err)
	}
	svc, _ := cs.CoreV1().Services("kyq-npm").Get(ctx, appName, metav1.GetOptions{})
	svc.Labels = nil
	cs.CoreV1().Services("kyq-npm").Update(ctx, svc, metav1.UpdateOptions{})
	if _, err := dial(ctx, "tcp", "x"); err == nil || !strings.Contains(err.Error(), "not managed") {
		t.Fatalf("unowned service: %v", err)
	}
}
