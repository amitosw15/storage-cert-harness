package clustercheck

import "testing"

func TestStorageRequests(t *testing.T) {
	data := []byte(`apiVersion: kubevirt.io/v1
kind: VirtualMachine
metadata: {name: vm, namespace: workloads}
spec:
  dataVolumeTemplates:
  - metadata: {name: root}
    spec:
      sourceRef: {kind: DataSource, name: golden, namespace: images}
      storage:
        storageClassName: nas
        accessModes: [ReadWriteMany]
  - metadata: {name: data}
    spec:
      source: {blank: {}}
      storage: {storageClassName: nas, accessModes: [ReadWriteMany]}
---
apiVersion: cdi.kubevirt.io/v1beta1
kind: DataVolume
metadata: {name: registry}
spec:
  source: {registry: {url: 'docker://example/image'}}
  pvc: {storageClassName: '', accessModes: [ReadWriteOnce]}
---
apiVersion: v1
kind: PersistentVolumeClaim
metadata: {name: plain}
spec: {accessModes: [ReadWriteOnce]}
`)
	requests, err := StorageRequests(data, "nas", "fallback")
	if err != nil || len(requests) != 4 {
		t.Fatalf("got %+v, %v", requests, err)
	}
	if requests[0].Source.Namespace != "images" || requests[0].SourceKind != "DataSource" || !requests[0].CDIStorage || *requests[0].Claim.StorageClassName != "nas" {
		t.Fatalf("root: %+v", requests[0])
	}
	if requests[1].SourceKind != "" || requests[1].Namespace != "workloads" {
		t.Fatalf("blank: %+v", requests[1])
	}
	if !requests[2].ScratchRequired || requests[2].CDIStorage || requests[2].Claim.StorageClassName == nil || *requests[2].Claim.StorageClassName != "" {
		t.Fatalf("legacy: %+v", requests[2])
	}
	if requests[3].Claim.StorageClassName != nil || requests[3].CDIStorage || requests[3].Namespace != "fallback" {
		t.Fatalf("PVC: %+v", requests[3])
	}
}
