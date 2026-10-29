package virtbench

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/openshift-storage-cert/storage-cert-harness/internal/clustercheck"
	"github.com/openshift-storage-cert/storage-cert-harness/internal/core"
	"github.com/openshift-storage-cert/storage-cert-harness/internal/manifest"
)

const imageNamespace = "openshift-virtualization-os-images"

func parseDataSource(value string) (cloneSource, error) {
	parts := strings.Split(value, "/")
	ref := cloneSource{Namespace: imageNamespace}
	switch len(parts) {
	case 1:
		ref.Name = parts[0]
	case 2:
		ref.Namespace, ref.Name = parts[0], parts[1]
	default:
		return ref, fmt.Errorf("datasource %q: use name or namespace/name", value)
	}
	if !validDNSName(ref.Name, 253, true) || !validDNSName(ref.Namespace, 63, false) {
		return ref, fmt.Errorf("datasource %q: invalid Kubernetes name or namespace; use name or namespace/name", value)
	}
	return ref, nil
}

// resolveLocalDataSources follows CDI's default to each receiving DataVolume's namespace.
func resolveLocalDataSources(tr core.TestRequirement, refs []cloneSource) ([]cloneSource, error) {
	var resolved []cloneSource
	for _, ref := range refs {
		if ref.Namespace != "" {
			resolved = append(resolved, ref)
			continue
		}
		namespaces, err := vmTargetNamespaces(tr)
		if err != nil {
			return nil, err
		}
		for _, ns := range namespaces {
			resolved = append(resolved, cloneSource{Name: ref.Name, Namespace: ns})
		}
	}
	return resolved, nil
}

func vmTargetNamespaces(tr core.TestRequirement) ([]string, error) {
	sc, ok := scenarioFor(tr)
	if !ok || sc.VMRange == nil {
		return nil, fmt.Errorf("cannot resolve local DataSource namespace for %s; specify sourceRef.namespace or datasource: namespace/name", tr.ID)
	}
	start, end, err := sc.VMRange(tr)
	if err != nil {
		return nil, err
	}
	prefix := strParamOr(tr, "namespace_prefix", sc.NSPrefix)
	var namespaces []string
	for i := start; i <= end; i++ {
		namespaces = append(namespaces, fmt.Sprintf("%s-%d", prefix, i))
	}
	return namespaces, nil
}

func checkDataSource(ctx context.Context, kubeconfig string, ref cloneSource) error {
	ds, err := loadDataSource(ctx, kubeconfig, ref)
	if err != nil {
		return err
	}
	ready := false
	for _, c := range ds.Status.Conditions {
		if c.Type == "Ready" && c.Status == "True" {
			ready = true
		}
	}
	if !ready {
		return fmt.Errorf("DataSource %s/%s is not Ready", ref.Namespace, ref.Name)
	}
	for kind, source := range ds.Spec.Source {
		if source.Name == "" {
			continue
		}
		switch kind {
		case "pvc":
			var pvc struct {
				Status struct {
					Phase string `json:"phase"`
				} `json:"status"`
			}
			if err := getCloneObject(ctx, kubeconfig, &pvc, "pvc", source); err != nil {
				return err
			}
			if pvc.Status.Phase != "Bound" {
				return fmt.Errorf("DataSource PVC %s/%s is not Bound", source.Namespace, source.Name)
			}
		case "snapshot":
			var snap struct {
				Status struct {
					Ready bool `json:"readyToUse"`
				} `json:"status"`
			}
			if err := getCloneObject(ctx, kubeconfig, &snap, "volumesnapshot", source); err != nil {
				return err
			}
			if !snap.Status.Ready {
				return fmt.Errorf("DataSource snapshot %s/%s is not readyToUse", source.Namespace, source.Name)
			}
		default:
			return fmt.Errorf("DataSource %s/%s has unsupported source %s", ref.Namespace, ref.Name, kind)
		}
		return nil
	}
	return fmt.Errorf("DataSource %s/%s has no PVC or snapshot source", ref.Namespace, ref.Name)
}

func validDNSName(value string, max int, dots bool) bool {
	if len(value) == 0 || len(value) > max {
		return false
	}
	pattern := `^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	if dots {
		pattern = `^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`
	}
	return regexp.MustCompile(pattern).MatchString(value)
}

type dataSource struct {
	Spec struct {
		Source map[string]cloneSource `json:"source"`
	} `json:"spec"`
	Status struct {
		Conditions []struct {
			Type   string `json:"type"`
			Status string `json:"status"`
		} `json:"conditions"`
	} `json:"status"`
}

func getCloneObject(ctx context.Context, kubeconfig string, obj any, kind string, source cloneSource) error {
	if source.Name == "" || source.Namespace == "" {
		return fmt.Errorf("%s source requires name and namespace", kind)
	}
	out, err := clustercheck.SnapshotCommand(ctx, kubeconfig, nil, "get", kind, source.Name, "-n", source.Namespace, "-o", "json")
	if err != nil {
		return err
	}
	return json.Unmarshal(out, obj)
}

func loadDataSource(ctx context.Context, kubeconfig string, ref cloneSource) (dataSource, error) {
	var ds dataSource
	if err := getCloneObject(ctx, kubeconfig, &ds, "datasource", ref); err != nil {
		return ds, err
	}
	for kind, source := range ds.Spec.Source {
		if source.Namespace == "" {
			source.Namespace = ref.Namespace
			ds.Spec.Source[kind] = source
		}
	}
	return ds, nil
}

// selectVMSource keeps source changes in a template and inspects the rendered result read-only.
func selectVMSource(data []byte, selection string, defaults bool) ([]byte, []cloneSource, error) {
	docs, err := manifest.Documents(data)
	if err != nil {
		return nil, nil, err
	}
	if len(docs) != 1 {
		return nil, nil, fmt.Errorf("VM template must contain one YAML document")
	}
	var selected cloneSource
	if selection != "" {
		selected, err = parseDataSource(selection)
		if err != nil {
			return nil, nil, err
		}
	}
	spec, _ := docs[0]["spec"].(map[string]any)
	volumes, _ := spec["dataVolumeTemplates"].([]any)
	var refs []cloneSource
	for _, item := range volumes {
		volume, _ := item.(map[string]any)
		v, _ := volume["spec"].(map[string]any)
		ref, _ := v["sourceRef"].(map[string]any)
		if ref["kind"] != "DataSource" {
			continue
		}
		name, _ := ref["name"].(string)
		ns, _ := ref["namespace"].(string)
		if selection != "" {
			name, ns = selected.Name, selected.Namespace
		}
		if name == "" {
			return nil, nil, fmt.Errorf("DataSource sourceRef requires a name")
		}
		refs = append(refs, cloneSource{Name: name, Namespace: ns})
	}
	if selection != "" && len(refs) == 0 {
		return nil, nil, fmt.Errorf("datasource %q: VM template has no DataSource sourceRef; supply a template with a DataSource reference", selection)
	}
	if (selection == "" && !defaults) || len(volumes) == 0 {
		return data, refs, nil
	}
	rendered, err := manifest.Render(vmRenderer, "selected-vm.yaml", []byte(selectedVMTemplate), map[string]any{
		"VM": docs[0], "DATASOURCE": selected, "DEFAULTS": defaults,
	})
	return rendered, refs, err
}

func datasourcePreflight(ctx context.Context, rc *core.RunCtx, tr core.TestRequirement) ([]cloneSource, error) {
	source, defaults, err := vmSourceTemplate(ctx, tr)
	if err != nil {
		return nil, err
	}
	command := "datasource-clone"
	if tr.ID == "TR-STOR-001" {
		command = "disk-ops"
	}
	if tr.ID == "TR-STOR-002" {
		command = "fio"
	}
	values, err := resolvePlanTemplateValues(command, rc, tr)
	if err != nil {
		return nil, err
	}
	namespaces, err := vmTargetNamespaces(tr)
	if err != nil {
		return nil, err
	}
	values["TARGET_NODE"] = strParamOr(tr, "target_node", "")
	values["NODE_NAME"] = strParamOr(tr, "node", "")
	values["HOSTNAME_LABEL"] = strParamOr(tr, "node_hostname_label", "")
	var refs []cloneSource
	for _, ns := range namespaces {
		values["NAMESPACE"] = ns
		rendered, err := renderVMTemplate("datasource-preflight.yaml", source, values)
		if err != nil {
			return nil, err
		}
		_, found, err := selectVMSource(rendered, strParamOr(tr, "datasource", ""), defaults)
		if err != nil {
			return nil, err
		}
		for _, ref := range found {
			if ref.Namespace == "" {
				ref.Namespace = ns
			}
			refs = append(refs, ref)
		}
	}
	checked := map[cloneSource]bool{}
	var unique []cloneSource
	for _, ref := range refs {
		if checked[ref] {
			continue
		}
		checked[ref] = true
		if _, err := parseDataSource(ref.Namespace + "/" + ref.Name); err != nil {
			return nil, err
		}
		if err := checkDataSource(ctx, strParamOr(tr, "kubeconfig", ""), ref); err != nil {
			return nil, err
		}
		unique = append(unique, ref)
	}
	return unique, nil
}
