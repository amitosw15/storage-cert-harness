package clustercheck

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/openshift-storage-cert/storage-cert-harness/internal/core"
)

func environmentCLI(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	script := `#!/bin/sh
case "$1 $2" in
  'get clusterversion') printf '4.21.22' ;;
  'version -o') echo '{"serverVersion":{"gitVersion":"v1.34.8"}}' ;;
  'get nodes') printf '%s' "$TEST_NODES" ;;
  'get csv') if [ "$3" = -A ]; then printf '%s' "$TEST_CSVS"; else printf '%s' "$TEST_SCOPED_CSVS"; fi ;;
  'get sc') printf '%s' "$TEST_SCS" ;;
  'get csidriver') printf '%s' "$TEST_CSIDRIVER" ;;
  'get daemonsets') printf '%s' "$TEST_DAEMONSETS" ;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, "oc"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	t.Setenv("TEST_NODES", `{"items":[
	  {"metadata":{"labels":{"node-role.kubernetes.io/control-plane":"","node-role.kubernetes.io/worker":""}},
	   "status":{"capacity":{"cpu":"128","memory":"1056964608Ki"},"nodeInfo":{"architecture":"amd64","osImage":"RHCOS 9.6"}}},
	  {"metadata":{"labels":{"node-role.kubernetes.io/worker":""}},
	   "status":{"capacity":{"cpu":"64","memory":"1056964608Ki"},"nodeInfo":{"architecture":"amd64","osImage":"RHCOS 9.6"}}}]}`)
	// A namespace-scoped query must not see the vendor operator, so a regression
	// to "-n openshift-cnv" fails the cluster-wide assertions below.
	t.Setenv("TEST_SCOPED_CSVS", `{"items":[
	  {"metadata":{"name":"kubevirt-hyperconverged-operator.v4.21.17"},"spec":{"version":"4.21.17"},"status":{"reason":"InstallSucceeded","phase":"Succeeded"}}]}`)
	t.Setenv("TEST_CSVS", `{"items":[
	  {"metadata":{"name":"kubevirt-hyperconverged-operator.v4.21.17"},"spec":{"version":"4.21.17"},"status":{"reason":"InstallSucceeded","phase":"Succeeded"}},
	  {"metadata":{"name":"examplestor-operator.v26.3.1"},"spec":{"version":"26.3.1"},"status":{"reason":"InstallSucceeded","phase":"Succeeded"}},
	  {"metadata":{"name":"examplestor-operator.v26.2.0"},"spec":{"version":"26.2.0"},"status":{"reason":"Replaced","phase":"Replacing"}},
	  {"metadata":{"name":"kubevirt-hyperconverged-operator.v4.21.16"},"spec":{"version":"4.21.16"},"status":{"reason":"InstallFailed","phase":"Failed"}},
	  {"metadata":{"name":"examplestor-operator.v26.3.1"},"spec":{"version":"26.3.1"},"status":{"reason":"Copied","phase":"Succeeded"}},
	  {"metadata":{"name":"kubevirt-hyperconverged-operator.v4.21.17"},"spec":{"version":"4.21.17"},"status":{"reason":"Copied","phase":"Succeeded"}}]}`)
	t.Setenv("TEST_SCS", `{"items":[
	  {"metadata":{"name":"sc-under-test"},"provisioner":"blk.examplestor.com","reclaimPolicy":"Delete","volumeBindingMode":"Immediate","parameters":{"backend":"block"}},
	  {"metadata":{"name":"standard","annotations":{"storageclass.kubernetes.io/is-default-class":"true"}},"provisioner":"kubernetes.io/no-provisioner"},
	  {"metadata":{"name":"e2e-leftover-1"},"provisioner":"blk.examplestor.com"},
	  {"metadata":{"name":"e2e-leftover-2"},"provisioner":"blk.examplestor.com"}]}`)
	t.Setenv("TEST_CSIDRIVER", `{"metadata":{"labels":{"app.kubernetes.io/version":"latest"}},
	  "spec":{"attachRequired":true,"fsGroupPolicy":"File","volumeLifecycleModes":["Persistent","Ephemeral"],
	          "podInfoOnMount":true,"storageCapacity":false,"requiresRepublish":false,"seLinuxMount":true}}`)
	t.Setenv("TEST_DAEMONSETS", `{"items":[
	  {"spec":{"template":{"spec":{"containers":[
	    {"name":"unrelated","image":"quay.io/other/thing:1.0","args":["--plugins/other.driver.io/csi.sock"]}]}}}},
	  {"spec":{"template":{"spec":{"containers":[
	    {"name":"csi-node-driver-registrar","image":"registry.k8s.io/sig-storage/csi-node-driver-registrar:v2.13.0",
	     "args":["--kubelet-registration-path=$(REG)"],"env":[{"value":"/var/lib/kubelet/plugins/blk.examplestor.com/csi.sock"}]},
	    {"name":"node-plugin","image":"registry.example.com/examplestor/blk-csi-driver:26.2.0"}]}}}}]}`)
}

func factValue(f []core.Fact, key string) string {
	for _, x := range f {
		if x.Key == key {
			return x.Value
		}
	}
	return ""
}

func TestCollectEnvironment(t *testing.T) {
	environmentCLI(t)
	env := CollectEnvironment(context.Background(), "oc", "sc-under-test")

	if env.Hardware.Nodes != 2 {
		t.Fatalf("nodes = %d, want 2", env.Hardware.Nodes)
	}
	if got := factValue(env.Hardware.Facts, "node_roles"); got != "control-plane=1,worker=2" {
		t.Errorf("node_roles = %q", got)
	}
	if got := factValue(env.Hardware.Facts, "cpu_per_node"); got != "128, 64" {
		t.Errorf("heterogeneous cpu reported as uniform: %q", got)
	}
	if got := factValue(env.Hardware.Facts, "arch"); got != "amd64" {
		t.Errorf("uniform arch should not be repeated: %q", got)
	}

	p := env.Platform
	if p.OCPVersion != "4.21.22" || p.KubeVersion != "v1.34.8" || p.CNVVersion != "4.21.17" {
		t.Errorf("platform versions = %+v", p)
	}
	if len(p.Operators) != 2 {
		t.Errorf("operators = %+v, want only the two installed originals", p.Operators)
	}
	var vendor *core.OperatorInfo
	for i, op := range p.Operators {
		if op.Name == "examplestor-operator" {
			vendor = &p.Operators[i]
		}
	}
	if vendor == nil {
		t.Fatalf("vendor operator missing; CSVs were not collected cluster-wide: %+v", p.Operators)
	}
	if vendor.Version != "26.3.1" {
		t.Errorf("vendor operator version = %q, want the installed CSV not the superseded one", vendor.Version)
	}

	if len(p.StorageClasses) != 2 {
		t.Fatalf("storage classes = %+v, want only under-test and default", p.StorageClasses)
	}
	var underTest *core.StorageClassInfo
	for i, s := range p.StorageClasses {
		if s.UnderTest {
			underTest = &p.StorageClasses[i]
		}
	}
	if underTest == nil || underTest.Name != "sc-under-test" || underTest.Parameters["backend"] != "block" {
		t.Fatalf("under-test class = %+v", underTest)
	}
	if got := factValue(p.Facts, "storage_classes_total"); got != "4" {
		t.Errorf("storage_classes_total = %q, want 4", got)
	}

	if p.CSIDriver.Name != "blk.examplestor.com" || !p.CSIDriver.AttachRequired || p.CSIDriver.FSGroupPolicy != "File" {
		t.Errorf("csi driver = %+v", p.CSIDriver)
	}
	// app.kubernetes.io/version is "latest", so the image tag must win.
	if p.CSIDriver.Version != "26.2.0" {
		t.Errorf("csi driver version = %q, want 26.2.0", p.CSIDriver.Version)
	}
	if got := factValue(p.Facts, "csi_driver_version"); got != "26.2.0" {
		t.Errorf("csi_driver_version fact = %q", got)
	}
	if want := []string{"podInfoOnMount", "seLinuxMount"}; len(p.CSIDriver.Capabilities) != 2 ||
		p.CSIDriver.Capabilities[0] != want[0] || p.CSIDriver.Capabilities[1] != want[1] {
		t.Errorf("capabilities = %v, want %v", p.CSIDriver.Capabilities, want)
	}
}

func TestCollectEnvironmentPrefersVendorLabel(t *testing.T) {
	environmentCLI(t)
	t.Setenv("TEST_CSIDRIVER", `{"metadata":{"labels":{"k8s_version":"v1.34.8","vendorb_version":"v26.06.1"}},"spec":{}}`)
	t.Setenv("TEST_DAEMONSETS", `{"items":[]}`)
	p := CollectEnvironment(context.Background(), "oc", "sc-under-test").Platform
	if p.CSIDriver.Version != "v26.06.1" {
		t.Fatalf("version = %q, want the vendor label and never the k8s one", p.CSIDriver.Version)
	}
}

func TestCollectEnvironmentPrefersLabelNamingTheDriver(t *testing.T) {
	environmentCLI(t)
	// "operator_version" sorts first but names no driver; the examplestor label does.
	t.Setenv("TEST_CSIDRIVER", `{"metadata":{"labels":{"operator_version":"1.2.3","examplestor_version":"26.2.0"}},"spec":{}}`)
	t.Setenv("TEST_DAEMONSETS", `{"items":[]}`)
	p := CollectEnvironment(context.Background(), "oc", "sc-under-test").Platform
	if p.CSIDriver.Version != "26.2.0" {
		t.Fatalf("version = %q, want the driver's label and not a co-located component's", p.CSIDriver.Version)
	}
}

func TestCollectEnvironmentVersionSurvivesCSIDriverReadFailure(t *testing.T) {
	environmentCLI(t)
	t.Setenv("TEST_CSIDRIVER", "")
	p := CollectEnvironment(context.Background(), "oc", "sc-under-test").Platform
	if p.CSIDriver.Version != "26.2.0" {
		t.Fatalf("version = %q, want the DaemonSet fallback to still run", p.CSIDriver.Version)
	}
}

func TestCollectEnvironmentAssembledSocketPath(t *testing.T) {
	environmentCLI(t)
	t.Setenv("TEST_CSIDRIVER", `{"metadata":{"labels":{}},"spec":{}}`)
	t.Setenv("TEST_DAEMONSETS", `{"items":[{"spec":{"template":{"spec":{"containers":[
	  {"name":"csi-node-driver-registrar","image":"registry.k8s.io/sig-storage/csi-node-driver-registrar:v2.13.0",
	   "args":["--kubelet-registration-path=$(PLUGIN_DIR)/blk.examplestor.com/csi.sock"]},
	  {"name":"node-plugin","image":"registry.example.com/examplestor/blk-csi-driver:26.2.0"}]}}}}]}`)
	p := CollectEnvironment(context.Background(), "oc", "sc-under-test").Platform
	if p.CSIDriver.Version != "26.2.0" {
		t.Fatalf("version = %q, want a path assembled from $(PLUGIN_DIR) to still match", p.CSIDriver.Version)
	}
}

func TestCollectEnvironmentVersionUnresolved(t *testing.T) {
	environmentCLI(t)
	t.Setenv("TEST_CSIDRIVER", `{"metadata":{"labels":{}},"spec":{}}`)
	t.Setenv("TEST_DAEMONSETS", `{"items":[]}`)
	p := CollectEnvironment(context.Background(), "oc", "sc-under-test").Platform
	if p.CSIDriver.Version != "" {
		t.Fatalf("version = %q, want empty rather than a guess", p.CSIDriver.Version)
	}
	if got := factValue(p.Facts, "csi_driver_version"); got != "" {
		t.Fatalf("unresolved version must not emit a fact: %q", got)
	}
}

func TestImageTag(t *testing.T) {
	for _, tt := range []struct{ image, want string }{
		{"registry.example.com/examplestor/blk-csi-driver:26.2.0", "26.2.0"},
		{"registry.example.com/vendorb/filer@sha256:d2d457526df46c", ""},
		{"registry.example.com/vendorb/filer:v26.06.1@sha256:d2d457526df46c", "v26.06.1"},
		{"registry:5000/some/image", ""},
		{"quay.io/vendor/driver:latest", ""},
		{"", ""},
	} {
		if got := imageTag(tt.image); got != tt.want {
			t.Errorf("imageTag(%q) = %q, want %q", tt.image, got, tt.want)
		}
	}
}

func TestDriverImageSkipsSidecarsAndStrangers(t *testing.T) {
	cs := []csiContainer{
		{Name: "csi-node-driver-registrar", Image: "registry.k8s.io/sig-storage/csi-node-driver-registrar:v2.13.0"},
		{Name: "recorder", Image: "example.io/csi-recorder:one-time"},
		{Name: "filer-main", Image: "registry.example.com/vendorb/filer:v26.06.1"},
	}
	if got := driverImage(cs, "csi.filer.vendorb.io"); got != "registry.example.com/vendorb/filer:v26.06.1" {
		t.Fatalf("driverImage = %q", got)
	}
}

func TestDriverImagePrefersStrongestMatch(t *testing.T) {
	// The companion shares one driver token and comes first; the driver's own
	// container matches both and must still win.
	cs := []csiContainer{
		{Name: "vendorb-metrics", Image: "registry.example.com/vendorb/metrics:9.9.9"},
		{Name: "filer-main", Image: "registry.example.com/vendorb/filer:v26.06.1"},
	}
	if got := driverImage(cs, "csi.filer.vendorb.io"); got != "registry.example.com/vendorb/filer:v26.06.1" {
		t.Fatalf("driverImage = %q, want the container matching the most driver tokens", got)
	}
}

func TestDriverTokens(t *testing.T) {
	for _, tt := range []struct {
		driver string
		want   []string
	}{
		{"blk.examplestor.com", []string{"blk", "examplestor"}},
		{"csi.filer.vendorb.io", []string{"filer", "vendorb"}},
		{"csi-blockvol.vendorc.com", []string{"blockvol", "vendorc"}},
	} {
		got := driverTokens(tt.driver)
		if len(got) != len(tt.want) {
			t.Fatalf("driverTokens(%q) = %v, want %v", tt.driver, got, tt.want)
		}
		for i := range got {
			if got[i] != tt.want[i] {
				t.Fatalf("driverTokens(%q) = %v, want %v", tt.driver, got, tt.want)
			}
		}
	}
}
