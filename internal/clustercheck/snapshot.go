package clustercheck

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/openshift-storage-cert/storage-cert-harness/internal/core"
)

// SnapshotSelection is a validated selection. Kubeconfig is kept out of reports.
type SnapshotSelection struct {
	StorageClass  string
	SnapshotClass string
	Kubeconfig    string
}

// ResolveSnapshot validates an explicit class and its driver. Without an explicit
// class, VM snapshots follow KubeVirt's StorageProfile selection, then the driver
// default (or sole matching class); direct snapshots follow the driver default.
// This is read-only, including when called by the preflight subcommand.
func ResolveSnapshot(ctx context.Context, storageClass, configured, kubeconfig string, vmSnapshot bool) (SnapshotSelection, error) {
	s := SnapshotSelection{StorageClass: storageClass, Kubeconfig: kubeconfig}
	if storageClass == "" {
		return s, fmt.Errorf("snapshot class: storage_class is required")
	}
	sc, err := GetStorageClass(ctx, kubeconfig, storageClass)
	if err != nil {
		return s, err
	}
	provisioner, _ := sc["provisioner"].(string)
	name := configured
	if name == "" && vmSnapshot {
		profile, err := GetStorageProfile(ctx, kubeconfig, storageClass)
		if err != nil && !errors.Is(err, ErrNotFound) {
			return s, err
		}
		if err == nil {
			name = profile.Status.SnapshotClass
		}
	}

	if name != "" {
		vsc, err := GetVolumeSnapshotClass(ctx, kubeconfig, name)
		if err != nil {
			return s, fmt.Errorf("snapshot class %q lookup failed: %w", name, err)
		}
		if provisioner == "" || vsc.Driver != provisioner {
			return s, fmt.Errorf("snapshot class %q driver %q does not match storage class %q provisioner %q", name, vsc.Driver, storageClass, provisioner)
		}
		s.SnapshotClass = name
		return s, nil
	}
	classes, err := ListVolumeSnapshotClasses(ctx, kubeconfig)
	if err != nil {
		return s, err
	}
	var matches, defaults []string
	for _, vsc := range classes {
		if vsc.Driver != provisioner || provisioner == "" {
			continue
		}
		matches = append(matches, vsc.Metadata.Name)
		if vsc.Metadata.Annotations["snapshot.storage.kubernetes.io/is-default-class"] == "true" {
			defaults = append(defaults, vsc.Metadata.Name)
		}
	}
	switch {
	case len(defaults) == 1:
		s.SnapshotClass = defaults[0]
	case vmSnapshot && len(matches) == 1:
		s.SnapshotClass = matches[0]
	default:
		return s, fmt.Errorf("storage class %q: cannot resolve snapshot class (%d matching classes, %d defaults); configure backend snapshot_class", storageClass, len(matches), len(defaults))
	}
	return s, nil
}

func (s SnapshotSelection) Finding() core.Finding {
	return core.Finding{Level: "info", Message: fmt.Sprintf("storage_class=%s snapshot_class=%s", s.StorageClass, s.SnapshotClass)}
}

// WaitForCreateSupported reports whether the local client can run
// "wait --for=create", which snapshot storage provisioning depends on. The condition
// arrived in kubectl 1.31; an older client gets through preflight and then
// fails partway into provisioning, after the run has started and cluster
// objects exist.
//
// This probes the condition rather than comparing versions on purpose:
// "oc version --client" reports an OpenShift release (4.17.3), not the
// Kubernetes version the condition belongs to, so there is no number to
// compare. Waiting on an object that does not exist is side-effect free, and
// a zero timeout returns immediately either way.
func WaitForCreateSupported(ctx context.Context, kubeconfig string) core.Finding {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	err := WaitForStorageProfileCreated(ctx, kubeconfig, "storage-cert-preflight-probe", 0)
	if err != nil && strings.Contains(err.Error(), `unrecognized condition: "create"`) {
		return core.Finding{Level: "error", Message: "kubectl/oc is too old for snapshot storage preparation: " +
			`it does not support "wait --for=create" (kubectl 1.31+ / a matching oc). ` +
			"Provisioning would fail partway through the run."}
	}
	// Any other outcome — a timeout, a missing object, no cluster reach — means
	// the condition itself was understood, which is all this checks.
	return core.Finding{Level: "info", Message: `client supports "wait --for=create"`}
}

func (s SnapshotSelection) CleanupVMStorage(ctx context.Context, created string) error {
	if created == "" {
		return nil
	}
	return DeleteStorageClass(ctx, s.Kubeconfig, created)
}
