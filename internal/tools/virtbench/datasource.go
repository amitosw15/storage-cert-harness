package virtbench

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/openshift-storage-cert/storage-cert-harness/internal/clustercheck"
	"github.com/openshift-storage-cert/storage-cert-harness/internal/core"
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

var templateToken = regexp.MustCompile(`\{\{[^{}]+\}\}`)

// rewriteVMSource edits only DataVolume sources and default storage volume modes.
func rewriteVMSource(data []byte, selection string, defaults bool) ([]byte, []cloneSource, error) {
	var selected cloneSource
	var err error
	if selection != "" {
		selected, err = parseDataSource(selection)
		if err != nil {
			return nil, nil, err
		}
	}
	tokens := map[string]string{}
	clean := templateToken.ReplaceAllStringFunc(string(data), func(token string) string {
		key := fmt.Sprintf("HARNESSPLACEHOLDER%dTOKEN", len(tokens))
		tokens[key] = token
		return key
	})
	var refs []cloneSource
	var walk func(*yaml.Node)
	walk = func(n *yaml.Node) {
		if n.Kind == yaml.MappingNode {
			for i := 0; i+1 < len(n.Content); i += 2 {
				if n.Content[i].Value == "dataVolumeTemplates" {
					for _, volume := range n.Content[i+1].Content {
						spec := yamlField(volume, "spec")
						if defaults {
							for _, key := range []string{"storage", "pvc"} {
								yamlDelete(yamlField(spec, key), "volumeMode")
							}
						}
						ref := yamlField(spec, "sourceRef")
						if kind := yamlField(ref, "kind"); kind != nil && kind.Value == "DataSource" {
							if selection != "" {
								yamlSet(ref, "name", selected.Name)
								yamlSet(ref, "namespace", selected.Namespace)
							}
							name, ns := yamlField(ref, "name"), yamlField(ref, "namespace")
							if name != nil {
								r := cloneSource{Name: name.Value}
								if ns != nil {
									r.Namespace = ns.Value
								}
								refs = append(refs, r)
							}
						}
					}
				}
			}
		}
		for _, c := range n.Content {
			walk(c)
		}
	}
	decoder := yaml.NewDecoder(strings.NewReader(clean))
	var output bytes.Buffer
	encoder := yaml.NewEncoder(&output)
	encoder.SetIndent(2)
	for {
		var doc yaml.Node
		err := decoder.Decode(&doc)
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, nil, fmt.Errorf("VM template: %w", err)
		}
		walk(&doc)
		if err := encoder.Encode(&doc); err != nil {
			return nil, nil, err
		}
	}
	if err := encoder.Close(); err != nil {
		return nil, nil, err
	}
	if selection != "" && len(refs) == 0 {
		return nil, nil, fmt.Errorf("datasource %q: VM template has no DataSource sourceRef; supply vm_template (fio_vm_template for FIO) with a DataSource reference", selection)
	}
	result := output.String()
	for key, token := range tokens {
		result = strings.ReplaceAll(result, key, token)
	}
	return []byte(result), refs, nil
}

func yamlField(n *yaml.Node, key string) *yaml.Node {
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return n.Content[i+1]
		}
	}
	return nil
}
func yamlDelete(n *yaml.Node, key string) {
	if n == nil {
		return
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			n.Content = append(n.Content[:i], n.Content[i+2:]...)
			return
		}
	}
}
func yamlSet(n *yaml.Node, key, value string) {
	if field := yamlField(n, key); field != nil {
		field.Value = value
		return
	}
	n.Content = append(n.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value})
}

func datasourcePreflight(ctx context.Context, tr core.TestRequirement) ([]cloneSource, error) {
	data, defaults, err := vmSourceTemplate(tr)
	if err != nil {
		return nil, err
	}
	_, refs, err := rewriteVMSource(data, strParamOr(tr, "datasource", ""), defaults)
	if err != nil {
		return nil, err
	}
	refs, err = resolveLocalDataSources(tr, refs)
	if err != nil {
		return nil, err
	}
	for _, ref := range refs {
		if _, err := parseDataSource(ref.Namespace + "/" + ref.Name); err != nil {
			return nil, err
		}
		if err := checkDataSource(ctx, strParamOr(tr, "kubeconfig", ""), ref); err != nil {
			return nil, err
		}
	}
	return refs, nil
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
		pattern = `^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$`
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
