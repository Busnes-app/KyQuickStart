package kube

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/utils/ptr"
)

// CredentialGeneratorImage pins the Docker Official Image BusyBox 1.37.0-musl
// to an immutable linux/amd64 manifest.
// Provenance and the update procedure are documented in docs/guided-setup.md.
const CredentialGeneratorImage = "busybox:1.37.0-musl@sha256:dc88b80842580654294472735332ec7c7789a4010ec9a68b69f9b88c19abede6"

const credentialCommand = `umask 077
raw=$(mktemp /tmp/random.XXXXXX) || exit 1
encoded=
trap 'rm -f "$raw"; [ -z "$encoded" ] || rm -f "$encoded"' 0
trap 'exit 1' HUP INT TERM
encoded=$(mktemp /tmp/encoded.XXXXXX) || exit 1
dd if=/dev/urandom of="$raw" bs=32 count=1 2>/dev/null || exit 1
[ "$(wc -c < "$raw")" -eq 32 ] || exit 1
base64 < "$raw" > "$encoded" || exit 1
tr '+/' '-_' < "$encoded" > "$raw" || exit 1
tr -d '=\n' < "$raw" > "$encoded" || exit 1
[ "$(wc -c < "$encoded")" -eq 43 ] || exit 1
cat "$encoded"`

func credentialPod(namespace, release string) *corev1.Pod {
	resources := corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m"), corev1.ResourceMemory: resource.MustParse("32Mi")}
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{GenerateName: "kyq-credentials-", Namespace: namespace, Labels: workloadLabels(release)},
		Spec: corev1.PodSpec{
			RestartPolicy:                 corev1.RestartPolicyNever,
			ActiveDeadlineSeconds:         ptr.To(int64(180)),
			TerminationGracePeriodSeconds: ptr.To(int64(0)),
			AutomountServiceAccountToken:  ptr.To(false), EnableServiceLinks: ptr.To(false),
			SecurityContext: &corev1.PodSecurityContext{RunAsNonRoot: ptr.To(true), RunAsUser: ptr.To(int64(65532)), RunAsGroup: ptr.To(int64(65532)), FSGroup: ptr.To(int64(65532)), SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}},
			Containers: []corev1.Container{{Name: "generator", Image: CredentialGeneratorImage, ImagePullPolicy: corev1.PullIfNotPresent, Command: []string{"/bin/sleep", "180"},
				Resources:       corev1.ResourceRequirements{Requests: resources, Limits: resources},
				SecurityContext: &corev1.SecurityContext{AllowPrivilegeEscalation: ptr.To(false), ReadOnlyRootFilesystem: ptr.To(true), Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}},
				VolumeMounts:    []corev1.VolumeMount{{Name: "scratch", MountPath: "/tmp"}},
			}},
			Volumes: []corev1.Volume{{Name: "scratch", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{Medium: corev1.StorageMediumMemory, SizeLimit: ptr.To(resource.MustParse("1Mi"))}}}},
		},
	}
}

// generateCredentials creates fresh values inside a unique target-cluster pod.
// Exec output is not a container log. No service token, app Secret or persistent
// volume enters the pod; the workstation only receives the bounded result in memory.
func (c *Client) generateCredentials(ctx context.Context, namespace, release string, count int) (values [][]byte, resultErr error) {
	err := c.podSession(ctx, credentialPod(namespace, release), "credential generator", func(name string) error {
		for i := 0; i < count; i++ {
			output, err := c.Exec(ctx, namespace, name, "generator", []string{"/bin/sh", "-c", credentialCommand})
			if err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				return errors.New("cluster-local credential generation failed")
			}
			if len(output) != 43 {
				return errors.New("credential generator returned an invalid value")
			}
			if _, err := base64.RawURLEncoding.Strict().DecodeString(string(output)); err != nil {
				return errors.New("credential generator returned malformed encoding")
			}
			values = append(values, output)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return values, nil
}

// podSession creates pod, waits until it is ready, runs use with its name, then deletes it by
// UID and waits until it is gone. A pod it cannot remove fails the session; its
// ActiveDeadlineSeconds bounds its life.
func (c *Client) podSession(ctx context.Context, pod *corev1.Pod, what string, use func(name string) error) (resultErr error) {
	pods := c.cs.CoreV1().Pods(pod.Namespace)
	pod, err := pods.Create(ctx, pod, metav1.CreateOptions{})
	if err != nil {
		return errors.New("cluster refused the " + what + " pod")
	}
	if pod.Name == "" || pod.UID == "" {
		return errors.New(what + " has no server-issued identity")
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		uid := pod.UID
		err := pods.Delete(cleanup, pod.Name, metav1.DeleteOptions{GracePeriodSeconds: ptr.To(int64(0)), Preconditions: &metav1.Preconditions{UID: &uid}})
		if err == nil || apierrors.IsNotFound(err) {
			err = wait.PollUntilContextCancel(cleanup, 100*time.Millisecond, true, func(ctx context.Context) (bool, error) {
				current, err := pods.Get(ctx, pod.Name, metav1.GetOptions{})
				if apierrors.IsNotFound(err) {
					return true, nil
				}
				if err != nil {
					return false, errors.New("cannot verify " + what + " removal")
				}
				return current.UID != uid, nil
			})
		}
		if err != nil {
			resultErr = fmt.Errorf("%s cleanup failed; its lifetime is capped at %d seconds", what, *pod.Spec.ActiveDeadlineSeconds)
		}
	}()
	ready, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	err = wait.PollUntilContextCancel(ready, 100*time.Millisecond, true, func(ctx context.Context) (bool, error) {
		current, err := pods.Get(ctx, pod.Name, metav1.GetOptions{})
		if err != nil {
			return false, errors.New("cannot inspect " + what)
		}
		if current.UID != pod.UID || !owned(current) {
			return false, errors.New(what + " ownership changed")
		}
		if current.Status.Phase == corev1.PodFailed || current.Status.Phase == corev1.PodSucceeded {
			return false, errors.New(what + " stopped before execution")
		}
		for _, condition := range current.Status.Conditions {
			if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue {
				return true, nil
			}
		}
		return false, nil
	})
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errors.New(what + " did not become ready")
	}
	return use(pod.Name)
}
