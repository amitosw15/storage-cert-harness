package clustercheck

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func nodeCLI(t *testing.T, script string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "kubectl"), []byte("#!/bin/sh\n"+script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
}

func TestListNodes(t *testing.T) {
	nodeCLI(t, `case "$1 $2" in
  'get nodes') printf '%s' '{"items":[{"metadata":{"name":"worker-1","labels":{"node-role.kubernetes.io/worker":""}},"spec":{"unschedulable":true},"status":{"capacity":{"cpu":"8"},"nodeInfo":{"architecture":"amd64","osImage":"RHCOS"},"conditions":[{"type":"Ready","status":"True"}]}}]}' ;;
esac`)
	nodes, err := ListNodes(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 || nodes[0].Metadata.Name != "worker-1" || !nodes[0].Spec.Unschedulable || nodes[0].Status.Conditions[0].Status != "True" || nodes[0].Status.Capacity["cpu"] != "8" {
		t.Fatalf("nodes = %+v", nodes)
	}
}

func TestGetNodeAndVMI(t *testing.T) {
	nodeCLI(t, `case "$1 $2" in
  'get node') printf '%s' '{"metadata":{"name":"worker-1","labels":{"kubernetes.io/hostname":"host-1"}}}' ;;
  'get vmi') printf '%s' '{"status":{"phase":"Running"}}' ;;
esac`)
	node, err := GetNode(context.Background(), "", "worker-1")
	if err != nil || node.Metadata.Labels["kubernetes.io/hostname"] != "host-1" {
		t.Fatalf("GetNode = %+v, %v", node, err)
	}
	vmi, err := GetVMI(context.Background(), "", NamespacedName{Name: "vm-1", Namespace: "ns-1"})
	if err != nil || vmi.Status.Phase != "Running" {
		t.Fatalf("GetVMI = %+v, %v", vmi, err)
	}
}

func TestGetVMIValidatesReference(t *testing.T) {
	_, err := GetVMI(context.Background(), "", NamespacedName{Name: "", Namespace: "ns"})
	if err == nil || !strings.Contains(err.Error(), "requires name and namespace") {
		t.Fatalf("error = %v", err)
	}
}

func TestUncordonNode(t *testing.T) {
	nodeCLI(t, `test "$1" = uncordon && test "$2" = worker-1`)
	if err := UncordonNode(context.Background(), "", "worker-1"); err != nil {
		t.Fatal(err)
	}
	if err := UncordonNode(context.Background(), "", "-unsafe"); err == nil {
		t.Fatal("expected invalid node name error")
	}
}
