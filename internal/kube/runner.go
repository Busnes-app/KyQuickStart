package kube

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/Busnes-app/kyquickstart/internal/managed"
	authorizationv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"k8s.io/client-go/tools/remotecommand"
	"k8s.io/utils/ptr"
)

// runnerName names the suite runner's ServiceAccount, roles and bindings; the token Secret
// records its runner in runnerAnnotation, as plan markers do.
const (
	runnerName  = "kyquickstart-runner"
	runnerToken = runnerName + "-token"
	// runnerContainer is the runner pod's container that runs kyquickstart.
	runnerContainer = "runner"
	inClusterAPI    = "kubernetes.default.svc"
	maxRunnerOutput = 1 << 20 // serve-export's plan limit
)

// ParseRunnerPod splits handoff's --runner-pod NAMESPACE/LABEL_SELECTOR.
func ParseRunnerPod(s string) (namespace, selector string, err error) {
	namespace, selector, _ = strings.Cut(s, "/")
	if len(validation.IsDNS1123Label(namespace)) > 0 || strings.TrimSpace(selector) == "" {
		return "", "", fmt.Errorf("--runner-pod %q is not NAMESPACE/LABEL_SELECTOR", s)
	}
	if _, err := labels.Parse(selector); err != nil {
		return "", "", fmt.Errorf("--runner-pod %q: %w", s, err)
	}
	return namespace, selector, nil
}

// ExecRunner runs args in the runner container of the one Running pod in namespace that
// selector matches, streaming stdin, and returns its stdout. Its errors hold the command's
// stderr: redact them before showing them. The permissions it needs are checked first, so a
// refusal changes nothing.
func (c *Client) ExecRunner(ctx context.Context, namespace, selector string, args []string, stdin io.Reader) ([]byte, error) {
	denied, err := c.denied(ctx, []authorizationv1.ResourceAttributes{
		{Resource: "pods", Namespace: namespace, Verb: "list"},
		{Resource: "pods", Subresource: "exec", Namespace: namespace, Verb: "create"},
	})
	if err != nil {
		return nil, err
	}
	if len(denied) > 0 {
		return nil, fmt.Errorf("this kubeconfig may not reach the runner in namespace %s: denied %s", namespace, strings.Join(denied, ", "))
	}
	list, err := c.cs.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return nil, err
	}
	var pods []corev1.Pod
	for _, p := range list.Items {
		if p.Status.Phase == corev1.PodRunning && p.DeletionTimestamp == nil {
			pods = append(pods, p)
		}
	}
	if len(pods) != 1 {
		return nil, fmt.Errorf("%d running pods in namespace %s match %s; the runner must be exactly one", len(pods), namespace, selector)
	}
	pod := pods[0]
	if !slices.ContainsFunc(pod.Spec.Containers, func(k corev1.Container) bool { return k.Name == runnerContainer }) {
		return nil, fmt.Errorf("pod %s/%s has no container %q", namespace, pod.Name, runnerContainer)
	}
	out, stderr := &boundedExecOutput{limit: maxRunnerOutput}, &boundedExecOutput{limit: 4 << 10}
	err = c.stream(ctx, namespace, pod.Name, runnerContainer, args, remotecommand.StreamOptions{Stdin: stdin, Stdout: out, Stderr: stderr})
	if err != nil {
		return nil, fmt.Errorf("pod %s/%s: %w: %s", namespace, pod.Name, err, bytes.TrimSpace(stderr.Bytes()))
	}
	return out.Bytes(), nil
}

// RunnerRules is what an install needs (required), as RBAC rules: cluster-wide ones, and the
// lock namespace's own.
func RunnerRules() (cluster, lock []rbacv1.PolicyRule) {
	for _, a := range required {
		rules := &cluster
		if a.Namespace != "" {
			rules = &lock
		}
		i := slices.IndexFunc(*rules, func(r rbacv1.PolicyRule) bool {
			return r.APIGroups[0] == a.Group && r.Resources[0] == a.Resource
		})
		if i < 0 {
			*rules = append(*rules, rbacv1.PolicyRule{APIGroups: []string{a.Group}, Resources: []string{a.Resource}})
			i = len(*rules) - 1
		}
		(*rules)[i].Verbs = append((*rules)[i].Verbs, a.Verb)
	}
	return cluster, lock
}

func runnerMeta(ns string) metav1.ObjectMeta {
	return metav1.ObjectMeta{Name: runnerName, Namespace: ns, Labels: map[string]string{managed.Label: managed.Value}}
}

// GrantRunner gives runner runnerID a ServiceAccount bound to RunnerRules and returns a
// kubeconfig holding its token. A token issued to another runner is replaced, so it stops
// working. The lock namespace exists: the caller holds the lock. A runner in this cluster
// (inCluster) reaches the API server at its in-cluster address, others at the workstation's.
func (c *Client) GrantRunner(ctx context.Context, runnerID string, inCluster bool) ([]byte, error) {
	if c.config == nil || c.config.Insecure {
		return nil, errors.New("the cluster connection must verify the API server's certificate")
	}
	cluster, lock := RunnerRules()
	subject := []rbacv1.Subject{{Kind: rbacv1.ServiceAccountKind, Name: runnerName, Namespace: lockNamespace}}
	rbac := c.cs.RbacV1()
	steps := []func() error{
		func() error {
			return upsert(ctx, c.cs.CoreV1().ServiceAccounts(lockNamespace), "service account", &corev1.ServiceAccount{ObjectMeta: runnerMeta(lockNamespace), AutomountServiceAccountToken: ptr.To(false)}, nil)
		},
		func() error {
			return upsert(ctx, rbac.ClusterRoles(), "cluster role", &rbacv1.ClusterRole{ObjectMeta: runnerMeta(""), Rules: cluster}, nil)
		},
		func() error {
			return upsert(ctx, rbac.ClusterRoleBindings(), "cluster role binding", &rbacv1.ClusterRoleBinding{ObjectMeta: runnerMeta(""), Subjects: subject,
				RoleRef: rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: runnerName}}, nil)
		},
		func() error {
			return upsert(ctx, rbac.Roles(lockNamespace), "role", &rbacv1.Role{ObjectMeta: runnerMeta(lockNamespace), Rules: lock}, nil)
		},
		func() error {
			return upsert(ctx, rbac.RoleBindings(lockNamespace), "role binding", &rbacv1.RoleBinding{ObjectMeta: runnerMeta(lockNamespace), Subjects: subject,
				RoleRef: rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: runnerName}}, nil)
		},
	}
	for _, step := range steps {
		if err := step(); err != nil {
			return nil, fmt.Errorf("runner access: %w", err)
		}
	}
	token, ca, err := c.runnerToken(ctx, runnerID)
	if err != nil {
		return nil, fmt.Errorf("runner token: %w", err)
	}
	server := &clientcmdapi.Cluster{Server: c.config.Host, TLSServerName: c.config.TLSClientConfig.ServerName, CertificateAuthorityData: ca}
	if inCluster {
		server.Server, server.TLSServerName = "https://"+inClusterAPI, inClusterAPI
	}
	cfg := clientcmdapi.NewConfig()
	cfg.Clusters["cluster"] = server
	cfg.AuthInfos[runnerName] = &clientcmdapi.AuthInfo{Token: string(token)}
	cfg.Contexts[runnerName] = &clientcmdapi.Context{Cluster: "cluster", AuthInfo: runnerName}
	cfg.CurrentContext = runnerName
	return clientcmd.Write(*cfg)
}

// runnerToken returns the token Secret's token and cluster CA, creating the Secret for
// runnerID, or replacing one created for another runner, and waiting for the token.
func (c *Client) runnerToken(ctx context.Context, runnerID string) (token, ca []byte, err error) {
	api := c.cs.CoreV1().Secrets(lockNamespace)
	s, found, err := getOwned(ctx, api, "secret", runnerToken)
	if err != nil {
		return nil, nil, err
	}
	if found && s.Annotations[runnerAnnotation] != runnerID {
		if err := deleteByUID(ctx, api, s); err != nil {
			return nil, nil, err
		}
		found = false
	}
	if !found {
		meta := runnerMeta(lockNamespace)
		meta.Name = runnerToken
		meta.Annotations = map[string]string{corev1.ServiceAccountNameKey: runnerName, runnerAnnotation: runnerID}
		if _, err := api.Create(ctx, &corev1.Secret{ObjectMeta: meta, Type: corev1.SecretTypeServiceAccountToken}, metav1.CreateOptions{}); err != nil {
			return nil, nil, err
		}
	}
	// The token controller fills the Secret in shortly after it is created.
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	for {
		s, err := api.Get(ctx, runnerToken, metav1.GetOptions{})
		if err != nil {
			return nil, nil, err
		}
		if len(s.Data[corev1.ServiceAccountTokenKey]) > 0 && len(s.Data[corev1.ServiceAccountRootCAKey]) > 0 {
			return s.Data[corev1.ServiceAccountTokenKey], s.Data[corev1.ServiceAccountRootCAKey], nil
		}
		select {
		case <-ctx.Done():
			return nil, nil, errors.New("the cluster issued no token; is its token controller running?")
		case <-time.After(500 * time.Millisecond):
		}
	}
}

type deleter[T metav1.Object] interface {
	getter[T]
	Delete(ctx context.Context, name string, opts metav1.DeleteOptions) error
}

func deleteByUID[T metav1.Object](ctx context.Context, api deleter[T], o T) error {
	uid := o.GetUID()
	err := api.Delete(ctx, o.GetName(), metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid}})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete %s: %w", o.GetName(), err)
	}
	return nil
}

// removeOwned deletes name if the installer created it; absent is done.
func removeOwned[T metav1.Object](ctx context.Context, api deleter[T], kind, name string) error {
	o, found, err := getOwned(ctx, api, kind, name)
	if err != nil || !found {
		return err
	}
	return deleteByUID(ctx, api, o)
}

// RevokeRunner removes everything GrantRunner created; the token stops working with it.
func (c *Client) RevokeRunner(ctx context.Context) error {
	rbac := c.cs.RbacV1()
	return errors.Join(
		removeOwned(ctx, c.cs.CoreV1().Secrets(lockNamespace), "secret", runnerToken),
		removeOwned(ctx, rbac.RoleBindings(lockNamespace), "role binding", runnerName),
		removeOwned(ctx, rbac.Roles(lockNamespace), "role", runnerName),
		removeOwned(ctx, rbac.ClusterRoleBindings(), "cluster role binding", runnerName),
		removeOwned(ctx, rbac.ClusterRoles(), "cluster role", runnerName),
		removeOwned(ctx, c.cs.CoreV1().ServiceAccounts(lockNamespace), "service account", runnerName),
	)
}
