package kube

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"

	"github.com/Busnes-app/kyquickstart/internal/engine"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// secretsStep keeps one Secret per app holding every declared secret. Values are created once
// and never replaced; a key added to the catalog later is generated on its own.
type secretsStep struct{ a App }

func (s secretsStep) ID() string        { return s.a.Name + ".secrets" }
func (s secretsStep) InputHash() string { return hashOf(s.a.Catalog.Secrets...) }

func (s secretsStep) read(ctx context.Context) (*corev1.Secret, bool, error) {
	return getOwned(ctx, s.a.Client.cs.CoreV1().Secrets(namespaceOf(s.a.Name)), "secret", secretName)
}

func (s secretsStep) missing(sec *corev1.Secret) []string {
	var out []string
	for _, n := range s.a.Catalog.Secrets {
		if sec == nil || len(sec.Data[n]) == 0 {
			out = append(out, n)
		}
	}
	return out
}

func (s secretsStep) Inspect(ctx context.Context) (bool, error) {
	sec, _, err := s.read(ctx)
	if err != nil {
		return false, err
	}
	return len(s.missing(sec)) == 0, nil
}

func (s secretsStep) Apply(ctx context.Context) error {
	if err := s.a.ensureNamespace(ctx); err != nil {
		return err
	}
	sec, found, err := s.read(ctx)
	if err != nil {
		return err
	}
	if !found {
		sec = &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: namespaceOf(s.a.Name)},
			Type:       corev1.SecretTypeOpaque,
		}
	}
	sec.Labels = workloadLabels(s.a.ReleaseSet)
	if sec.Data == nil {
		sec.Data = map[string][]byte{}
	}
	for _, n := range s.missing(sec) {
		b := make([]byte, 32)
		rand.Read(b)
		v := base64.RawURLEncoding.EncodeToString(b)
		s.a.Redact.Add(v)
		sec.Data[n] = []byte(v)
	}
	// Written against the resourceVersion just read: a concurrent writer is a conflict, retried
	// by the engine after a fresh Inspect, never silently overwritten.
	api := s.a.Client.cs.CoreV1().Secrets(sec.Namespace)
	if found {
		_, err = api.Update(ctx, sec, metav1.UpdateOptions{})
	} else {
		_, err = api.Create(ctx, sec, metav1.CreateOptions{})
	}
	switch {
	case apierrors.IsConflict(err) || apierrors.IsAlreadyExists(err):
		return fmt.Errorf("secret %s: %w: %w", secretName, engine.ErrTransient, err)
	case err != nil:
		return fmt.Errorf("secret %s: %w", secretName, err)
	}
	return nil
}

// Verify reads every secret back, which also registers it for redaction on re-runs.
func (s secretsStep) Verify(ctx context.Context) error {
	sec, _, err := s.read(ctx)
	if err != nil {
		return err
	}
	if m := s.missing(sec); len(m) > 0 {
		return fmt.Errorf("secrets missing: %v", m)
	}
	for _, n := range s.a.Catalog.Secrets {
		if len(sec.Data[n]) != 43 {
			return fmt.Errorf("secret %s is %d bytes, want 43", n, len(sec.Data[n]))
		}
		s.a.Redact.Add(string(sec.Data[n]))
	}
	return nil
}
