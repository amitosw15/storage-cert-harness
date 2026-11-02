package clustercheck

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openshift-storage-cert/storage-cert-harness/internal/core"
)

type storageLiveCluster struct{ kubeconfig string }

func (c storageLiveCluster) run(t *testing.T, input any, args ...string) []byte {
	t.Helper()
	var data []byte
	if input != nil {
		var err error
		data, err = json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	out, err := SnapshotCommand(ctx, c.kubeconfig, data, args...)
	if err != nil {
		t.Fatalf("%s: %v", strings.Join(args, " "), err)
	}
	return out
}

func (c storageLiveCluster) create(t *testing.T, object map[string]any) {
	t.Helper()
	c.run(t, object, "create", "-f", "-")
}

func (c storageLiveCluster) check(t *testing.T, requests []StorageRequest, wantError, wantWarning bool) []core.Finding {
	t.Helper()
	tr := core.TestRequirement{ID: t.Name(), Params: map[string]any{}}
	LabelStorageRequests(requests, tr)
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	findings := CheckStorage(ctx, c.kubeconfig, requests)
	hasError, hasWarning := false, false
	for _, finding := range findings {
		hasError = hasError || finding.Level == "error"
		hasWarning = hasWarning || finding.Level == "warn"
	}
	if hasError != wantError || hasWarning != wantWarning {
		t.Fatalf("errors=%t warnings=%t; expected %t %t: %+v", hasError, hasWarning, wantError, wantWarning, findings)
	}
	t.Logf("errors=%t warnings=%t as expected", hasError, hasWarning)
	return findings
}

func (c storageLiveCluster) await(t *testing.T, kind, name, namespace string, predicate func([]byte) bool) {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		args := []string{"get", kind, name, "-o", "json"}
		if namespace != "" {
			args = append(args, "-n", namespace)
		}
		data := c.run(t, nil, args...)
		if predicate(data) {
			return
		}
		select {
		case <-t.Context().Done():
			t.Fatal(t.Context().Err())
		case <-time.After(time.Second):
		}
	}
	t.Fatalf("timed out observing %s %s", kind, name)
}

func readySource(data []byte) bool {
	var ds preflightDataSource
	if json.Unmarshal(data, &ds) != nil {
		return false
	}
	for _, condition := range ds.Status.Conditions {
		if condition.Type == "Ready" && condition.Status == "True" {
			return true
		}
	}
	return false
}

// TestStoragePreflightPatchedLive mutates only disposable fixtures and requires explicit opt-in.
func TestStoragePreflightPatchedLive(t *testing.T) {
	if os.Getenv("STORAGE_PREFLIGHT_LIVE_MUTATIONS") != "true" {
		t.Skip("set STORAGE_PREFLIGHT_LIVE_MUTATIONS=true to create disposable fixtures")
	}
	required := func(key string) string {
		t.Helper()
		value := os.Getenv("STORAGE_PREFLIGHT_LIVE_" + key)
		if value == "" {
			t.Fatalf("missing live configuration %s", key)
		}
		return value
	}
	c := storageLiveCluster{kubeconfig: required("KUBECONFIG")}
	nas, economy := required("NAS_CLASS"), required("ECONOMY_CLASS")
	nasDS, economyDS := required("NAS_SOURCE"), required("ECONOMY_SOURCE")
	snapshotClass := required("SNAPSHOT_CLASS")
	config, err := GetCDIConfig(t.Context(), c.kubeconfig)
	if err != nil {
		t.Fatal(err)
	}
	scratch := config.Status.ScratchSpaceStorageClass
	if scratch == "" {
		t.Fatal("this role matrix requires an explicitly configured scratch class")
	}
	prefix := fmt.Sprintf("storage-preflight-%d", time.Now().UnixNano())
	sourceNS, targetNS := prefix, prefix+"-target"
	temporaryClass, goodVSC, badVSC := prefix+"-sc", prefix+"-snap", prefix+"-bad-snap"
	role := prefix + "-reader"
	unknownClass := prefix + "-unknown"
	t.Logf("fixtures=%s source=%s target=%s scratch=%s", prefix, nas, economy, scratch)
	t.Cleanup(func() {
		for _, args := range [][]string{
			{"delete", "namespace", sourceNS, targetNS, "--ignore-not-found", "--wait=true", "--timeout=90s"},
			{"delete", "storageclass", temporaryClass, unknownClass, "--ignore-not-found"},
			{"delete", "volumesnapshotclass", goodVSC, badVSC, "--ignore-not-found"},
			{"delete", "clusterrolebinding", role, "--ignore-not-found"},
			{"delete", "clusterrole", role, "--ignore-not-found"},
		} {
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Second)
			_, err := SnapshotCommand(ctx, c.kubeconfig, nil, args...)
			cancel()
			if err != nil {
				t.Errorf("fixture cleanup failed: %v", err)
			}
		}
	})
	for _, ns := range []string{sourceNS, targetNS} {
		c.create(t, map[string]any{"apiVersion": "v1", "kind": "Namespace", "metadata": map[string]any{"name": ns}})
	}
	sc, err := readPreflightStorageClass(t.Context(), c.kubeconfig, nas)
	if err != nil {
		t.Fatal(err)
	}
	sc["metadata"] = map[string]any{"name": temporaryClass}
	delete(sc, "status")
	c.create(t, sc)
	for name, driver := range map[string]string{goodVSC: sc["provisioner"].(string), badVSC: "storage-preflight.invalid.csi"} {
		c.create(t, map[string]any{"apiVersion": "snapshot.storage.k8s.io/v1", "kind": "VolumeSnapshotClass", "metadata": map[string]any{"name": name}, "driver": driver, "deletionPolicy": "Delete"})
	}
	scratchPVC := "scratch-source"
	c.create(t, map[string]any{"apiVersion": "v1", "kind": "PersistentVolumeClaim", "metadata": map[string]any{"name": scratchPVC, "namespace": sourceNS}, "spec": map[string]any{"storageClassName": scratch, "volumeMode": "Filesystem", "accessModes": []string{"ReadWriteOnce"}, "resources": map[string]any{"requests": map[string]any{"storage": "1Gi"}}}})
	c.run(t, nil, "wait", "--for=jsonpath={.status.phase}=Bound", "pvc/"+scratchPVC, "-n", sourceNS, "--timeout=90s")
	aliases := map[string]string{nas: "nas-source", economy: "economy-source", scratch: "scratch-source"}
	for class, dsName := range map[string]string{nas: nasDS, economy: economyDS} {
		parts := strings.SplitN(dsName, "/", 2)
		if len(parts) != 2 {
			t.Fatal("live source must be namespace/name")
		}
		ds, err := readPreflightDataSource(t.Context(), c.kubeconfig, storageReference{Namespace: parts[0], Name: parts[1]})
		if err != nil {
			t.Fatal(err)
		}
		for kind, ref := range ds.Spec.Source {
			if ref.Namespace == "" {
				ref.Namespace = parts[0]
				ds.Spec.Source[kind] = ref
			}
		}
		c.create(t, map[string]any{"apiVersion": "cdi.kubevirt.io/v1beta1", "kind": "DataSource", "metadata": map[string]any{"name": aliases[class], "namespace": sourceNS}, "spec": ds.Spec})
		c.await(t, "datasource", aliases[class], sourceNS, readySource)
	}
	c.create(t, map[string]any{"apiVersion": "cdi.kubevirt.io/v1beta1", "kind": "DataSource", "metadata": map[string]any{"name": aliases[scratch], "namespace": sourceNS}, "spec": map[string]any{"source": map[string]any{"pvc": map[string]any{"name": scratchPVC, "namespace": sourceNS}}}})
	c.await(t, "datasource", aliases[scratch], sourceNS, readySource)
	requestsFor := func(source, target string) []StorageRequest {
		data := []byte(fmt.Sprintf(`apiVersion: kubevirt.io/v1
kind: VirtualMachine
metadata: {name: role-matrix, namespace: %q}
spec:
  dataVolumeTemplates:
  - metadata: {name: clone}
    spec:
      sourceRef: {kind: DataSource, name: %q, namespace: %q}
      storage: {storageClassName: %q, accessModes: [ReadWriteOnce], volumeMode: Filesystem, resources: {requests: {storage: 30Gi}}}
  - metadata: {name: import}
    spec:
      source: {registry: {url: 'docker://example.invalid/not-executed'}}
      storage: {storageClassName: %q, accessModes: [ReadWriteOnce], volumeMode: Filesystem, resources: {requests: {storage: 30Gi}}}
`, targetNS, aliases[source], sourceNS, target, target))
		requests, err := StorageRequests(data, target, targetNS)
		if err != nil {
			t.Fatal(err)
		}
		return requests
	}
	for _, source := range []string{nas, economy, scratch} {
		for _, target := range []string{nas, economy, scratch} {
			t.Run("roles/"+aliases[source]+"_to_"+target, func(t *testing.T) {
				findings := c.check(t, requestsFor(source, target), false, source != target)
				if source != target && !findingContains(findings, "warn", "source StorageClass") {
					t.Fatalf("expected advisory class comparison: %+v", findings)
				}
				if !findingContains(findings, "info", "CDI scratch StorageClass="+scratch) {
					t.Fatalf("scratch class not assessed: %+v", findings)
				}
			})
		}
	}

	t.Run("profile patches", func(t *testing.T) {
		c.run(t, nil, "wait", "--for=create", "storageprofile/"+temporaryClass, "--timeout=60s")
		patchProfile := func(properties string) {
			c.run(t, nil, "patch", "storageprofile", temporaryClass, "--type=merge", "-p", `{"spec":{"claimPropertySets":`+properties+`}}`)
			c.await(t, "storageprofile", temporaryClass, "", func(data []byte) bool {
				var profile preflightStorageProfile
				if json.Unmarshal(data, &profile) != nil {
					return false
				}
				want := []json.RawMessage{}
				if json.Unmarshal([]byte(properties), &want) != nil {
					return false
				}
				left, _ := json.Marshal(profile.Status.ClaimPropertySets)
				right, _ := json.Marshal(want)
				return string(left) == string(right)
			})
		}
		request := func(claim StorageClaim) []StorageRequest {
			claim.StorageClassName = &temporaryClass
			return []StorageRequest{{Identity: "profile", ExpectedClass: temporaryClass, Namespace: targetNS, CDI: true, CDIStorage: true, Claim: claim}}
		}
		patchProfile(`[{"accessModes":["ReadWriteMany"],"volumeMode":"Filesystem"}]`)
		c.check(t, request(StorageClaim{}), false, false)
		patchProfile(`[{"accessModes":["ReadWriteMany"],"volumeMode":"Block"}]`)
		c.check(t, request(StorageClaim{VolumeMode: "Filesystem"}), true, false)
		c.check(t, request(StorageClaim{VolumeMode: "Filesystem", AccessModes: []string{"ReadWriteOnce"}}), false, false)

		_, patchErr := SnapshotCommand(t.Context(), c.kubeconfig, nil, "patch", "storageprofile", temporaryClass, "--type=merge", "-p", `{"spec":{"claimPropertySets":[{"volumeMode":"Block"}]}}`)
		if patchErr == nil {
			t.Fatal("cluster admitted a StorageProfile property set without required accessModes")
		}
		t.Log("API admission rejects a configured property set with missing accessModes")
		c.create(t, map[string]any{"apiVersion": "storage.k8s.io/v1", "kind": "StorageClass", "metadata": map[string]any{"name": unknownClass}, "provisioner": "storage-preflight.invalid.csi"})
		c.run(t, nil, "wait", "--for=create", "storageprofile/"+unknownClass, "--timeout=60s")
		unknown := []StorageRequest{{Identity: "unknown-profile", ExpectedClass: unknownClass, Namespace: targetNS, CDI: true, CDIStorage: true, Claim: StorageClaim{StorageClassName: &unknownClass}}}
		c.check(t, unknown, true, false)
		c.run(t, nil, "delete", "storageclass", unknownClass, "--wait=true", "--timeout=60s")
		c.check(t, unknown, true, false)
		patchProfile(`[{"accessModes":["ReadWriteMany"],"volumeMode":"Filesystem"}]`)
		c.check(t, request(StorageClaim{}), false, false)
	})
	t.Run("snapshot classes", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			fail bool
		}{{goodVSC, false}, {badVSC, true}, {prefix + "-missing", true}} {
			_, err := ResolveSnapshot(t.Context(), nas, tc.name, c.kubeconfig, true)
			if (err != nil) != tc.fail {
				t.Fatalf("class=%s err=%v expected failure=%t", tc.name, err, tc.fail)
			}
		}
		for _, name := range []string{badVSC, goodVSC} {
			c.run(t, nil, "patch", "storageprofile", temporaryClass, "--type=merge", "-p", fmt.Sprintf(`{"spec":{"snapshotClass":%q}}`, name))
			expected := name
			if name == badVSC {
				expected = snapshotClass
			}
			c.await(t, "storageprofile", temporaryClass, "", func(data []byte) bool {
				var profile preflightStorageProfile
				return json.Unmarshal(data, &profile) == nil && profile.Status.SnapshotClass == expected
			})
			selection, err := ResolveSnapshot(t.Context(), temporaryClass, "", c.kubeconfig, true)
			if err != nil || selection.SnapshotClass != expected {
				t.Fatalf("profile preference %s: selected=%s err=%v", name, selection.SnapshotClass, err)
			}
			if name == badVSC {
				t.Log("CDI ignores the wrong-driver profile preference and retains a compatible effective snapshot class")
			}
		}
	})
	t.Run("patch source references", func(t *testing.T) {
		patch := func(name string) {
			c.run(t, nil, "patch", "datasource", aliases[scratch], "-n", sourceNS, "--type=merge", "-p", fmt.Sprintf(`{"spec":{"source":{"pvc":{"name":%q,"namespace":%q}}}}`, name, sourceNS))
		}
		patch("missing-pvc")
		c.await(t, "datasource", aliases[scratch], sourceNS, func(data []byte) bool { return !readySource(data) })
		c.check(t, requestsFor(scratch, scratch), true, false)
		patch(scratchPVC)
		c.await(t, "datasource", aliases[scratch], sourceNS, readySource)
		c.check(t, requestsFor(scratch, scratch), false, false)
		requests := requestsFor(scratch, scratch)
		requests[0].Source.Namespace = targetNS
		c.check(t, requests, true, false)
	})
	pending := "pending-source"
	c.create(t, map[string]any{"apiVersion": "v1", "kind": "PersistentVolumeClaim", "metadata": map[string]any{"name": pending, "namespace": sourceNS}, "spec": map[string]any{"storageClassName": prefix + "-missing", "accessModes": []string{"ReadWriteOnce"}, "resources": map[string]any{"requests": map[string]any{"storage": "1Gi"}}}})
	t.Run("pending and missing PVC", func(t *testing.T) {
		request := requestsFor(scratch, scratch)[:1]
		request[0].SourceKind = "pvc"
		request[0].Source = storageReference{Name: pending, Namespace: sourceNS}
		c.check(t, request, true, false)
		c.run(t, nil, "delete", "pvc", pending, "-n", sourceNS, "--wait=true", "--timeout=60s")
		c.check(t, request, true, false)
	})
	t.Run("clone authorization patches", func(t *testing.T) {
		c.create(t, map[string]any{"apiVersion": "v1", "kind": "ServiceAccount", "metadata": map[string]any{"name": "reader", "namespace": sourceNS}})
		rules := []map[string]any{
			{"apiGroups": []string{"storage.k8s.io"}, "resources": []string{"storageclasses"}, "verbs": []string{"get", "list"}},
			{"apiGroups": []string{"cdi.kubevirt.io"}, "resources": []string{"datasources", "storageprofiles", "cdiconfigs"}, "verbs": []string{"get"}},
			{"apiGroups": []string{"snapshot.storage.k8s.io"}, "resources": []string{"volumesnapshots", "volumesnapshotcontents"}, "verbs": []string{"get"}},
			{"apiGroups": []string{""}, "resources": []string{"persistentvolumeclaims", "persistentvolumes"}, "verbs": []string{"get"}},
		}
		c.create(t, map[string]any{"apiVersion": "rbac.authorization.k8s.io/v1", "kind": "ClusterRole", "metadata": map[string]any{"name": role}, "rules": rules})
		c.create(t, map[string]any{"apiVersion": "rbac.authorization.k8s.io/v1", "kind": "ClusterRoleBinding", "metadata": map[string]any{"name": role}, "subjects": []map[string]any{{"kind": "ServiceAccount", "name": "reader", "namespace": sourceNS}}, "roleRef": map[string]any{"apiGroup": "rbac.authorization.k8s.io", "kind": "ClusterRole", "name": role}})
		data := c.run(t, nil, "config", "view", "--raw", "--minify", "--flatten", "-o", "json")
		var kube map[string]any
		if err := json.Unmarshal(data, &kube); err != nil {
			t.Fatal(err)
		}
		users := kube["users"].([]any)
		user := users[0].(map[string]any)["user"].(map[string]any)
		user["as"] = "system:serviceaccount:" + sourceNS + ":reader"
		user["as-groups"] = []string{"system:authenticated", "system:serviceaccounts", "system:serviceaccounts:" + sourceNS}
		impersonated := filepath.Join(t.TempDir(), "reader-kubeconfig.json")
		data, err = json.Marshal(kube)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(impersonated, data, 0600); err != nil {
			t.Fatal(err)
		}
		reader := storageLiveCluster{kubeconfig: impersonated}
		reader.check(t, requestsFor(scratch, scratch), true, false)
		grant := map[string]any{"apiGroups": []string{"cdi.kubevirt.io"}, "resources": []string{"datavolumes/source"}, "verbs": []string{"create"}}
		setRules := func(values []map[string]any) {
			patch, _ := json.Marshal(map[string]any{"rules": values})
			c.run(t, nil, "patch", "clusterrole", role, "--type=merge", "-p", string(patch))
			time.Sleep(time.Second)
		}
		setRules(append(append([]map[string]any{}, rules...), grant))
		reader.check(t, requestsFor(scratch, scratch), false, false)
		setRules(rules)
		reader.check(t, requestsFor(scratch, scratch), true, false)
	})
	t.Run("snapshot survives source deletion", func(t *testing.T) {
		snap := "retained-source"
		c.create(t, map[string]any{"apiVersion": "snapshot.storage.k8s.io/v1", "kind": "VolumeSnapshot", "metadata": map[string]any{"name": snap, "namespace": sourceNS}, "spec": map[string]any{"volumeSnapshotClassName": snapshotClass, "source": map[string]any{"persistentVolumeClaimName": scratchPVC}}})
		c.run(t, nil, "wait", "--for=jsonpath={.status.readyToUse}=true", "volumesnapshot/"+snap, "-n", sourceNS, "--timeout=90s")
		request := requestsFor(scratch, scratch)[:1]
		request[0].SourceKind = "snapshot"
		request[0].Source = storageReference{Name: snap, Namespace: sourceNS}
		c.check(t, request, false, false)
		c.run(t, nil, "delete", "pvc", scratchPVC, "-n", sourceNS, "--wait=true", "--timeout=60s")
		c.check(t, request, false, true)
		c.run(t, nil, "delete", "volumesnapshot", snap, "-n", sourceNS, "--wait=true", "--timeout=60s")
		c.check(t, request, true, false)
	})
}
