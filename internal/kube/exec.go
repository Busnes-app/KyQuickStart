package kube

import (
	"bytes"
	"context"
	"errors"
	"io"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/remotecommand"
)

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
	if c.config == nil {
		return nil, errors.New("container exec requires a connected Kubernetes client")
	}
	req := c.cs.CoreV1().RESTClient().Post().Resource("pods").Name(pod).Namespace(namespace).SubResource("exec").VersionedParams(&corev1.PodExecOptions{Container: container, Command: args, Stdin: len(input) > 0, Stdout: true, Stderr: true}, scheme.ParameterCodec)
	executor, err := remotecommand.NewSPDYExecutor(c.config, "POST", req.URL())
	if err != nil {
		return nil, errors.New("cannot prepare Kubernetes product command")
	}
	out := &boundedExecOutput{}
	var stdin io.Reader
	if len(input) > 0 {
		stdin = bytes.NewReader(input)
	}
	err = executor.StreamWithContext(ctx, remotecommand.StreamOptions{Stdin: stdin, Stdout: out, Stderr: io.Discard})
	if err != nil {
		return nil, errors.New("Kubernetes product command failed; inspect product logs locally")
	}
	return out.Bytes(), nil
}

type boundedExecOutput struct{ bytes.Buffer }

func (b *boundedExecOutput) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 128*1024 {
		return 0, errors.New("product command output exceeds limit")
	}
	return b.Buffer.Write(p)
}
