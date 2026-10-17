package kube

import (
	"bytes"
	"context"
	"errors"
	"io"

	corev1 "k8s.io/api/core/v1"
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
