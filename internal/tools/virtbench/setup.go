package virtbench

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/openshift-storage-cert/storage-cert-harness/internal/clustercheck"
	"github.com/openshift-storage-cert/storage-cert-harness/internal/core"
	"github.com/openshift-storage-cert/storage-cert-harness/internal/manifest"
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

	sshPodDeleteAttempts       = 3
	sshPodDeleteRetryWait      = 100 * time.Millisecond
	sshPodRollbackPoll         = 50 * time.Millisecond
	sshPodRollbackPollDeleting = time.Second
	sshPodRollbackSettle       = time.Second

	sshPodAnnotPrefix = "storage-cert-harness.redhat.com/"
)

// errSSHPodUIDPreconditionFailed means delete targeted a different object instance.
var errSSHPodUIDPreconditionFailed = errors.New("ssh helper pod delete UID precondition failed")

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
	var createAttemptedThisSetup bool
	var createdPodUID string
	defer func() {
		if err != nil && createAttemptedThisSetup {
			rollbackFailedSSHSetup(ctx, kubeconfig, rc, createdPodUID)
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
			// Create (not apply) so a pod another harness created between get and
			// create is not patched with this run's annotations. Create can still
			// commit before its command returns, so rollback reconciles by run-id/UID.
			manifest, err := sshPodManifestForNodes(avoidHostnameLabels, sshPodBootstrapAnnotations(rc))
			if err != nil {
				return err
			}
			createAttemptedThisSetup = true
			uid, err := createSSHPod(ctx, kubeconfig, manifest)
			if err != nil {
				if errors.Is(err, clustercheck.ErrAlreadyExists) {
					continue
				}
				return fmt.Errorf("create ssh helper pod: %w", err)
			}
			if uid != "" {
				createdPodUID = uid
			}
			continue
		case pod.Metadata.DeletionTimestamp != "":
			// A terminating pod cannot be recreated until deletion finishes. Wait
			// for it to disappear, then create a fresh pod on the next poll.
		case pod.Status.Phase == "Failed" || pod.Status.Phase == "Succeeded":
			rc.Logger.Info("virtbench: replacing stopped ssh helper pod", "phase", pod.Status.Phase)
			if err := clustercheck.DeletePod(ctx, kubeconfig, sshPodReference()); err != nil {
				return fmt.Errorf("delete stopped ssh helper pod: %w", err)
			}
			continue
		case sshPodOnNode(pod, avoidNodeNames):
			rc.Logger.Info("virtbench: replacing ssh helper on FAR target node", "node", pod.Spec.NodeName)
			if err := clustercheck.DeletePod(ctx, kubeconfig, sshPodReference()); err != nil {
				return fmt.Errorf("delete ssh helper on FAR target node: %w", err)
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

// rollbackFailedSSHSetup reconciles cleanup after setup has attempted to apply
// the shared helper pod. The apply may have created the pod before kubectl
// reported a timeout, while another harness may have claimed the fixed pod
// name after this setup observed it as absent. rollbackSSHPod therefore checks
// the harness run ID before deleting and keeps polling until a delayed apply is
// settled and the pod has remained absent. Cleanup uses a detached bounded
// context because setup may already be cancelled when this deferred rollback
// runs.
func rollbackFailedSSHSetup(ctx context.Context, kubeconfig string, rc *core.RunCtx, createdPodUID string) {
	rc.Logger.Info("virtbench: rolling back ssh helper pod after failed setup", "pod", sshPodNS+"/"+sshPodName)
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), teardownBudget)
	defer cancel()
	if err := rollbackSSHPod(cleanupCtx, kubeconfig, rc.RunID, createdPodUID); err != nil {
		rc.Logger.Warn("virtbench: failed to roll back ssh helper pod after failed setup", "err", err)
	}
}

// rollbackSSHPod reconciles a failed create without reissuing delete while the
// pod is still visible. It deletes at most once, using a UID precondition on the
// pod returned by getSSHPod, and only when the harness run-id matches. Another
// run's pod or a same-name replacement is left alone. After delete, it polls
// with a slower interval until the pod stays absent for sshPodRollbackSettle.
func rollbackSSHPod(ctx context.Context, kubeconfig, runID, createdPodUID string) error {
	if strings.TrimSpace(runID) == "" {
		return fmt.Errorf("cannot roll back ssh helper pod without a harness run ID")
	}

	var absentSince time.Time
	var lastErr error
	var deleteIssued bool
	ticker := time.NewTicker(sshPodRollbackPoll)
	defer ticker.Stop()
	for {
		pod, err := getSSHPod(ctx, kubeconfig)
		if err != nil {
			lastErr = err
			absentSince = time.Time{}
		} else if pod == nil {
			if absentSince.IsZero() {
				absentSince = time.Now()
			}
			if time.Since(absentSince) >= sshPodRollbackSettle {
				return nil
			}
		} else {
			absentSince = time.Time{}
			if sshPodHarnessRunID(pod) != runID {
				return nil
			}
			if createdPodUID != "" && pod.Metadata.UID != createdPodUID {
				return nil
			}
			if !deleteIssued {
				if err := deleteSSHPodUID(ctx, kubeconfig, pod.Metadata.UID); err != nil {
					if errors.Is(err, errSSHPodUIDPreconditionFailed) {
						return nil
					}
					lastErr = err
				} else {
					deleteIssued = true
					ticker.Reset(sshPodRollbackPollDeleting)
				}
			}
		}

		select {
		case <-ctx.Done():
			if lastErr != nil {
				return fmt.Errorf("reconcile ssh helper pod rollback: %w", lastErr)
			}
			return ctx.Err()
		case <-ticker.C:
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
		resource, err := clustercheck.GetNode(ctx, kubeconfig, node)
		if err != nil {
			return nil, nil, fmt.Errorf("get FAR target node %s: %w", node, err)
		}
		label := strings.TrimSpace(resource.Metadata.Labels["kubernetes.io/hostname"])
		if label == "" {
			return nil, nil, fmt.Errorf("FAR target node %s has no hostname label", node)
		}
		avoidLabels[label] = struct{}{}
	}
	return avoidNames, avoidLabels, nil
}

func sshPodManifestForNodes(avoidNodes map[string]struct{}, annotations map[string]string) (string, error) {
	values := make([]string, 0, len(avoidNodes))
	for node := range avoidNodes {
		values = append(values, node)
	}
	slices.Sort(values)
	templateSource, err := templates.ReadFile(sshPodManifestPath)
	if err != nil {
		return "", err
	}
	rendered, err := manifest.Render(vmRenderer, "ssh-pod.yaml", templateSource, map[string]any{
		"NAME": sshPodName, "NAMESPACE": sshPodNS, "ANNOTATIONS": annotations, "AVOID_NODES": values,
	})
	return string(rendered), err
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

func sshPodHarnessRunID(pod *clustercheck.Pod) string {
	if pod == nil || pod.Metadata.Annotations == nil {
		return ""
	}
	return strings.TrimSpace(pod.Metadata.Annotations[sshPodAnnotPrefix+"run-id"])
}

func logExistingSSHPodAtStartup(rc *core.RunCtx, pod *clustercheck.Pod) {
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

func sshPodLogAttrs(pod *clustercheck.Pod, managedBy string) []any {
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

func stampSSHPodHarness(ctx context.Context, kubeconfig string, rc *core.RunCtx, pod *clustercheck.Pod) error {
	ann := sshPodBootstrapAnnotations(rc)
	if ip := strings.TrimSpace(pod.Status.PodIP); ip != "" {
		ann[sshPodAnnotPrefix+"pod-ip"] = ip
	}
	if len(ann) == 0 {
		return nil
	}
	if err := clustercheck.AnnotatePod(ctx, kubeconfig, sshPodReference(), ann); err != nil {
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

func deleteSSHPodUID(ctx context.Context, kubeconfig, uid string) error {
	uid = strings.TrimSpace(uid)
	if uid == "" {
		return fmt.Errorf("delete ssh helper pod: missing UID")
	}
	var lastErr error
	for attempt := 0; attempt < sshPodDeleteAttempts; attempt++ {
		err := clustercheck.DeletePodWithUID(ctx, kubeconfig, sshPodReference(), uid)
		if err == nil {
			return nil
		}
		if errors.Is(err, clustercheck.ErrUIDPreconditionFailed) {
			return fmt.Errorf("%w: %v", errSSHPodUIDPreconditionFailed, err)
		}
		lastErr = err
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

func deleteSSHPod(ctx context.Context, kubeconfig string) error {
	var lastErr error
	for attempt := 0; attempt < sshPodDeleteAttempts; attempt++ {
		err := clustercheck.DeletePod(ctx, kubeconfig, sshPodReference())
		if err == nil {
			return nil
		}
		lastErr = err
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

// Resource operations for the shared SSH helper.

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

func sshPodReference() clustercheck.NamespacedName {
	return clustercheck.NamespacedName{Name: sshPodName, Namespace: sshPodNS}
}

// createSSHPod creates the helper pod and returns its UID from the API response.
func createSSHPod(ctx context.Context, kubeconfig, manifest string) (string, error) {
	pod, err := clustercheck.CreatePod(ctx, kubeconfig, []byte(manifest))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(pod.Metadata.UID), nil
}

func sshPodOnNode(pod *clustercheck.Pod, avoidNodes map[string]struct{}) bool {
	if pod == nil || pod.Spec.NodeName == "" {
		return false
	}
	_, found := avoidNodes[pod.Spec.NodeName]
	return found
}

// getSSHPod treats only absence as permission to create the helper.
func getSSHPod(ctx context.Context, kubeconfig string) (*clustercheck.Pod, error) {
	pod, err := clustercheck.GetPod(ctx, kubeconfig, sshPodReference())
	if errors.Is(err, clustercheck.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get ssh helper pod: %w", err)
	}
	return &pod, nil
}

// sshpassReady checks that the helper image has sshpass on PATH.
func sshpassReady(ctx context.Context, kubeconfig string) bool {
	_, err := clustercheck.ExecPod(ctx, kubeconfig, sshPodReference(),
		"/bin/bash", "-c", "command -v sshpass >/dev/null")
	return err == nil
}

func listNamespacesByPrefix(ctx context.Context, kubeconfig, prefix string) ([]string, error) {
	namespaces, err := clustercheck.ListNamespaces(ctx, kubeconfig)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, namespace := range namespaces {
		if strings.HasPrefix(namespace.Name, prefix) {
			names = append(names, namespace.Name)
		}
	}
	return names, nil
}
