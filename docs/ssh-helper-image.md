# ssh-helper image updates (virtbench `ssh-test-pod`)

Virtbench scenarios use a shared cluster pod `default/ssh-test-pod` for VM ping
and in-VM SSH (`sshpass` / `ssh`). The harness creates it from an embedded
YAML manifest and ships a matching example under the Virtbench runtime tree.

The helper **image** is built and published from the
[`storage-cert-harness-images`](../../storage-cert-harness-images/) GitLab project
(`quay.io/virtarraycert/ssh-helper`). The harness **pins** a digest in source;
it does not pull a floating `:latest` tag at runtime.

## When to update

- A new `ssh-helper` release was published to Quay (security fix, tool change, or
  base image rebuild).
- You validated the candidate image in the images repo (CI plus optional local
  `make test-phase3` in `storage-cert-harness-images`).

## 1. Resolve the immutable image reference

After publish, record the digest for the semver tag (multi-arch manifest tag, not
only `*-amd64`):

```sh
skopeo inspect docker://quay.io/virtarraycert/ssh-helper:<version> \
  --format '{{.Digest}}'
```

Use the full reference:

```text
quay.io/virtarraycert/ssh-helper@sha256:<digest>
```

## 2. Update harness pins (keep both in sync)

Edit **both** of these to the same `@sha256:…` reference:

| Location | What to change |
| --- | --- |
| `internal/tools/virtbench/templates/shared/ssh-pod.yaml` | `containers[].image` on `ssh-client`. |
| `container-patches/virtbench/examples/utilities/ssh-pod.yaml` | `containers[].image` on `ssh-client`. |

The embedded manifest and the runtime example must stay aligned: same image, security
context, and volume mounts. Command logging (`ssh` / `sshpass` / `ping` → pod
logs) is implemented in the **ssh-helper image** (`/opt/sshhelper/bin` in
`storage-cert-harness-images/images/ssh-helper/`), not in the pod spec. The
example is copied into the harness container at build time:

```text
container-patches/virtbench/examples/utilities/ssh-pod.yaml
  → /opt/virtbench-runtime/examples/utilities/ssh-pod.yaml
```

If you change logging behavior, edit `install-log-wrappers.sh` in the images repo,
publish a new ssh-helper release, then bump the harness digest pins.

## 3. Verify locally

```sh
cd storage-cert-harness
go test ./internal/tools/virtbench/ -count=1
make build
```

Optional cluster checks:

- Rebuild and exercise the harness image (`make image-build`, cluster smoke /
  `run-image-tests` per [`docs/ci.md`](ci.md)).
- On a cluster that already has `ssh-test-pod`, delete it so the harness recreates
  it with the new image (pre-existing pods are reused and are not upgraded in place):

  ```sh
  kubectl delete pod ssh-test-pod -n default --ignore-not-found
  ```

Confirm tools inside the running pod:

```sh
kubectl exec -n default ssh-test-pod -- /bin/bash -c 'command -v sshpass; sshpass -V'
kubectl logs ssh-test-pod -n default   # ssh/sshpass/ping lines from PATH wrappers
```

## 4. Images repo cross-check (recommended)

From `storage-cert-harness-images`, against the same digest or tag you pinned:

```sh
SSH_HELPER_TEST_IMAGE=quay.io/virtarraycert/ssh-helper@sha256:<digest> make test-phase3
```

See [`storage-cert-harness-images/docs/wiring-harness.md`](../../storage-cert-harness-images/docs/wiring-harness.md)
for the broader harness ↔ published-images workflow (builder image, Containerfile
slimming).

## 5. Commit and release

- Harness MR: describe the Quay semver and digest; note any pod-spec change.
- Release a new harness image if certification consumers run the container build.
- Clusters using a **private** Quay repo still need pull secrets on the pod
  service account; digest pinning does not remove that requirement.

## Related code and docs

- Group setup: `internal/tools/virtbench/setup.go` (`sshPodSetup`).
- Adapter pattern: [`docs/adapter-authoring.md`](adapter-authoring.md#setup-thats-shared-across-tests-run--group-scope).
- Image CI design: [`Docs/Design/image-builds/storage-cert-harness-images-gitlab-implementation.md`](../../Docs/Design/image-builds/storage-cert-harness-images-gitlab-implementation.md).

Tracked under [ECOPROJECT-5489](https://redhat.atlassian.net/browse/ECOPROJECT-5489) and
[ECOPROJECT-5498](https://redhat.atlassian.net/browse/ECOPROJECT-5498).
