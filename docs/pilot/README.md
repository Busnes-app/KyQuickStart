# Pilot cluster K80

`k80.urlxl.us` (kubeadm 1.37, nodes k80 control plane and k81 worker). It also runs a hand-built
`ky-stack`; KyQuickStart installs alongside it in its own `kyq-*` namespaces and never adopts
`ky-stack` objects. Migrating `ky-stack` is a separate, later step.

K80 has no default StorageClass, by choice. `k80-local-path.yaml` is
[local-path-provisioner](https://github.com/rancher/local-path-provisioner) v0.0.37 as applied on
2026-10-17, changed from upstream only to pin both images by digest and to `reclaimPolicy: Retain`,
so deleting a claim or namespace never deletes app data. Its `local-path` class is not the
default; stack targets on K80 name it with `storage_class: local-path`. Volumes live under
`/opt/local-path-provisioner` on the node that runs the pod. Remove with
`kubectl delete -f k80-local-path.yaml` (retained volumes and their directories stay).
