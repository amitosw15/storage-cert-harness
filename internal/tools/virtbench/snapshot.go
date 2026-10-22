package virtbench

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/openshift-storage-cert/storage-cert-harness/internal/clustercheck"
	"github.com/openshift-storage-cert/storage-cert-harness/internal/core"
	"github.com/openshift-storage-cert/storage-cert-harness/internal/safefs"
	"github.com/openshift-storage-cert/storage-cert-harness/internal/tools/common"
)

type cloneSource = common.NamespacedSource

type snapshotClone struct {
	Selection clustercheck.SnapshotSelection
	Template  map[string]any
	Volumes   []map[string]any
	Sources   map[int]cloneSource
	Created   []cloneSource
}

func usesSnapshot(tr core.TestRequirement) bool {
	b, _ := core.AsBool(tr.Params["use_snapshot"])
	return b
}

// snapshotClonePreflight binds a selected snapshot class to the VM template.
func snapshotClonePreflight(ctx context.Context, rc *core.RunCtx, tr core.TestRequirement) (*snapshotClone, error) {
	c := &snapshotClone{Sources: map[int]cloneSource{}}
	configured := ""
	if rc != nil && rc.Backend != nil {
		configured = rc.Backend.SnapshotClass
	}
	if storageClass(rc, tr) == "" {
		return nil, fmt.Errorf("snapshot clone: storage_class required")
	}
	selectionCheck := common.SnapshotSupportCheck(common.SnapshotSupport{
		Name: "snapshot clone support", StorageClass: storageClass(rc, tr), SnapshotClass: configured,
		Kubeconfig: strParamOr(tr, "kubeconfig", ""), SnapshotCount: 1,
		Record: func(selection clustercheck.SnapshotSelection) { c.Selection = selection },
	})
	if err := runSnapshotCheck(ctx, selectionCheck); err != nil {
		return nil, err
	}
	data, err := snapshotTemplate(tr)
	if err != nil {
		return nil, err
	}
	// virtbench also substitutes this placeholder before reading YAML.
	data = []byte(strings.ReplaceAll(string(data), "{{STORAGE_CLASS_NAME}}", c.Selection.StorageClass))
	if err := yaml.Unmarshal(data, &c.Template); err != nil {
		return nil, fmt.Errorf("snapshot clone VM template: %w", err)
	}
	spec, _ := c.Template["spec"].(map[string]any)
	volumes, _ := spec["dataVolumeTemplates"].([]any)
	for _, item := range volumes {
		volume, _ := item.(map[string]any)
		v, _ := volume["spec"].(map[string]any)
		if v == nil {
			return nil, fmt.Errorf("snapshot clone: invalid dataVolumeTemplate")
		}
		c.Volumes = append(c.Volumes, v)
	}
	cloned := false
	for i, v := range c.Volumes {
		source, err := c.resolveAndValidateSource(ctx, i, v)
		if err != nil {
			return nil, err
		}
		if source == nil {
			continue
		}
		cloned = true
	}
	if !cloned {
		return nil, fmt.Errorf("use_snapshot requires at least one PVC or snapshot clone source in dataVolumeTemplates")
	}
	return c, nil
}

func (c *snapshotClone) resolveAndValidateSource(ctx context.Context, index int, volume map[string]any) (map[string]common.NamespacedSource, error) {
	var sources map[string]common.NamespacedSource
	if ref, ok := volume["sourceRef"].(map[string]any); ok {
		if ref["kind"] != "DataSource" {
			return nil, fmt.Errorf("use_snapshot: unsupported sourceRef kind %v", ref["kind"])
		}
		name, _ := ref["name"].(string)
		namespace, _ := ref["namespace"].(string)
		err := runSnapshotCheck(ctx, common.DataSourceCheck(common.DataSourceRequirement{
			Source: common.NamespacedSource{Name: name, Namespace: namespace}, Kubeconfig: c.Selection.Kubeconfig,
			Record: func(resolved map[string]common.NamespacedSource) { sources = resolved },
		}))
		if err != nil {
			return nil, err
		}
		if sources == nil {
			return nil, nil
		}
		if len(sources) == 0 {
			return nil, fmt.Errorf("use_snapshot requires PVC or snapshot clone sources")
		}
	} else {
		direct, _ := volume["source"].(map[string]any)
		if _, blank := direct["blank"]; blank {
			return nil, nil
		}
		encoded, err := json.Marshal(volume["source"])
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(encoded, &sources); err != nil {
			return nil, err
		}
	}
	if len(sources) == 0 {
		return nil, fmt.Errorf("use_snapshot: missing clone source")
	}
	if pvc, ok := sources["pvc"]; ok {
		err := runSnapshotCheck(ctx, common.PVCSnapshotCompatibilityCheck(common.SnapshotSourceRequirement{
			Source: pvc, SnapshotClass: c.Selection.SnapshotClass, Kubeconfig: c.Selection.Kubeconfig,
		}))
		if err != nil {
			return nil, err
		}
		c.Sources[index] = pvc
		return sources, nil
	}
	if snapshot, ok := sources["snapshot"]; ok {
		err := runSnapshotCheck(ctx, common.VolumeSnapshotCompatibilityCheck(common.SnapshotSourceRequirement{
			Source: snapshot, SnapshotClass: c.Selection.SnapshotClass, Kubeconfig: c.Selection.Kubeconfig,
		}))
		if err != nil {
			return nil, err
		}
		delete(volume, "sourceRef")
		volume["source"] = map[string]any{"snapshot": map[string]any{"name": snapshot.Name, "namespace": snapshot.Namespace}}
		return sources, nil
	}
	return nil, fmt.Errorf("use_snapshot requires PVC or snapshot clone sources")
}

func runSnapshotCheck(ctx context.Context, check common.PreflightCheck) error {
	for _, finding := range common.RunPreflightChecks(ctx, []common.PreflightCheck{check}) {
		if finding.Level == "error" {
			return fmt.Errorf("%s", finding.Message)
		}
	}
	return nil
}

func snapshotCloneBinding(rc *core.RunCtx, s *session, tr core.TestRequirement) common.PreflightCheck {
	return common.PreflightCheck{
		Name: tr.ID + " snapshot clone",
		Run: func(ctx context.Context) []core.Finding {
			clone, err := snapshotClonePreflight(ctx, rc, tr)
			if err != nil {
				return []core.Finding{{Level: "error", Message: tr.ID + " snapshot clone: " + err.Error()}}
			}
			s.snapshotClone = clone
			return []core.Finding{clone.Selection.Finding()}
		},
	}
}

func snapshotTemplate(tr core.TestRequirement) ([]byte, error) {
	if path := strParamOr(tr, "vm_template", ""); path != "" {
		return safefs.ReadFile(path)
	}
	if tr.ID == "TR-VIRT-018" {
		return []byte(twoDiskVMTemplate), nil
	}
	for _, root := range []string{os.Getenv("VIRTBENCH_REPO"), "/opt/virtbench-runtime", "."} {
		if root == "" {
			continue
		}
		if data, err := safefs.ReadFile(filepath.Join(root, "examples/vm-templates/rhel9-vm-datasource.yaml")); err == nil {
			return data, nil
		}
	}
	return nil, fmt.Errorf("use_snapshot: cannot locate virtbench's default VM template; set vm_template or VIRTBENCH_REPO")
}

// prepare creates only new snapshots; source PVCs and DataSources are read-only.
// Created is populated before waiting so partial failures are cleaned up too.
func (c *snapshotClone) prepare(ctx context.Context, root string) (string, error) {
	for i, v := range c.Volumes {
		source, ok := c.Sources[i]
		if !ok {
			continue
		}
		obj := map[string]any{
			"apiVersion": "snapshot.storage.k8s.io/v1", "kind": "VolumeSnapshot",
			"metadata": map[string]any{"generateName": "storage-cert-clone-", "namespace": source.Namespace,
				"labels": map[string]string{"app.kubernetes.io/managed-by": "storage-cert-harness"}},
			"spec": map[string]any{"volumeSnapshotClassName": c.Selection.SnapshotClass, "source": map[string]string{"persistentVolumeClaimName": source.Name}},
		}
		data, err := json.Marshal(obj)
		if err != nil {
			return "", err
		}
		out, err := clustercheck.SnapshotCommand(ctx, c.Selection.Kubeconfig, data, "create", "-f", "-", "-o", "json")
		if err != nil {
			return "", err
		}
		var made struct {
			Metadata cloneSource `json:"metadata"`
		}
		if err := json.Unmarshal(out, &made); err != nil {
			return "", err
		}
		c.Created = append(c.Created, made.Metadata)
		if _, err := clustercheck.SnapshotCommand(ctx, c.Selection.Kubeconfig, nil, "wait", "volumesnapshot/"+made.Metadata.Name, "-n", made.Metadata.Namespace, "--for=jsonpath={.status.readyToUse}=true", "--timeout=5m"); err != nil {
			return "", err
		}
		delete(v, "sourceRef")
		v["source"] = map[string]any{"snapshot": map[string]any{"name": made.Metadata.Name, "namespace": made.Metadata.Namespace}}
	}
	data, err := yaml.Marshal(c.Template)
	if err != nil {
		return "", err
	}
	path := filepath.Join(root, "snapshot-vm.yaml")
	if err := safefs.WriteFile(path, data, 0o600); err != nil {
		return "", err
	}
	return filepath.Abs(path)
}

func (c *snapshotClone) cleanup(ctx context.Context, rc *core.RunCtx) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	for _, source := range c.Created {
		if _, err := clustercheck.SnapshotCommand(ctx, c.Selection.Kubeconfig, nil, "delete", "volumesnapshot", source.Name, "-n", source.Namespace, "--ignore-not-found", "--wait=false"); err != nil {
			rc.Logger.Warn("virtbench: delete source snapshot", "name", source.Name, "err", err)
		}
	}
}
