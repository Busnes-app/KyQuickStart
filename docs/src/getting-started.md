# Getting started

## Prepare targets

Install a current release binary, or use the Go version required by `go.mod` and run `make build` for source evaluation. KyYard on Kubernetes requires the release binary's pinned runner image; source builds refuse that path. Docker targets need SSH agent access and verified host keys. Kubernetes targets need an existing kubeconfig and suitable permissions.

## Make a plan

Run `./kyquickstart` for guided setup, or use the documented `plan` command. Select products and targets and inspect every required component. Choosing finish saves the plan without changing a server. The catalog includes the nine Ky products and optional Euro-Office, with placement restrictions: KyDrive, KyPost and KyDNS are Docker only; Euro-Office is Kubernetes only. Read the guided setup for the chosen combination. Demo mode is a disposable rehearsal.

Review the saved plan with `review`, run `preflight`, and resolve its findings before `apply`. Treat the current README/status as authoritative about supported products; do not infer support from a design document.
