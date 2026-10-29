package clustercheck

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
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

// SnapshotCommand runs structured arguments with the same kubeconfig as the tool.
func SnapshotCommand(ctx context.Context, kubeconfig string, input []byte, args ...string) ([]byte, error) {
	cli := KubeCLI()
	if cli == "" {
		return nil, fmt.Errorf("snapshot class: neither kubectl nor oc found on PATH")
	}
	if kubeconfig != "" {
		args = append([]string{"--kubeconfig", kubeconfig}, args...)
	}
	cmd := exec.CommandContext(ctx, cli, args...) // #nosec G204 -- CLI is selected by cluster detection; arguments are structured.
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if input != nil {
		cmd.Stdin = strings.NewReader(string(input))
	}
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("%s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

func snapshotJSON(ctx context.Context, kubeconfig string, v any, args ...string) error {
	out, err := SnapshotCommand(ctx, kubeconfig, nil, args...)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(out, v); err != nil {
		return fmt.Errorf("decode %s: %w", strings.Join(args, " "), err)
	}
	return nil
}

type snapshotClass struct {
	Metadata struct {
		Name        string            `json:"name"`
		Annotations map[string]string `json:"annotations"`
	} `json:"metadata"`
	Driver string `json:"driver"`
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
	var sc struct {
		Provisioner string `json:"provisioner"`
	}
	if err := snapshotJSON(ctx, kubeconfig, &sc, "get", "storageclass", storageClass, "-o", "json"); err != nil {
		return s, err
	}
	name := configured
	if name == "" && vmSnapshot {
		var profile struct {
			Status struct {
				SnapshotClass string `json:"snapshotClass"`
			} `json:"status"`
		}
		// Missing profiles are allowed by KubeVirt; other lookup errors are not.
		out, err := SnapshotCommand(ctx, kubeconfig, nil, "get", "storageprofile", storageClass, "--ignore-not-found", "-o", "json")
		if err != nil {
			return s, err
		}
		if len(strings.TrimSpace(string(out))) > 0 {
			if err := json.Unmarshal(out, &profile); err != nil {
				return s, fmt.Errorf("decode storage profile: %w", err)
			}
			name = profile.Status.SnapshotClass
		}
	}
	if name != "" {
		var vsc snapshotClass
		if err := snapshotJSON(ctx, kubeconfig, &vsc, "get", "volumesnapshotclass", name, "-o", "json"); err != nil {
			return s, fmt.Errorf("snapshot class %q lookup failed: %w", name, err)
		}
		if sc.Provisioner == "" || vsc.Driver != sc.Provisioner {
			return s, fmt.Errorf("snapshot class %q driver %q does not match storage class %q provisioner %q", name, vsc.Driver, storageClass, sc.Provisioner)
		}
		s.SnapshotClass = name
		return s, nil
	}
	var list struct {
		Items []snapshotClass `json:"items"`
	}
	if err := snapshotJSON(ctx, kubeconfig, &list, "get", "volumesnapshotclass", "-o", "json"); err != nil {
		return s, err
	}
	var matches, defaults []string
	for _, vsc := range list.Items {
		if vsc.Driver != sc.Provisioner || sc.Provisioner == "" {
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
	_, err := SnapshotCommand(ctx, kubeconfig, nil,
		"wait", "--for=create", "storageprofile/storage-cert-preflight-probe", "--timeout=0s")
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
	_, err := SnapshotCommand(ctx, s.Kubeconfig, nil, "delete", "storageclass", created, "--ignore-not-found", "--wait=false")
	return err
}
