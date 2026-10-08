package kube

import (
	"context"
	"fmt"
	"maps"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func (a App) namespaceLabels() map[string]string {
	l := workloadLabels(a.ReleaseSet)
	l[podSecurity] = "restricted"
	return l
}

func (a App) ensureNamespace(ctx context.Context) error {
	return a.Client.ensureNamespace(ctx, namespaceOf(a.Name), a.namespaceLabels())
}

// ensureNamespace creates name with labels, or brings the installer's existing namespace up to
// them. A namespace the installer did not create is refused.
func (c *Client) ensureNamespace(ctx context.Context, name string, labels map[string]string) error {
	api := c.cs.CoreV1().Namespaces()
	have, err := api.Get(ctx, name, metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
		_, err = api.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels}}, metav1.CreateOptions{})
		if err != nil {
			return fmt.Errorf("namespace %s: %w", name, err)
		}
		return nil
	case err != nil:
		return fmt.Errorf("namespace %s: %w", name, err)
	case !owned(have):
		return fmt.Errorf("namespace %s exists and is not managed by kyquickstart; remove or rename it by hand", name)
	}
	if have.Labels == nil {
		have.Labels = map[string]string{}
	}
	before := maps.Clone(have.Labels)
	maps.Copy(have.Labels, labels)
	if maps.Equal(before, have.Labels) {
		return nil
	}
	if _, err := api.Update(ctx, have, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("namespace %s: %w", name, err)
	}
	return nil
}
