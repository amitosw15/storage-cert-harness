package virtbench

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/openshift-storage-cert/storage-cert-harness/internal/core"
	"github.com/openshift-storage-cert/storage-cert-harness/internal/stages"
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

	sshPodDeleteAttempts  = 3
	sshPodDeleteRetryWait = 100 * time.Millisecond

	// Pinned quay.io/virtarraycert/ssh-helper release (0.1.11; log wrappers in /opt/sshhelper/bin).
	sshHelperImage = "quay.io/virtarraycert/ssh-helper@sha256:369697c3002c1971bb608189d343fe9ea4989d2b66fde1bd1b64e924416533d1"

	sshPodAnnotPrefix = "storage-cert-harness.redhat.com/"
)

// sshPodManifest is the helper pod virtbench uses for ping and in-VM SSH checks.
// Keep in sync with container-patches/virtbench/examples/utilities/ssh-pod.yaml.
// Remove manually when decommissioning the cluster:
//
//	kubectl delete pod ssh-test-pod -n default
var sshPodManifest = `apiVersion: v1
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
    runAsUser: 65532
    runAsGroup: 65532
    fsGroup: 65532
    seccompProfile:
      type: RuntimeDefault
  containers:
  - name: ssh-client
    image: ` + sshHelperImage + `
    imagePullPolicy: IfNotPresent
    securityContext:
      allowPrivilegeEscalation: false
      capabilities:
        drop: ["ALL"]
      readOnlyRootFilesystem: true
      runAsNonRoot: true
      runAsUser: 65532
      runAsGroup: 65532
    command: ["/bin/bash", "-c", "exec sleep infinity"]
    volumeMounts:
    - name: tmp
      mountPath: /tmp
    - name: home
      mountPath: /home/sshhelper
    resources:
      requests:
        memory: "128Mi"
        cpu: "100m"
      limits:
        memory: "256Mi"
        cpu: "200m"
  volumes:
  - name: tmp
    emptyDir: {}
  - name: home
    emptyDir: {}
  restartPolicy: Always
`

// sshPodSetup is the group-scoped provider that ensures the shared ssh helper pod.
type sshPodSetup struct {
	mu sync.Mutex
}

var sharedSSHSetup = &sshPodSetup{}

func (*sshPodSetup) SetupInfo() stages.SetupInfo {
	return stages.SetupInfo{Scope: stages.ScopeGroup, Key: setupGroup}
}

// Setup ensures the ssh helper pod exists and has sshpass, creating it if absent.
// Idempotent: reuses a pod that is already usable (so re-runs and a pre-existing
// pod are fine). Called at group startup and before each live scenario.
func (s *sshPodSetup) Setup(ctx context.Context, rc *core.RunCtx, trs []core.TestRequirement) (err error) {
	// Replay grades pre-collected results with no cluster — no helper pod needed.
	if allReplayTRs(trs) {
		rc.Logger.Info("virtbench: replay mode, skipping ssh helper pod setup")
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	kubeconfig := kubeconfigOf(trs)
	var createdPodThisSetup bool
	defer func() {
		if err != nil && createdPodThisSetup {
			rc.Logger.Info("virtbench: rolling back ssh helper pod after failed setup", "pod", sshPodNS+"/"+sshPodName)
			cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), teardownBudget)
			defer cancel()
			if delErr := deleteSSHPod(cleanupCtx, kubeconfig); delErr != nil {
				rc.Logger.Warn("virtbench: failed to roll back ssh helper pod after failed setup", "err", delErr)
			}
		}
	}()
	avoidNodeNames, avoidHostnameLabels, err := sshHelperAvoidNodes(ctx, kubeconfig, trs)
	if err != nil {
		return err
	}
	// Wait for the init container to finish installing tools (image pull + apk add).
	// Bound kubectl calls and polling together, and honor run cancellation.
	ctx, cancel := context.WithTimeout(ctx, 300*time.Second)
	defer cancel()
	isSSHPodExistingLogged := false
	for {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("wait for ssh helper pod %s/%s: %w", sshPodNS, sshPodName, err)
		}
		pod, err := getSSHPod(ctx, kubeconfig)
		if err != nil {
			return err
		}
		if pod != nil && !isSSHPodExistingLogged {
			logExistingSSHPodAtStartup(rc, pod)
			isSSHPodExistingLogged = true
		}
		switch {
		case pod == nil:
			rc.Logger.Info("virtbench: creating shared ssh helper pod", "pod", sshPodNS+"/"+sshPodName)
			// Apply can create the pod before its command returns, so a timeout or
			// partial failure must still trigger idempotent rollback.
			createdPodThisSetup = true
			if err := kubectlApply(ctx, kubeconfig, sshPodManifestForNodes(avoidHostnameLabels, sshPodBootstrapAnnotations(rc))); err != nil {
				return fmt.Errorf("create ssh helper pod: %w", err)
			}
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
		case sshPodOnNode(pod, avoidNodeNames):
			rc.Logger.Info("virtbench: replacing ssh helper on FAR target node", "node", pod.Spec.NodeName)
			out, ok, err := runKubectl(ctx, kubeconfig, "delete", "pod", sshPodName, "-n", sshPodNS, "--wait=false", "--ignore-not-found")
			if err != nil || !ok {
				return fmt.Errorf("delete ssh helper on FAR target node: %v: %s", err, strings.TrimSpace(out))
			}
			continue
		case pod.Status.Phase == "Running" && sshpassReady(ctx, kubeconfig):
			if err := stampSSHPodHarness(ctx, kubeconfig, rc, pod); err != nil {
				return err
			}
			rc.Logger.Info("virtbench: ssh helper pod ready", "pod", sshPodNS+"/"+sshPodName, "podIP", pod.Status.PodIP)
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("wait for ssh helper pod %s/%s: %w", sshPodNS, sshPodName, ctx.Err())
		case <-time.After(5 * time.Second):
		}
	}
}

// sshHelperAvoidNodes returns target node names and hostname labels declared by
// the plan. Names recognize an existing helper's assigned node; labels are used
// by Kubernetes scheduling affinity.
func sshHelperAvoidNodes(ctx context.Context, kubeconfig string, trs []core.TestRequirement) (map[string]struct{}, map[string]struct{}, error) {
	avoidNames := make(map[string]struct{})
	avoidLabels := make(map[string]struct{})
	for _, tr := range trs {
		node := strParamOr(tr, "node", "")
		if node == "" {
			continue
		}
		avoidNames[node] = struct{}{}
		label, ok, err := runKubectl(ctx, kubeconfig, "get", "node", node,
			"-o", `jsonpath={.metadata.labels.kubernetes\.io/hostname}`)
		if err != nil {
			return nil, nil, fmt.Errorf("get FAR target node %s: %w", node, err)
		}
		if !ok || strings.TrimSpace(label) == "" {
			return nil, nil, fmt.Errorf("get FAR target node %s: %s", node, strings.TrimSpace(label))
		}
		avoidLabels[strings.TrimSpace(label)] = struct{}{}
	}
	return avoidNames, avoidLabels, nil
}

func sshPodManifestForNodes(avoidNodes map[string]struct{}, annotations map[string]string) string {
	manifest := sshPodManifest
	if len(annotations) > 0 {
		var ann strings.Builder
		ann.WriteString("  annotations:\n")
		for _, key := range sortedMapKeys(annotations) {
			ann.WriteString("    ")
			ann.WriteString(key)
			ann.WriteString(": ")
			ann.WriteString(strconv.Quote(annotations[key]))
			ann.WriteByte('\n')
		}
		manifest = strings.Replace(manifest, "    managed-by: storage-cert-harness\n", "    managed-by: storage-cert-harness\n"+ann.String(), 1)
	}
	if len(avoidNodes) == 0 {
		return manifest
	}
	values := make([]string, 0, len(avoidNodes))
	for node := range avoidNodes {
		values = append(values, node)
	}
	slices.Sort(values)
	var affinity strings.Builder
	affinity.WriteString("  affinity:\n    nodeAffinity:\n      requiredDuringSchedulingIgnoredDuringExecution:\n        nodeSelectorTerms:\n        - matchExpressions:\n          - key: kubernetes.io/hostname\n            operator: NotIn\n            values:\n")
	for _, node := range values {
		affinity.WriteString("            - ")
		affinity.WriteString(strconv.Quote(node))
		affinity.WriteByte('\n')
	}
	return strings.Replace(manifest, "spec:\n", "spec:\n"+affinity.String(), 1)
}

func sortedMapKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

func sshPodBootstrapAnnotations(rc *core.RunCtx) map[string]string {
	if rc == nil {
		return nil
	}
	ann := map[string]string{
		sshPodAnnotPrefix + "run-id":       rc.RunID,
		sshPodAnnotPrefix + "bootstrap-at": time.Now().UTC().Format(time.RFC3339),
	}
	if rc.PartKind != "" {
		ann[sshPodAnnotPrefix+"part-kind"] = rc.PartKind
	}
	if host, err := os.Hostname(); err == nil && host != "" {
		ann[sshPodAnnotPrefix+"bootstrap-host"] = host
	}
	return ann
}

func sshPodHarnessRunID(pod *sshPodState) string {
	if pod == nil || pod.Metadata.Annotations == nil {
		return ""
	}
	return strings.TrimSpace(pod.Metadata.Annotations[sshPodAnnotPrefix+"run-id"])
}

func logExistingSSHPodAtStartup(rc *core.RunCtx, pod *sshPodState) {
	if rc == nil || pod == nil {
		return
	}
	managedBy := pod.Metadata.Labels["managed-by"]
	attrs := sshPodLogAttrs(pod, managedBy)
	if managedBy != "" && managedBy != "storage-cert-harness" {
		rc.Logger.Warn("virtbench: ssh helper pod exists but is not labeled managed-by storage-cert-harness", attrs...)
		return
	}
	existingRunID := sshPodHarnessRunID(pod)
	if existingRunID == rc.RunID {
		rc.Logger.Info("virtbench: ssh helper pod already exists for this harness run", attrs...)
		return
	}
	rc.Logger.Info("virtbench: orphan ssh helper pod found; it will be removed when this harness run completes", attrs...)
	if existingRunID != "" && existingRunID != rc.RunID {
		other := []any{
			"pod", sshPodNS + "/" + sshPodName,
			"otherRunID", existingRunID,
			"thisRunID", rc.RunID,
		}
		if host := strings.TrimSpace(pod.Metadata.Annotations[sshPodAnnotPrefix+"bootstrap-host"]); host != "" {
			other = append(other, "otherBootstrapHost", host)
		}
		if at := strings.TrimSpace(pod.Metadata.Annotations[sshPodAnnotPrefix+"bootstrap-at"]); at != "" {
			other = append(other, "otherBootstrapAt", at)
		}
		rc.Logger.Warn("virtbench: another storage-cert-harness run may still be using this ssh helper pod; this run will delete the pod when it completes", other...)
	}
}

func sshPodLogAttrs(pod *sshPodState, managedBy string) []any {
	attrs := []any{
		"pod", sshPodNS + "/" + sshPodName,
		"phase", pod.Status.Phase,
		"node", pod.Spec.NodeName,
		"podIP", pod.Status.PodIP,
		"managed-by", managedBy,
	}
	if runID := sshPodHarnessRunID(pod); runID != "" {
		attrs = append(attrs, "harnessRunID", runID)
	}
	if pod.Metadata.DeletionTimestamp != "" {
		attrs = append(attrs, "deletionTimestamp", pod.Metadata.DeletionTimestamp)
	}
	return attrs
}

func stampSSHPodHarness(ctx context.Context, kubeconfig string, rc *core.RunCtx, pod *sshPodState) error {
	ann := sshPodBootstrapAnnotations(rc)
	if ip := strings.TrimSpace(pod.Status.PodIP); ip != "" {
		ann[sshPodAnnotPrefix+"pod-ip"] = ip
	}
	if len(ann) == 0 {
		return nil
	}
	if err := kubectlAnnotatePod(ctx, kubeconfig, ann); err != nil {
		return fmt.Errorf("annotate ssh helper pod: %w", err)
	}
	return nil
}

// Teardown removes the shared ssh helper pod. This is the group teardown — called
// once after ALL virtbench jobs in the run complete. The pod is deleted whether
// this run created it or reused a leftover from a prior run. Best-effort; never
// fails the run.
func (s *sshPodSetup) Teardown(ctx context.Context, rc *core.RunCtx, trs []core.TestRequirement) error {
	if allReplayTRs(trs) {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	kubeconfig := kubeconfigOf(trs)
	pod, err := getSSHPod(ctx, kubeconfig)
	if err != nil {
		rc.Logger.Warn("virtbench: could not inspect ssh helper pod before teardown", "err", err)
	} else if pod == nil {
		rc.Logger.Info("virtbench: ssh helper pod not found; nothing to delete", "pod", sshPodNS+"/"+sshPodName)
	} else {
		rc.Logger.Info("virtbench: deleting ssh helper pod", sshPodLogAttrs(pod, pod.Metadata.Labels["managed-by"])...)
	}
	// Cleanup must still run if the benchmark was cancelled.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), teardownBudget)
	defer cancel()
	return deleteSSHPod(ctx, kubeconfig)
}

func deleteSSHPod(ctx context.Context, kubeconfig string) error {
	var lastErr error
	for attempt := 0; attempt < sshPodDeleteAttempts; attempt++ {
		out, ok, err := runKubectl(ctx, kubeconfig, "delete", "pod", sshPodName, "-n", sshPodNS, "--wait=false", "--ignore-not-found")
		if err == nil && ok {
			return nil
		}
		if err != nil {
			lastErr = err
		} else {
			lastErr = fmt.Errorf("delete ssh helper pod: %s", strings.TrimSpace(out))
		}
		if attempt+1 == sshPodDeleteAttempts {
			break
		}
		timer := time.NewTimer(sshPodDeleteRetryWait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return lastErr
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

func kubectlAnnotatePod(ctx context.Context, kubeconfig string, annotations map[string]string) error {
	if len(annotations) == 0 {
		return nil
	}
	args := []string{"annotate", "pod", sshPodName, "-n", sshPodNS, "--overwrite"}
	for _, key := range sortedMapKeys(annotations) {
		args = append(args, key+"="+annotations[key])
	}
	out, ok, err := runKubectl(ctx, kubeconfig, args...)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("kubectl annotate: %s", strings.TrimSpace(out))
	}
	return nil
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
		DeletionTimestamp string            `json:"deletionTimestamp"`
		Labels            map[string]string `json:"labels"`
		Annotations       map[string]string `json:"annotations"`
	} `json:"metadata"`
	Status struct {
		Phase string `json:"phase"`
		PodIP string `json:"podIP"`
	} `json:"status"`
	Spec struct {
		NodeName string `json:"nodeName"`
	} `json:"spec"`
}

func sshPodOnNode(pod *sshPodState, avoidNodes map[string]struct{}) bool {
	if pod == nil || pod.Spec.NodeName == "" {
		return false
	}
	_, found := avoidNodes[pod.Spec.NodeName]
	return found
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

// sshpassReady checks that the helper image has sshpass on PATH (command -v; the
// UBI image does not ship which).
func sshpassReady(ctx context.Context, kubeconfig string) bool {
	_, ok, _ := runKubectl(ctx, kubeconfig, "exec", "-n", sshPodNS, sshPodName, "--",
		"/bin/bash", "-c", "command -v sshpass >/dev/null")
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
