package virtbench

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"strconv"
	"time"

	"github.com/openshift-storage-cert/storage-cert-harness/internal/clustercheck"
	"github.com/openshift-storage-cert/storage-cert-harness/internal/core"
	"github.com/openshift-storage-cert/storage-cert-harness/internal/manifest"
)

type cloneSource = clustercheck.NamespacedName

type snapshotClone struct {
	Target    core.TestRequirement
	Selection clustercheck.SnapshotSelection
	Template  map[string]any
	Volumes   []map[string]any
	Sources   map[int]cloneSource
	Snapshots map[string]cloneSource
	Created   []cloneSource
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
	data, _, err := vmSourceTemplate(ctx, tr)
	if err != nil {
		return nil, err
	}
	c := &snapshotClone{Target: tr, Selection: selection, Sources: map[int]cloneSource{}, Snapshots: map[string]cloneSource{}}
	values, err := resolvePlanTemplateValues("datasource-clone", rc, tr)
	if err != nil {
		return nil, err
	}
	data, err = renderVMTemplate("snapshot-source.yaml", data, values)
	if err != nil {
		return nil, err
	}
	documents, err := manifest.Documents(data)
	if err != nil {
		return nil, fmt.Errorf("snapshot clone VM template: %w", err)
	}
	if len(documents) != 1 {
		return nil, fmt.Errorf("snapshot clone VM template must contain one YAML document")
	}
	c.Template = documents[0]
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
			obj, err := clustercheck.GetPVC(ctx, selection.Kubeconfig, pvc)
			if err != nil {
				return nil, err
			}
			if _, err := clustercheck.ResolveSnapshot(ctx, obj.Spec.StorageClassName, selection.SnapshotClass, selection.Kubeconfig, false); err != nil {
				return nil, fmt.Errorf("source PVC %s/%s: %w", pvc.Namespace, pvc.Name, err)
			}
			c.Sources[i] = pvc
		} else if snapshot, ok := source["snapshot"]; ok {
			obj, err := clustercheck.GetVolumeSnapshot(ctx, selection.Kubeconfig, snapshot)
			if err != nil {
				return nil, err
			}
			if obj.Spec.VolumeSnapshotClassName != selection.SnapshotClass {
				return nil, fmt.Errorf("source snapshot %s/%s uses snapshot class %q, requested %q", snapshot.Namespace, snapshot.Name, obj.Spec.VolumeSnapshotClassName, selection.SnapshotClass)
			}
			c.Snapshots[strconv.Itoa(i)] = snapshot
		} else {
			return nil, fmt.Errorf("use_snapshot requires PVC or snapshot clone sources")
		}
	}
	if cloned == 0 {
		return nil, fmt.Errorf("use_snapshot requires at least one PVC or snapshot clone source in dataVolumeTemplates")
	}
	return c, nil
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
		var source map[string]cloneSource
		for _, ref := range refs {
			ds, err := loadDataSource(ctx, c.Selection.Kubeconfig, ref)
			if err != nil {
				return nil, err
			}
			if source != nil && !maps.Equal(source, ds.Spec.Source) {
				return nil, fmt.Errorf("local DataSource %s resolves to different sources; specify sourceRef.namespace in the template", name)
			}
			source = ds.Spec.Source
		}
		return source, nil
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
	for i := range c.Volumes {
		source, ok := c.Sources[i]
		if !ok {
			continue
		}
		templateSource, err := templates.ReadFile(volumeSnapshotTemplatePath)
		if err != nil {
			return "", err
		}
		data, err := manifest.Render(vmRenderer, "volume-snapshot.yaml", templateSource, map[string]any{
			"NAMESPACE": source.Namespace, "PVC_NAME": source.Name, "SNAPSHOT_CLASS": c.Selection.SnapshotClass,
		})
		if err != nil {
			return "", err
		}
		made, err := clustercheck.CreateVolumeSnapshot(ctx, c.Selection.Kubeconfig, data)
		if err != nil {
			return "", err
		}
		madeRef := cloneSource{Name: made.Metadata.Name, Namespace: made.Metadata.Namespace}
		c.Created = append(c.Created, madeRef)
		if err := clustercheck.WaitForVolumeSnapshotReady(ctx, c.Selection.Kubeconfig, madeRef, 5*time.Minute); err != nil {
			return "", err
		}
		c.Snapshots[strconv.Itoa(i)] = madeRef
	}
	templateSource, err := templates.ReadFile(snapshotVMTemplatePath)
	if err != nil {
		return "", err
	}
	data, err := manifest.Render(vmRenderer, "snapshot-vm.yaml", templateSource, map[string]any{"VM": c.Template, "SNAPSHOTS": c.Snapshots})
	if err != nil {
		return "", err
	}
	return writeTemplateFile(root, "snapshot-vm.yaml", data)
}

func (c *snapshotClone) cleanup(ctx context.Context, rc *core.RunCtx) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	for _, source := range c.Created {
		if err := clustercheck.DeleteVolumeSnapshot(ctx, c.Selection.Kubeconfig, source); err != nil {
			rc.Logger.Warn("virtbench: delete source snapshot", "name", source.Name, "err", err)
		}
	}
}
