package kube

import (
	"bytes"
	"context"
	"testing"

	"k8s.io/client-go/kubernetes/fake"
)

func TestExecOutputIsBounded(t *testing.T) {
	b := boundedExecOutput{limit: 128 * 1024}
	if _, err := b.Write(bytes.Repeat([]byte("a"), 128*1024)); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Write([]byte("extra")); err == nil || b.Len() != 128*1024 {
		t.Fatal("unbounded exec stdout")
	}
	if _, err := New(fake.NewClientset()).Exec(context.Background(), "namespace", "pod", "container", []string{"command"}); err == nil {
		t.Fatal("fake client executed")
	}
}

func TestExecInputIsBoundedBeforeConnection(t *testing.T) {
	c := New(fake.NewClientset())
	if _, err := c.ExecInput(context.Background(), "namespace", "pod", "container", []string{"command"}, bytes.Repeat([]byte("a"), (128<<10)+1)); err == nil || err.Error() != "product command input exceeds limit" {
		t.Fatal("unbounded input admitted", err)
	}
	if _, err := c.ExecInput(context.Background(), "namespace", "pod", "container", []string{"command"}, []byte("bounded")); err == nil {
		t.Fatal("fake client executed stdin")
	}
}
