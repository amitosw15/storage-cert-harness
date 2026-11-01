package clustercheck

import (
	"context"
	"encoding/json"
	"fmt"
)

// Node contains the metadata and status projections used by cluster operations.
type Node struct {
	Metadata ResourceMetadata `json:"metadata"`
	Spec     NodeSpec         `json:"spec"`
	Status   NodeStatus       `json:"status"`
}

type NodeSpec struct {
	Unschedulable bool `json:"unschedulable"`
}

type NodeStatus struct {
	Capacity   map[string]string   `json:"capacity"`
	NodeInfo   NodeInfo            `json:"nodeInfo"`
	Conditions []ResourceCondition `json:"conditions"`
}

type NodeInfo struct {
	Architecture string `json:"architecture"`
	OSImage      string `json:"osImage"`
}

// ListNodes reads cluster nodes with the fields used by selection and reporting.
func ListNodes(ctx context.Context, kubeconfig string) ([]Node, error) {
	data, err := runKubeCommand(ctx, kubeconfig, nil, "get", "nodes", "-o", "json")
	if err != nil {
		return nil, err
	}
	return decodeNodeList(data)
}

func listNodesWithCLI(ctx context.Context, cli string) ([]Node, error) {
	var value struct {
		Items []Node `json:"items"`
	}
	if err := runJSON(ctx, cli, &value, "get", "nodes", "-o", "json"); err != nil {
		return nil, err
	}
	return value.Items, nil
}

func decodeNodeList(data []byte) ([]Node, error) {
	var value struct {
		Items []Node `json:"items"`
	}
	if err := json.Unmarshal(data, &value); err != nil {
		return nil, fmt.Errorf("decode node list: %w", err)
	}
	return value.Items, nil
}

// GetNode reads one named node.
func GetNode(ctx context.Context, kubeconfig, name string) (Node, error) {
	var value Node
	err := getResource(ctx, kubeconfig, "node", name, "", &value)
	return value, err
}

// VirtualMachineInstance contains the phase used by readiness checks.
type VirtualMachineInstance struct {
	Status VirtualMachineInstanceStatus `json:"status"`
}

type VirtualMachineInstanceStatus struct {
	Phase string `json:"phase"`
}

// GetVMI reads a named KubeVirt instance.
func GetVMI(ctx context.Context, kubeconfig string, ref NamespacedName) (VirtualMachineInstance, error) {
	if err := validateReference("vmi", ref); err != nil {
		return VirtualMachineInstance{}, err
	}
	var value VirtualMachineInstance
	err := getResource(ctx, kubeconfig, "vmi", ref.Name, ref.Namespace, &value)
	return value, err
}

// UncordonNode marks a node schedulable.
func UncordonNode(ctx context.Context, kubeconfig, name string) error {
	if err := validateResourceName("node", name); err != nil {
		return err
	}
	_, err := runKubeCommand(ctx, kubeconfig, nil, "uncordon", name)
	return err
}
