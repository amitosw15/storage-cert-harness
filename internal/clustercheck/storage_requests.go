package clustercheck

import (
	"encoding/json"
	"fmt"

	"github.com/openshift-storage-cert/storage-cert-harness/internal/core"
	"github.com/openshift-storage-cert/storage-cert-harness/internal/manifest"
)

// StorageClaim preserves an omitted class separately from an explicitly empty class.
type StorageClaim struct {
	StorageClassName *string  `json:"storageClassName"`
	AccessModes      []string `json:"accessModes"`
	VolumeMode       string   `json:"volumeMode"`
}

// StorageRequest describes one target disk without depending on a concrete tool.
type StorageRequest struct {
	Identity               string
	Namespace              string
	ExpectedClass          string
	Claim                  StorageClaim
	CDIStorage             bool
	CDI                    bool
	SourceKind             string
	Source                 NamespacedName
	ScratchRequired        bool
	RequireSameSourceClass bool
	ContentType            string
}

// StorageRequests extracts disks from prepared PVCs, DataVolumes, and VM templates.
func StorageRequests(data []byte, expectedClass, namespace string) ([]StorageRequest, error) {
	documents, err := manifest.Documents(data)
	if err != nil {
		return nil, err
	}
	var requests []StorageRequest
	add := func(identity, ns string, spec map[string]any, dv bool) error {
		request := StorageRequest{Identity: identity, Namespace: ns, ExpectedClass: expectedClass, RequireSameSourceClass: true}
		claim := spec
		if dv {
			request.CDI = true
			request.ContentType, _ = spec["contentType"].(string)
			if storage, ok := spec["storage"].(map[string]any); ok {
				claim = storage
				request.CDIStorage = true
			} else {
				claim, _ = spec["pvc"].(map[string]any)
			}
			if claim == nil {
				return fmt.Errorf("%s: DataVolume requires storage or pvc", identity)
			}
			if ref, ok := spec["sourceRef"].(map[string]any); ok {
				request.SourceKind, _ = ref["kind"].(string)
				request.Source.Name, _ = ref["name"].(string)
				request.Source.Namespace, _ = ref["namespace"].(string)
			} else if source, ok := spec["source"].(map[string]any); ok {
				for _, kind := range []string{"pvc", "snapshot"} {
					if ref, ok := source[kind].(map[string]any); ok {
						request.SourceKind = kind
						request.Source.Name, _ = ref["name"].(string)
						request.Source.Namespace, _ = ref["namespace"].(string)
					}
				}
				_, upload := source["upload"]
				registry, registrySource := source["registry"].(map[string]any)
				request.ScratchRequired = upload || (registrySource && registry["pullMethod"] != "node")
			}
			if request.Source.Namespace == "" {
				request.Source.Namespace = ns
			}
		}
		if !dv {
			for _, key := range []string{"dataSourceRef", "dataSource"} {
				if ref, ok := spec[key].(map[string]any); ok && request.SourceKind == "" {
					request.SourceKind, _ = ref["kind"].(string)
					request.Source.Name, _ = ref["name"].(string)
					request.Source.Namespace, _ = ref["namespace"].(string)
					if request.Source.Namespace == "" {
						request.Source.Namespace = ns
					}
				}
			}
		}
		raw, err := json.Marshal(claim)
		if err != nil {
			return err
		}
		if err := json.Unmarshal(raw, &request.Claim); err != nil {
			return fmt.Errorf("%s storage: %w", identity, err)
		}
		requests = append(requests, request)
		return nil
	}
	for _, doc := range documents {
		metadata, _ := doc["metadata"].(map[string]any)
		name, _ := metadata["name"].(string)
		ns, _ := metadata["namespace"].(string)
		if ns == "" {
			ns = namespace
		}
		spec, _ := doc["spec"].(map[string]any)
		switch doc["kind"] {
		case "PersistentVolumeClaim", "DataVolume":
			if err := add(fmt.Sprintf("%s/%s", doc["kind"], name), ns, spec, doc["kind"] == "DataVolume"); err != nil {
				return nil, err
			}
		case "VirtualMachine":
			volumes, _ := spec["dataVolumeTemplates"].([]any)
			for _, item := range volumes {
				volume, _ := item.(map[string]any)
				meta, _ := volume["metadata"].(map[string]any)
				disk, _ := meta["name"].(string)
				diskSpec, _ := volume["spec"].(map[string]any)
				if err := add("VirtualMachine/"+name+"/"+disk, ns, diskSpec, true); err != nil {
					return nil, err
				}
			}
		}
	}
	return requests, nil
}

// ApplyStoragePolicy applies the selected TR's policy to every extracted disk.
func ApplyStoragePolicy(requests []StorageRequest, tr core.TestRequirement) error {
	value, exists := tr.Params["require_same_source_storage_class"]
	required := true
	if exists {
		var ok bool
		required, ok = value.(bool)
		if !ok {
			return fmt.Errorf("%s: require_same_source_storage_class must be a boolean", tr.ID)
		}
	}
	for i := range requests {
		requests[i].RequireSameSourceClass = required
		if tr.ID != "" {
			requests[i].Identity = core.ExecutionLabel(tr.ID, tr.Variant) + "/" + requests[i].Identity
		}
	}
	return nil
}
