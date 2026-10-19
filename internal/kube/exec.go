package kube

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"slices"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
)

var errNotConnected = errors.New("container exec requires a connected Kubernetes client")

// Exec invokes a container-local product CLI; it never changes workload ownership.
func (c *Client) Exec(ctx context.Context, namespace, pod, container string, args []string) ([]byte, error) {
	return c.exec(ctx, namespace, pod, container, args, nil)
}

// ExecReport is Exec for a command whose stdout reports its result even when it exits
// non-zero; that output is returned with the failure, bounded.
func (c *Client) ExecReport(ctx context.Context, namespace, pod, container string, args []string) ([]byte, error) {
	if c.execCommand != nil {
		return c.execCommand(ctx, namespace, pod, container, args)
	}
	out := &boundedExecOutput{limit: 128 << 10}
	if err := c.stream(ctx, namespace, pod, container, args, remotecommand.StreamOptions{Stdout: out, Stderr: io.Discard}); err != nil {
		if errors.Is(err, errNotConnected) {
			return nil, err
		}
		return out.Bytes(), errors.New("Kubernetes product command failed; inspect product logs locally")
	}
	return out.Bytes(), nil
}

// ExecInput transfers bounded product setup input directly to container stdin.
// It never writes an input file on the workstation or includes the body in argv.
func (c *Client) ExecInput(ctx context.Context, namespace, pod, container string, args []string, input []byte) ([]byte, error) {
	if len(input) > 128<<10 {
		return nil, errors.New("product command input exceeds limit")
	}
	return c.exec(ctx, namespace, pod, container, args, input)
}

func (c *Client) exec(ctx context.Context, namespace, pod, container string, args []string, input []byte) ([]byte, error) {
	if c.execCommand != nil && input == nil {
		return c.execCommand(ctx, namespace, pod, container, args)
	}
	out := &boundedExecOutput{limit: 128 << 10}
	var stdin io.Reader
	if len(input) > 0 {
		stdin = bytes.NewReader(input)
	}
	if err := c.stream(ctx, namespace, pod, container, args, remotecommand.StreamOptions{Stdin: stdin, Stdout: out, Stderr: io.Discard}); err != nil {
		if errors.Is(err, errNotConnected) {
			return nil, err
		}
		return nil, errors.New("Kubernetes product command failed; inspect product logs locally")
	}
	return out.Bytes(), nil
}

// stream runs args in a container over pods/exec, without a TTY. Tests replace it with
// execStream.
func (c *Client) stream(ctx context.Context, namespace, pod, container string, args []string, o remotecommand.StreamOptions) error {
	if c.execStream != nil {
		return c.execStream(ctx, namespace, pod, container, args, o)
	}
	if c.config == nil {
		return errNotConnected
	}
	req := c.cs.CoreV1().RESTClient().Post().Resource("pods").Name(pod).Namespace(namespace).SubResource("exec").VersionedParams(&corev1.PodExecOptions{Container: container, Command: args, Stdin: o.Stdin != nil, Stdout: true, Stderr: true}, scheme.ParameterCodec)
	executor, err := remotecommand.NewSPDYExecutor(c.config, "POST", req.URL())
	if err != nil {
		return err
	}
	return executor.StreamWithContext(ctx, o)
}

// AppPod names the one ready, installer-managed pod of a catalog app and its container.
func (c *Client) AppPod(ctx context.Context, app string) (namespace, pod, container string, err error) {
	ns := namespaceOf(app)
	list, err := c.cs.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{LabelSelector: labels.SelectorFromSet(selector(app)).String()})
	if err != nil {
		return "", "", "", fmt.Errorf("pods of %s: %w", app, err)
	}
	var ready []string
	for _, p := range list.Items {
		if owned(&p) && p.DeletionTimestamp == nil && p.Status.Phase == corev1.PodRunning && slices.ContainsFunc(p.Status.Conditions, func(c corev1.PodCondition) bool {
			return c.Type == corev1.PodReady && c.Status == corev1.ConditionTrue
		}) {
			ready = append(ready, p.Name)
		}
	}
	if len(ready) != 1 {
		return "", "", "", fmt.Errorf("%s has %d ready pods, want one", app, len(ready))
	}
	return ns, ready[0], appName, nil
}

type boundedExecOutput struct {
	bytes.Buffer
	limit int
}

func (b *boundedExecOutput) Write(p []byte) (int, error) {
	if b.Len()+len(p) > b.limit {
		return 0, errors.New("command output exceeds limit")
	}
	return b.Buffer.Write(p)
}

// Fake returns a client of cs that verifies host's certificate and sends every pods/exec to
// stream. For tests outside this package.
func Fake(cs kubernetes.Interface, host string, stream func(ctx context.Context, namespace, pod, container string, args []string, o remotecommand.StreamOptions) error) *Client {
	return &Client{cs: cs, config: &rest.Config{Host: host}, execStream: stream}
}
