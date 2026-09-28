package virtbench

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"

	"gitlab.cee.redhat.com/eco-special-projects/storage-cert-harness/internal/core"
	"gitlab.cee.redhat.com/eco-special-projects/storage-cert-harness/internal/stages"
)

// The virtbench scenarios all need one shared ssh helper pod: datasource-clone /
// boot-storm ping the VMs through it (they only VALIDATE it exists and fail
// otherwise), and disk-ops runs in-VM checks through it via sshpass. It is a
// single cluster-wide resource, shared by a group-scoped SetupProvider. Each
// scenario rechecks it before provisioning because a preceding node drain can
// evict it. Mirrors virtbench's own ensure_ssh_pod
// (disk-ops-benchmark/measure-disk-ops.py). See decisions/0008.
const (
	sshPodName = "ssh-test-pod"
	sshPodNS   = "default"
)

// sshPodManifest is the helper pod virtbench uses for ping and in-VM SSH checks.
// An init container installs alpine packages into a shared emptyDir; the main
// container mounts those paths and runs with readOnlyRootFilesystem. The pod is
// shared across scenarios; pre-existing pods are left running after the run.
// The readiness check confirms the pod is Running and sshpass is installed.
// Remove manually when decommissioning the cluster:
//
//	kubectl delete pod ssh-test-pod -n default
const sshPodManifest = `apiVersion: v1
kind: Pod
metadata:
  name: ` + sshPodName + `
  namespace: ` + sshPodNS + `
  labels:
    app: kubevirt-perf-test
    managed-by: storage-cert-harness
spec:
  securityContext:
    runAsNonRoot: true
    runAsUser: 1000
    runAsGroup: 1000
    fsGroup: 1000
    seccompProfile:
      type: RuntimeDefault
  initContainers:
  - name: install-tools
    image: alpine:latest
    securityContext:
      allowPrivilegeEscalation: false
      capabilities:
        drop: ["ALL"]
      readOnlyRootFilesystem: true
    command:
    - /bin/sh
    - -c
    - |
      cp -a /bin /lib /usr /sbin /etc /var /staging/
      apk add --root /staging --no-cache bash openssh-client sshpass iputils
      mkdir -p /tools/bin /tools/sbin /tools/lib /tools/usr/bin /tools/usr/sbin /tools/usr/lib
      cp -a /staging/bin/. /tools/bin/
      cp -a /staging/sbin/. /tools/sbin/
      cp -a /staging/lib/. /tools/lib/
      cp -a /staging/usr/bin/. /tools/usr/bin/
      cp -a /staging/usr/sbin/. /tools/usr/sbin/
      cp -a /staging/usr/lib/. /tools/usr/lib/
    volumeMounts:
    - name: staging
      mountPath: /staging
    - name: tools
      mountPath: /tools
    - name: tmp
      mountPath: /tmp
  containers:
  - name: ssh-client
    image: alpine:latest
    securityContext:
      allowPrivilegeEscalation: false
      capabilities:
        drop: ["ALL"]
      readOnlyRootFilesystem: true
    command: ["/bin/sh", "-c", "tail -f /dev/null"]
    volumeMounts:
    - name: tools
      mountPath: /bin
      subPath: bin
    - name: tools
      mountPath: /sbin
      subPath: sbin
    - name: tools
      mountPath: /lib
      subPath: lib
    - name: tools
      mountPath: /usr/bin
      subPath: usr/bin
    - name: tools
      mountPath: /usr/sbin
      subPath: usr/sbin
    - name: tools
      mountPath: /usr/lib
      subPath: usr/lib
    - name: tmp
      mountPath: /tmp
    - name: root-home
      mountPath: /root
    resources:
      requests:
        memory: "128Mi"
        cpu: "100m"
      limits:
        memory: "256Mi"
        cpu: "200m"
  volumes:
  - name: staging
    emptyDir: {}
  - name: tools
    emptyDir: {}
  - name: tmp
    emptyDir: {}
  - name: root-home
    emptyDir: {}
  restartPolicy: Always
`

// sshPodSetup is the group-scoped provider that ensures the shared ssh helper pod.
type sshPodSetup struct {
	mu          sync.Mutex
	createdByUs bool
}

// The group setup and individual provisioners share ownership tracking so a pod
// recreated after a drain is still cleaned up by the group's final teardown.
var sharedSSHSetup = &sshPodSetup{}

func (*sshPodSetup) SetupInfo() stages.SetupInfo {
	return stages.SetupInfo{Scope: stages.ScopeGroup, Key: setupGroup}
}

// Setup ensures the ssh helper pod exists and has sshpass, creating it if absent.
// Idempotent: reuses a pod that is already usable (so re-runs and a pre-existing
// pod are fine). Called at group startup and before each live scenario.
func (s *sshPodSetup) Setup(ctx context.Context, rc *core.RunCtx, trs []core.TestRequirement) error {
	// Replay grades pre-collected results with no cluster — no helper pod needed.
	if replayDir(trs) != "" {
		rc.Logger.Info("virtbench: replay mode, skipping ssh helper pod setup")
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	kubeconfig := kubeconfigOf(trs)
	// Wait for the init container to finish installing tools (image pull + apk add).
	// Bound kubectl calls and polling together, and honor run cancellation.
	ctx, cancel := context.WithTimeout(ctx, 300*time.Second)
	defer cancel()
	for {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("wait for ssh helper pod %s/%s: %w", sshPodNS, sshPodName, err)
		}
		pod, err := getSSHPod(ctx, kubeconfig)
		if err != nil {
			return err
		}
		switch {
		case pod == nil:
			rc.Logger.Info("virtbench: creating shared ssh helper pod", "pod", sshPodNS+"/"+sshPodName)
			if err := kubectlApply(ctx, kubeconfig, sshPodManifest); err != nil {
				return fmt.Errorf("create ssh helper pod: %w", err)
			}
			s.createdByUs = true
			continue
		case pod.Metadata.DeletionTimestamp != "":
			// Applying an existing terminating pod cannot revive it. Wait for
			// deletion to complete, then create a fresh pod on the next poll.
		case pod.Status.Phase == "Failed" || pod.Status.Phase == "Succeeded":
			rc.Logger.Info("virtbench: replacing stopped ssh helper pod", "phase", pod.Status.Phase)
			out, ok, err := runKubectl(ctx, kubeconfig, "delete", "pod", sshPodName, "-n", sshPodNS, "--wait=false", "--ignore-not-found")
			if err != nil || !ok {
				return fmt.Errorf("delete stopped ssh helper pod: %v: %s", err, strings.TrimSpace(out))
			}
			continue
		case pod.Status.Phase == "Running" && sshpassReady(ctx, kubeconfig):
			rc.Logger.Info("virtbench: ssh helper pod ready", "pod", sshPodNS+"/"+sshPodName)
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("wait for ssh helper pod %s/%s: %w", sshPodNS, sshPodName, ctx.Err())
		case <-time.After(5 * time.Second):
		}
	}
}

// Teardown removes the ssh helper pod if this run created it. This is the group
// teardown — called once after ALL virtbench jobs in the run complete, so the pod
// is shared across every scenario in a single harness invocation and removed once
// at the end. A pre-existing pod (created by a previous run or by the customer) is
// left alone. Best-effort; never fails the run.
func (s *sshPodSetup) Teardown(ctx context.Context, rc *core.RunCtx, trs []core.TestRequirement) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	defer func() { s.createdByUs = false }()
	if !s.createdByUs {
		return nil
	}
	kubeconfig := kubeconfigOf(trs)
	rc.Logger.Info("virtbench: removing shared ssh helper pod", "pod", sshPodNS+"/"+sshPodName)
	// Cleanup must still run if the benchmark was cancelled.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), teardownBudget)
	defer cancel()
	out, ok, err := runKubectl(ctx, kubeconfig, "delete", "pod", sshPodName, "-n", sshPodNS, "--wait=false", "--ignore-not-found")
	if err == nil && !ok {
		return fmt.Errorf("delete ssh helper pod: %s", strings.TrimSpace(out))
	}
	return err
}

// --- kubectl helpers ---------------------------------------------------------

// kubeconfigOf returns the first non-empty "kubeconfig" param among trs. Empty
// means "use the ambient KUBECONFIG / default", so no --kubeconfig flag is added.
func kubeconfigOf(trs []core.TestRequirement) string {
	for _, tr := range trs {
		if kc := strParamOr(tr, "kubeconfig", ""); kc != "" {
			return kc
		}
	}
	return ""
}

// runKubectl runs a kubectl command, returning combined stdout, exit-ok, and any
// exec error. A non-zero exit is reported via ok=false, not err (err is reserved
// for failing to launch kubectl at all).
func runKubectl(ctx context.Context, kubeconfig string, args ...string) (out string, ok bool, err error) {
	full := args
	if kubeconfig != "" {
		full = append([]string{"--kubeconfig", kubeconfig}, args...)
	}
	cmd := exec.CommandContext(ctx, "kubectl", full...) // #nosec G204 -- kubectl arguments are constructed by the harness.
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	runErr := cmd.Run()
	if runErr == nil {
		return buf.String(), true, nil
	}
	if _, isExit := runErr.(*exec.ExitError); isExit {
		return buf.String(), false, nil
	}
	return buf.String(), false, runErr
}

// kubectlApply pipes a manifest to `kubectl apply -f -`.
func kubectlApply(ctx context.Context, kubeconfig, manifest string) error {
	args := []string{"apply", "-f", "-"}
	if kubeconfig != "" {
		args = append([]string{"--kubeconfig", kubeconfig}, args...)
	}
	cmd := exec.CommandContext(ctx, "kubectl", args...)
	cmd.Stdin = strings.NewReader(manifest)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("kubectl apply: %v: %s", err, strings.TrimSpace(buf.String()))
	}
	return nil
}

type sshPodState struct {
	Metadata struct {
		DeletionTimestamp string `json:"deletionTimestamp"`
	} `json:"metadata"`
	Status struct {
		Phase string `json:"phase"`
	} `json:"status"`
}

// getSSHPod distinguishes absence from API/auth failures; only absence permits
// creation. A terminating or terminal pod must never pass the readiness check.
func getSSHPod(ctx context.Context, kubeconfig string) (*sshPodState, error) {
	out, ok, err := runKubectl(ctx, kubeconfig, "get", "pod", sshPodName, "-n", sshPodNS,
		"--ignore-not-found", "-o", "json")
	if err != nil {
		return nil, fmt.Errorf("get ssh helper pod: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("get ssh helper pod: %w", err)
	}
	if !ok {
		return nil, fmt.Errorf("get ssh helper pod %s/%s: %s", sshPodNS, sshPodName, strings.TrimSpace(out))
	}
	if strings.TrimSpace(out) == "" {
		return nil, nil
	}
	var pod sshPodState
	if err := json.Unmarshal([]byte(out), &pod); err != nil {
		return nil, fmt.Errorf("decode ssh helper pod: %w", err)
	}
	return &pod, nil
}

// sshpassReady checks that the pod has sshpass installed and ready. The alpine
// pod installs it via apk on startup; this exec confirms the install completed.
func sshpassReady(ctx context.Context, kubeconfig string) bool {
	_, ok, _ := runKubectl(ctx, kubeconfig, "exec", "-n", sshPodNS, sshPodName, "--", "which", "sshpass")
	return ok
}

// listNamespacesByPrefix returns namespaces whose name starts with prefix.
func listNamespacesByPrefix(ctx context.Context, kubeconfig, prefix string) ([]string, error) {
	out, ok, err := runKubectl(ctx, kubeconfig, "get", "ns", "-o", "name")
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("kubectl get ns failed: %s", strings.TrimSpace(out))
	}
	var names []string
	for line := range strings.SplitSeq(strings.TrimSpace(out), "\n") {
		name := strings.TrimPrefix(strings.TrimSpace(line), "namespace/")
		if name != "" && strings.HasPrefix(name, prefix) {
			names = append(names, name)
		}
	}
	return names, nil
}

func deleteNamespace(ctx context.Context, kubeconfig, ns string) error {
	out, ok, err := runKubectl(ctx, kubeconfig, "delete", "ns", ns, "--wait=false", "--ignore-not-found")
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("kubectl delete ns %s: %s", ns, strings.TrimSpace(out))
	}
	return nil
}
