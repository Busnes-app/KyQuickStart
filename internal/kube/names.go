package kube

import (
	"context"
	"errors"
	"fmt"
	"maps"

	"github.com/Busnes-app/kyquickstart/internal/engine"
	"github.com/Busnes-app/kyquickstart/internal/managed"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	appName        = "app"
	secretName     = "kyq-secrets"
	policyName     = "default-deny-ingress"
	hashAnnotation = "kyquickstart/input-hash"
	podSecurity    = "pod-security.kubernetes.io/enforce"
	nameLabel      = "app.kubernetes.io/name"
)

func namespaceOf(app string) string { return "kyq-" + app }

func workloadLabels(releaseSet string) map[string]string {
	return map[string]string{managed.Label: managed.Value, managed.ReleaseSetLabel: releaseSet}
}

func podLabels(app, releaseSet string) map[string]string {
	l := workloadLabels(releaseSet)
	maps.Copy(l, selector(app))
	return l
}

func selector(app string) map[string]string { return map[string]string{nameLabel: app} }

func owned(o metav1.Object) bool { return o.GetLabels()[managed.Label] == managed.Value }

var errNotOwned = errors.New("not managed by kyquickstart")

type getter[T metav1.Object] interface {
	Get(ctx context.Context, name string, opts metav1.GetOptions) (T, error)
}

type writer[T metav1.Object] interface {
	getter[T]
	Create(ctx context.Context, o T, opts metav1.CreateOptions) (T, error)
	Update(ctx context.Context, o T, opts metav1.UpdateOptions) (T, error)
}

// getOwned reads an object; found is false when it is absent. An object of that name the
// installer did not create is an error, never something to adopt.
func getOwned[T metav1.Object](ctx context.Context, api getter[T], kind, name string) (T, bool, error) {
	var zero T
	o, err := api.Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return zero, false, nil
	}
	if err != nil {
		return zero, false, fmt.Errorf("%s %s: %w", kind, name, err)
	}
	if !owned(o) {
		return zero, false, fmt.Errorf("%s %s exists and is %w; remove or rename it by hand", kind, name, errNotOwned)
	}
	return o, true, nil
}

// upsert creates want, or replaces the installer's object of the same name with it. keep copies
// fields the API server owns from the existing object. A conflict means someone wrote in
// between: transient, so the engine inspects again.
func upsert[T metav1.Object](ctx context.Context, api writer[T], kind string, want T, keep func(have T)) error {
	have, found, err := getOwned(ctx, api, kind, want.GetName())
	if err != nil {
		return err
	}
	if found {
		want.SetResourceVersion(have.GetResourceVersion())
		if keep != nil {
			keep(have)
		}
		_, err = api.Update(ctx, want, metav1.UpdateOptions{})
	} else {
		_, err = api.Create(ctx, want, metav1.CreateOptions{})
	}
	switch {
	case apierrors.IsConflict(err) || apierrors.IsAlreadyExists(err):
		return fmt.Errorf("%s %s: %w: %w", kind, want.GetName(), engine.ErrTransient, err)
	case err != nil:
		return fmt.Errorf("%s %s: %w", kind, want.GetName(), err)
	}
	return nil
}
