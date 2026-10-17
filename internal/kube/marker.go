package kube

import (
	"context"
	"fmt"
	"strconv"

	"github.com/Busnes-app/kyquickstart/internal/stack"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	revisionAnnotation = "kyquickstart/plan-revision"
	ownerAnnotation    = "kyquickstart/plan-owner"
	runnerAnnotation   = "kyquickstart/runner-id"
	installAnnotation  = "kyquickstart/installation-id"
)

// Marker reads the plan marker from the lock namespace; the zero Marker when there is none.
func (c *Client) Marker(ctx context.Context) (stack.Marker, error) {
	ns, err := c.cs.CoreV1().Namespaces().Get(ctx, lockNamespace, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return stack.Marker{}, nil
	}
	if err != nil {
		return stack.Marker{}, fmt.Errorf("read plan marker: %w", err)
	}
	a := ns.Annotations
	v, ok := a[revisionAnnotation]
	if !ok {
		return stack.Marker{}, nil
	}
	rev, err := strconv.Atoi(v)
	if err != nil {
		return stack.Marker{}, fmt.Errorf("plan marker revision %q is not a number", v)
	}
	m := stack.Marker{Revision: rev, Owner: a[ownerAnnotation], RunnerID: a[runnerAnnotation], InstallationID: a[installAnnotation]}
	return m, m.Validate()
}

// WriteMarker records m on the lock namespace, which Acquire created.
func (c *Client) WriteMarker(ctx context.Context, m stack.Marker) error {
	api := c.cs.CoreV1().Namespaces()
	ns, found, err := getOwned(ctx, api, "namespace", lockNamespace)
	if err != nil {
		return fmt.Errorf("write plan marker: %w", err)
	}
	if !found {
		return fmt.Errorf("write plan marker: namespace %s missing", lockNamespace)
	}
	if ns.Annotations == nil {
		ns.Annotations = map[string]string{}
	}
	ns.Annotations[revisionAnnotation] = strconv.Itoa(m.Revision)
	ns.Annotations[ownerAnnotation] = m.Owner
	ns.Annotations[runnerAnnotation] = m.RunnerID
	if m.InstallationID != "" {
		ns.Annotations[installAnnotation] = m.InstallationID
	}
	if _, err := api.Update(ctx, ns, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("write plan marker: %w", err)
	}
	return nil
}
