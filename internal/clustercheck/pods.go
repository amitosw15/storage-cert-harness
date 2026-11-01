package clustercheck

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strings"
)

// ErrUIDPreconditionFailed identifies a delete rejected because the named Pod changed.
var ErrUIDPreconditionFailed = errors.New("cluster resource UID precondition failed")

// Pod contains the identity, scheduling, and readiness fields used by adapters.
type Pod struct {
	Metadata ResourceMetadata `json:"metadata"`
	Spec     PodSpec          `json:"spec"`
	Status   PodStatus        `json:"status"`
}

type PodSpec struct {
	NodeName string `json:"nodeName"`
}

type PodStatus struct {
	Phase string `json:"phase"`
	PodIP string `json:"podIP"`
}

// GetPod reads an existing Pod in an explicit namespace.
func GetPod(ctx context.Context, kubeconfig string, ref NamespacedName) (Pod, error) {
	var value Pod
	if err := validateReference("pod", ref); err != nil {
		return value, err
	}
	err := getResource(ctx, kubeconfig, "pod", ref.Name, ref.Namespace, &value)
	return value, err
}

// CreatePod strictly creates a Pod and returns the API response, including its UID.
func CreatePod(ctx context.Context, kubeconfig string, data []byte) (Pod, error) {
	var value Pod
	_, err := createResource(ctx, kubeconfig, data, "Pod", &value)
	return value, err
}

// AnnotatePod updates only the supplied annotations on an existing named Pod.
func AnnotatePod(ctx context.Context, kubeconfig string, ref NamespacedName, annotations map[string]string) error {
	if err := validateReference("pod", ref); err != nil {
		return err
	}
	if len(annotations) == 0 {
		return nil
	}
	keys := make([]string, 0, len(annotations))
	for key, value := range annotations {
		if strings.TrimSpace(key) == "" || strings.HasPrefix(key, "-") || strings.ContainsAny(key+value, "\r\n") {
			return fmt.Errorf("pod annotation contains an empty key or newline")
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	args := []string{"annotate", "pod", ref.Name, "-n", ref.Namespace, "--overwrite"}
	for _, key := range keys {
		args = append(args, key+"="+annotations[key])
	}
	_, err := runKubeCommand(ctx, kubeconfig, nil, args...)
	return err
}

// ExecPod runs a command in the named Pod's default container.
func ExecPod(ctx context.Context, kubeconfig string, ref NamespacedName, command ...string) ([]byte, error) {
	if err := validateReference("pod", ref); err != nil {
		return nil, err
	}
	if len(command) == 0 || command[0] == "" {
		return nil, fmt.Errorf("pod exec requires a command")
	}
	args := []string{"exec", "-n", ref.Namespace, ref.Name, "--"}
	args = append(args, command...)
	return runKubeCommand(ctx, kubeconfig, nil, args...)
}

// DeletePod idempotently deletes a named Pod without waiting for termination.
func DeletePod(ctx context.Context, kubeconfig string, ref NamespacedName) error {
	if err := validateReference("pod", ref); err != nil {
		return err
	}
	_, err := runKubeCommand(ctx, kubeconfig, nil, "delete", "pod", ref.Name, "-n", ref.Namespace, "--ignore-not-found", "--wait=false")
	return err
}

// DeletePodWithUID deletes a Pod only if its current UID matches uid.
func DeletePodWithUID(ctx context.Context, kubeconfig string, ref NamespacedName, uid string) error {
	if err := validateReference("pod", ref); err != nil {
		return err
	}
	uid = strings.TrimSpace(uid)
	if uid == "" {
		return fmt.Errorf("delete pod requires a UID precondition")
	}
	path, err := writePodDeleteOptions(uid)
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(path) }()
	rawPath := fmt.Sprintf("/api/v1/namespaces/%s/pods/%s", url.PathEscape(ref.Namespace), url.PathEscape(ref.Name))
	_, err = runKubeCommand(ctx, kubeconfig, nil, "delete", "--raw", rawPath, "-f", path)
	lowerErr := strings.ToLower(fmt.Sprint(err))
	if err != nil && strings.Contains(lowerErr, "uid") && strings.Contains(lowerErr, "does not match") {
		return fmt.Errorf("%w: %v", ErrUIDPreconditionFailed, err)
	}
	if err != nil && strings.Contains(strings.ToLower(err.Error()), "notfound") {
		return nil
	}
	return err
}

func writePodDeleteOptions(uid string) (string, error) {
	options := struct {
		APIVersion    string `json:"apiVersion"`
		Kind          string `json:"kind"`
		Preconditions struct {
			UID string `json:"uid"`
		} `json:"preconditions"`
	}{APIVersion: "v1", Kind: "DeleteOptions"}
	options.Preconditions.UID = uid
	body, err := json.Marshal(options)
	if err != nil {
		return "", err
	}
	f, err := os.CreateTemp("", "pod-delete-*.json")
	if err != nil {
		return "", err
	}
	if _, err := f.Write(body); err != nil {
		_ = f.Close()
		_ = os.Remove(f.Name())
		return "", err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}
