package virtbench

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/openshift-storage-cert/storage-cert-harness/internal/clustercheck"
	"github.com/openshift-storage-cert/storage-cert-harness/internal/core"
	"github.com/openshift-storage-cert/storage-cert-harness/internal/safefs"
)

type cloneSource struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
}

type snapshotClone struct {
	Selection clustercheck.SnapshotSelection
	Template  map[string]any
	Volumes   []map[string]any
	Sources   map[int]cloneSource
	Created   []cloneSource
	Target    core.TestRequirement
}

func usesSnapshot(tr core.TestRequirement) bool {
	b, _ := core.AsBool(tr.Params["use_snapshot"])
	return b
}

// snapshotClonePreflight resolves every source without creating resources. The
// released virtbench CLI has no snapshot-class or use-snapshot flags, so the
// adapter supplies a staged VM template with explicit CDI snapshot sources.
func snapshotClonePreflight(ctx context.Context, rc *core.RunCtx, tr core.TestRequirement) (*snapshotClone, error) {
	configured := ""
	if rc.Backend != nil {
		configured = rc.Backend.SnapshotClass
	}
	selection, err := clustercheck.ResolveSnapshot(ctx, storageClass(rc, tr), configured, strParamOr(tr, "kubeconfig", ""), false)
	if err != nil {
		return nil, err
	}
	data, defaults, err := vmSourceTemplate(tr)
	if err != nil {
		return nil, err
	}
	data, _, err = rewriteVMSource(data, selectedDataSource(rc, tr), defaults)
	if err != nil {
		return nil, err
	}
	c := &snapshotClone{Selection: selection, Sources: map[int]cloneSource{}, Target: tr}
	// virtbench also substitutes this placeholder before reading YAML.
	data = []byte(strings.ReplaceAll(string(data), "{{STORAGE_CLASS_NAME}}", selection.StorageClass))
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
	cloned := 0
	for i, v := range c.Volumes {
		source, err := c.resolveSource(ctx, v)
		if err != nil {
			return nil, err
		}
		if source == nil { // blank data disks are not clone sources
			continue
		}
		cloned++
		if pvc, ok := source["pvc"]; ok {
			var obj struct {
				Spec struct {
					StorageClass string `json:"storageClassName"`
				} `json:"spec"`
			}
			if err := c.get(ctx, &obj, "pvc", pvc); err != nil {
				return nil, err
			}
			if _, err := clustercheck.ResolveSnapshot(ctx, obj.Spec.StorageClass, selection.SnapshotClass, selection.Kubeconfig, false); err != nil {
				return nil, fmt.Errorf("source PVC %s/%s: %w", pvc.Namespace, pvc.Name, err)
			}
			c.Sources[i] = pvc
		} else if snapshot, ok := source["snapshot"]; ok {
			var obj struct {
				Spec struct {
					Class string `json:"volumeSnapshotClassName"`
				} `json:"spec"`
			}
			if err := c.get(ctx, &obj, "volumesnapshot", snapshot); err != nil {
				return nil, err
			}
			if obj.Spec.Class != selection.SnapshotClass {
				return nil, fmt.Errorf("source snapshot %s/%s uses snapshot class %q, requested %q", snapshot.Namespace, snapshot.Name, obj.Spec.Class, selection.SnapshotClass)
			}
			delete(v, "sourceRef")
			v["source"] = map[string]any{"snapshot": map[string]any{"name": snapshot.Name, "namespace": snapshot.Namespace}}
		} else {
			return nil, fmt.Errorf("use_snapshot requires PVC or snapshot clone sources")
		}
	}
	if cloned == 0 {
		return nil, fmt.Errorf("use_snapshot requires at least one PVC or snapshot clone source in dataVolumeTemplates")
	}
	return c, nil
}

func vmSourceTemplate(tr core.TestRequirement) ([]byte, bool, error) {
	config := templateConfig(tr)
	if path := strParamOr(tr, config.Param, ""); path != "" {
		data, err := safefs.ReadFile(path)
		return data, false, err
	}
	if config.Render != nil {
		return []byte(config.Render(tr)), true, nil
	}
	if config.Upstream == "" {
		return nil, true, fmt.Errorf("no default VM template configured for %s; set %s", tr.ID, config.Param)
	}
	for _, root := range []string{os.Getenv("VIRTBENCH_REPO"), "/opt/virtbench-runtime", "."} {
		if root == "" {
			continue
		}
		if data, err := safefs.ReadFile(filepath.Join(root, config.Upstream)); err == nil {
			return data, true, nil
		}
	}
	return nil, true, fmt.Errorf("cannot locate virtbench default VM template %s; set %s or VIRTBENCH_REPO", config.Upstream, config.Param)
}

func (c *snapshotClone) get(ctx context.Context, obj any, kind string, source cloneSource) error {
	return getCloneObject(ctx, c.Selection.Kubeconfig, obj, kind, source)
}

func (c *snapshotClone) resolveSource(ctx context.Context, v map[string]any) (map[string]cloneSource, error) {
	if ref, ok := v["sourceRef"].(map[string]any); ok {
		if ref["kind"] != "DataSource" {
			return nil, fmt.Errorf("use_snapshot: unsupported sourceRef kind %v", ref["kind"])
		}
		name, _ := ref["name"].(string)
		ns, _ := ref["namespace"].(string)
		refs, err := resolveLocalDataSources(c.Target, []cloneSource{{Name: name, Namespace: ns}})
		if err != nil {
			return nil, err
		}
		var resolved map[string]cloneSource
		for _, local := range refs {
			ds, err := loadDataSource(ctx, c.Selection.Kubeconfig, local)
			if err != nil {
				return nil, err
			}
			if resolved != nil && !maps.Equal(resolved, ds.Spec.Source) {
				return nil, fmt.Errorf("use_snapshot: namespace-local DataSource %q resolves to different sources across VM namespaces; use sourceRef.namespace or --datasource namespace/name to select a shared source", name)
			}
			resolved = ds.Spec.Source
		}
		return resolved, nil
	}
	source, _ := v["source"].(map[string]any)
	if _, blank := source["blank"]; blank {
		return nil, nil
	}
	data, err := json.Marshal(source)
	if err != nil {
		return nil, err
	}
	var decoded map[string]cloneSource
	if err := json.Unmarshal(data, &decoded); err != nil {
		return nil, err
	}
	if len(decoded) == 0 {
		return nil, fmt.Errorf("use_snapshot: missing clone source")
	}
	return decoded, nil
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
