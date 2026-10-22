package kube

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/utils/ptr"
)

// ErrEgressUnenforced: the cluster's network plugin let a pod under an egress policy reach
// what that policy denies.
var ErrEgressUnenforced = errors.New("egress_unenforced")

// egressProbe resolves the API server's Service through cluster DNS, which the policy admits,
// and connects to it, which the policy denies. It prints dns, open or blocked.
const egressProbe = `out=$(nslookup -type=a kubernetes.default.svc.cluster.local 2>/dev/null) || { echo dns; exit 0; }
ip=$(echo "$out" | awk '/^Name:/ {n=1} n && /^Address/ {print $NF; exit}')
[ -n "$ip" ] || { echo dns; exit 0; }
if nc -z -w 3 "$ip" 443; then echo open; else echo blocked; fi`

// egressRetry spaces the probes; tests shorten it.
var egressRetry = 2 * time.Second

func egressPod(namespace, release string) *corev1.Pod {
	p := credentialPod(namespace, release)
	p.GenerateName = "kyq-egress-check-"
	p.Spec.ActiveDeadlineSeconds = ptr.To(int64(120))
	p.Spec.Volumes = nil
	c := &p.Spec.Containers[0]
	c.Name, c.Command, c.VolumeMounts = "check", []string{"/bin/sleep", "120"}, nil
	r := corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m"), corev1.ResourceMemory: resource.MustParse("16Mi")}
	c.Resources = corev1.ResourceRequirements{Requests: r, Limits: r}
	return p
}

// proveEgress fails closed unless a pod in namespace, under the egress policy already written
// there, resolves through cluster DNS and cannot connect to the API server. A plugin that
// ignores egress policies fails it. The policy may take a moment to apply, so an open result
// is retried a few times before it counts.
func (c *Client) proveEgress(ctx context.Context, namespace, release string) error {
	var last string
	err := c.podSession(ctx, egressPod(namespace, release), "egress check", func(name string) error {
		for try := 0; try < 5; try++ {
			if try > 0 {
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(egressRetry):
				}
			}
			out, err := c.Exec(ctx, namespace, name, "check", []string{"/bin/sh", "-c", egressProbe})
			if err != nil {
				return errors.New("egress check could not run")
			}
			if last = strings.TrimSpace(string(out)); last == "blocked" {
				return nil
			}
		}
		return nil
	})
	switch {
	case err != nil:
		return err
	case last == "blocked":
		return nil
	case last == "dns":
		return fmt.Errorf("%w: a pod under the egress policy could not resolve cluster DNS, so enforcement was not demonstrated; the app was not deployed", ErrEgressUnenforced)
	}
	return fmt.Errorf("%w: a pod under the egress policy reached the API server, so this cluster's network plugin does not enforce NetworkPolicy egress; the app was not deployed. Use a plugin that enforces it", ErrEgressUnenforced)
}
