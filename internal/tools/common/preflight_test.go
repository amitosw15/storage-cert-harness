package common

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/openshift-storage-cert/storage-cert-harness/internal/clustercheck"
	"github.com/openshift-storage-cert/storage-cert-harness/internal/core"
)

func TestPathCheckOutcomes(t *testing.T) {
	tmp := t.TempDir()
	dir := filepath.Join(tmp, "dir")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(tmp, "file")
	if err := os.WriteFile(file, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name        string
		requirement PathRequirement
		want        string
	}{
		{name: "directory", requirement: PathRequirement{Name: "directory", Path: dir}, want: "info"},
		{name: "regular file", requirement: PathRequirement{Name: "file", Path: file, RegularFile: true}, want: "info"},
		{name: "directory rejected as file", requirement: PathRequirement{Name: "file", Path: dir, RegularFile: true}, want: "error"},
		{name: "directory rejected", requirement: PathRequirement{Name: "file", Path: dir, RejectDirectory: true}, want: "error"},
		{name: "non-directory file", requirement: PathRequirement{Name: "file", Path: file, RejectDirectory: true}, want: "info"},
		{name: "non-directory device", requirement: PathRequirement{Name: "file", Path: os.DevNull, RejectDirectory: true}, want: "info"},
		{name: "missing path", requirement: PathRequirement{Name: "missing", Path: filepath.Join(tmp, "missing")}, want: "error"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertSingleFindingLevel(t, PathCheck(tc.requirement).Run(context.Background()), tc.want)
		})
	}
}

func TestRequiredValueCheckOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name, value, want string
	}{
		{name: "provided", value: "worker-a", want: "info"},
		{name: "empty", want: "error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assertSingleFindingLevel(t, RequiredValueCheck("node", tc.value).Run(context.Background()), tc.want)
		})
	}
}

func TestIntegerBoundsCheckOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name       string
		value, min int
		max        int
		want       string
	}{
		{name: "lower boundary", value: 1, min: 1, max: 5, want: "info"},
		{name: "upper boundary", value: 5, min: 1, max: 5, want: "info"},
		{name: "below minimum", value: 0, min: 1, want: "error"},
		{name: "above maximum", value: 6, min: 1, max: 5, want: "error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assertSingleFindingLevel(t, IntegerBoundsCheck("count", tc.value, tc.min, tc.max).Run(context.Background()), tc.want)
		})
	}
}

func TestEnumCheckOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name       string
		value      string
		allowEmpty bool
		want       string
	}{
		{name: "choice", value: "fast", want: "info"},
		{name: "allowed default", allowEmpty: true, want: "info"},
		{name: "unknown choice", value: "turbo", want: "error"},
		{name: "empty disallowed", want: "error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assertSingleFindingLevel(t, EnumCheck("mode", tc.value, []string{"fast", "safe"}, tc.allowEmpty).Run(context.Background()), tc.want)
		})
	}
}

func TestTemplateDiskCountCheckOutcomes(t *testing.T) {
	parseCount := func(got int, err error) func(string) (int, error) {
		return func(string) (int, error) { return got, err }
	}
	for _, tc := range []struct {
		name       string
		disks      int
		defaultNum int
		template   string
		parse      func(string) (int, error)
		want       string
	}{
		{name: "default template match", disks: 2, defaultNum: 2, want: "info"},
		{name: "default template mismatch", disks: 3, defaultNum: 2, want: "error"},
		{name: "disk count below one", disks: 0, defaultNum: 2, want: "error"},
		{name: "template parser required", disks: 2, defaultNum: 1, template: "vm.yaml", want: "error"},
		{name: "parser failure", disks: 2, defaultNum: 1, template: "vm.yaml", parse: parseCount(0, errors.New("invalid")), want: "error"},
		{name: "template count mismatch", disks: 3, defaultNum: 1, template: "vm.yaml", parse: parseCount(2, nil), want: "error"},
		{name: "template count match", disks: 2, defaultNum: 1, template: "vm.yaml", parse: parseCount(2, nil), want: "info"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			check := TemplateDiskCountCheck("disks", tc.disks, tc.defaultNum, tc.template, tc.parse)
			assertSingleFindingLevel(t, check.Run(context.Background()), tc.want)
		})
	}
}

func TestRunPreflightChecksOrderingSkippingAndDeduplication(t *testing.T) {
	keyedCalls := 0
	unkeyedCalls := 0
	type contextKey struct{}
	ctx := context.WithValue(context.Background(), contextKey{}, "test-context")
	checks := []PreflightCheck{
		{Name: "skip", Key: "shared", Applies: func() (bool, string) { return false, "disabled" }, Run: func(context.Context) []core.Finding { keyedCalls++; return nil }},
		{Name: "error", Run: func(context.Context) []core.Finding {
			unkeyedCalls++
			return []core.Finding{{Level: "error"}}
		}},
		{Name: "first", Key: "shared", Run: func(got context.Context) []core.Finding {
			keyedCalls++
			if got.Value(contextKey{}) == "test-context" {
				return []core.Finding{{Level: "info"}}
			}
			return []core.Finding{{Level: "error"}}
		}},
		{Name: "duplicate", Key: "shared", Run: func(context.Context) []core.Finding { keyedCalls++; return nil }},
		{Name: "unkeyed one", Run: func(context.Context) []core.Finding { unkeyedCalls++; return []core.Finding{{Level: "info"}} }},
		{Name: "unkeyed two", Run: func(context.Context) []core.Finding { unkeyedCalls++; return []core.Finding{{Level: "info"}} }},
	}
	findings := RunPreflightChecks(ctx, checks)
	if keyedCalls != 1 || unkeyedCalls != 3 || len(findings) != 5 {
		t.Fatalf("calls keyed=%d unkeyed=%d findings=%+v, want skip, error, one keyed, and both unkeyed results", keyedCalls, unkeyedCalls, findings)
	}
	if findings[0].Level != "skip" || findings[1].Level != "error" || findings[2].Level != "info" || findings[3].Level != "info" || findings[4].Level != "info" {
		t.Fatalf("findings order/outcomes = %+v, want skip, error, and three info findings", findings)
	}
	findings = RunPreflightChecks(ctx, checks)
	if keyedCalls != 2 || unkeyedCalls != 6 || len(findings) != 5 {
		t.Fatalf("second call counts keyed=%d unkeyed=%d findings=%+v, want a fresh dedup scope", keyedCalls, unkeyedCalls, findings)
	}
}

func assertSingleFindingLevel(t *testing.T, findings []core.Finding, want string) {
	t.Helper()
	if len(findings) != 1 || findings[0].Level != want {
		t.Fatalf("findings = %+v, want one %q finding", findings, want)
	}
}

func TestIdentityStorageClassAndBinaryChecks(t *testing.T) {
	identity := ToolIdentityCheck(core.TestRequirement{ID: "TR-1"}, core.ToolVirtbench)
	if findings := identity.Run(context.Background()); len(findings) != 0 {
		t.Fatalf("empty automation tool findings = %+v, want none", findings)
	}
	identity = ToolIdentityCheck(core.TestRequirement{ID: "TR-1", AutomationTool: string(core.ToolVirtbench)}, core.ToolVirtbench)
	if findings := identity.Run(context.Background()); len(findings) != 0 {
		t.Fatalf("matching automation tool findings = %+v, want none", findings)
	}
	identity = ToolIdentityCheck(core.TestRequirement{ID: "TR-1", AutomationTool: "other"}, core.ToolVirtbench)
	if findings := identity.Run(context.Background()); len(findings) != 1 || findings[0].Level != "error" {
		t.Fatalf("mismatched automation tool findings = %+v, want error", findings)
	}

	for _, rc := range []*core.RunCtx{nil, {Backend: &core.ResolvedBackend{}}} {
		if findings := StorageClassCheck(rc).Run(context.Background()); len(findings) != 1 || findings[0].Level != "error" {
			t.Fatalf("missing storage class findings = %+v, want error", findings)
		}
	}
	if findings := StorageClassCheck(&core.RunCtx{Backend: &core.ResolvedBackend{StorageClass: "fast-sc"}}).Run(context.Background()); len(findings) != 1 || findings[0].Level != "info" {
		t.Fatalf("configured storage class findings = %+v, want info", findings)
	}

	tmp := t.TempDir()
	writeExecutable(t, filepath.Join(tmp, "available"), "#!/bin/sh\nexit 0\n")
	t.Setenv("PATH", tmp)
	if findings := BinaryCheck(BinaryRequirement{Name: "available"}).Run(context.Background()); len(findings) != 1 || findings[0].Level != "info" {
		t.Fatalf("available binary findings = %+v, want info", findings)
	}
	if findings := BinaryCheck(BinaryRequirement{Name: "missing", Workload: "fio"}).Run(context.Background()); len(findings) != 1 || findings[0].Level != "error" {
		t.Fatalf("missing binary findings = %+v, want error", findings)
	}
}

func TestCommandCheckRunsFixedArguments(t *testing.T) {
	tmp := t.TempDir()
	logPath := filepath.Join(tmp, "args")
	writeExecutable(t, filepath.Join(tmp, "fake-command"), "#!/bin/sh\nprintf '%s\\n' \"$*\" > \"$COMMAND_LOG\"\n")
	t.Setenv("PATH", tmp)
	t.Setenv("COMMAND_LOG", logPath)

	check := CommandCheck(CommandRequirement{Name: "fake command --check", Binary: "fake-command", Args: []string{"--check", "value"}})
	if check.Key == "" || check.Key == BinaryCheck(BinaryRequirement{Name: "fake-command"}).Key {
		t.Fatal("command check must have a distinct identity from a binary lookup")
	}
	findings := check.Run(context.Background())
	if len(findings) != 1 || findings[0].Level != "info" {
		t.Fatalf("findings = %+v, want one info finding", findings)
	}
	args, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(args)) != "--check value" {
		t.Fatalf("command args = %q", args)
	}
	if generated := CommandCheck(CommandRequirement{Binary: "fake-command", Args: []string{"--check"}}); generated.Name != "fake-command --check" {
		t.Fatalf("generated command name = %q", generated.Name)
	}
}

func TestCommandCheckReportsExecutionAndLookupFailures(t *testing.T) {
	tmp := t.TempDir()
	writeExecutable(t, filepath.Join(tmp, "bad-command"), "#!/bin/sh\necho failed >&2\nexit 7\n")
	t.Setenv("PATH", tmp)
	for _, requirement := range []CommandRequirement{
		{Name: "bad command", Binary: "bad-command"},
		{Name: "missing command", Binary: "not-installed", Workload: "fio"},
	} {
		findings := CommandCheck(requirement).Run(context.Background())
		if len(findings) != 1 || findings[0].Level != "error" {
			t.Errorf("requirement %+v findings = %+v, want error", requirement, findings)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	writeExecutable(t, filepath.Join(tmp, "slow-command"), "#!/bin/sh\n/bin/sleep 5\n")
	findings := CommandCheck(CommandRequirement{Binary: "slow-command"}).Run(ctx)
	if len(findings) != 1 || findings[0].Level != "error" {
		t.Fatalf("canceled command findings = %+v, want error", findings)
	}
}

func TestSchedulableWorkerCheckMatchesExistingFiltering(t *testing.T) {
	tmp := t.TempDir()
	logPath := filepath.Join(tmp, "args")
	response := `{"items":[` +
		`{"metadata":{"name":"worker-ready","labels":{"node-role.kubernetes.io/worker":""}},"spec":{}},` +
		`{"metadata":{"name":"worker-cordoned","labels":{"node-role.kubernetes.io/worker":""}},"spec":{"unschedulable":true}},` +
		`{"metadata":{"name":"control-plane","labels":{"node-role.kubernetes.io/worker":"","node-role.kubernetes.io/control-plane":""}},"spec":{}},` +
		`{"metadata":{"name":"master","labels":{"node-role.kubernetes.io/worker":"","node-role.kubernetes.io/master":""}},"spec":{}}]}`
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" > \"$KUBECTL_LOG\"\nprintf '%s' '" + response + "'\n"
	writeExecutable(t, filepath.Join(tmp, "kubectl"), script)
	t.Setenv("PATH", tmp)
	t.Setenv("KUBECTL_LOG", logPath)

	check := SchedulableWorkerCheck(WorkerRequirement{Name: "workers", Kubeconfig: "/tmp/kubeconfig", Node: "worker-ready", MinWorkers: 1})
	findings := check.Run(context.Background())
	if len(findings) != 2 || findings[0].Level != "info" || findings[1].Level != "info" {
		t.Fatalf("findings = %+v, want worker count and pinned node info", findings)
	}
	args, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(args)) != "--kubeconfig /tmp/kubeconfig get nodes -o json" {
		t.Fatalf("kubectl args = %q", args)
	}

	bad := SchedulableWorkerCheck(WorkerRequirement{Name: "workers", Node: "worker-cordoned", MinWorkers: 2})
	findings = bad.Run(context.Background())
	if len(findings) != 2 || findings[0].Level != "error" || findings[1].Level != "error" {
		t.Fatalf("findings = %+v, want minimum count and cordoned-node errors", findings)
	}
	if findings := SchedulableWorkerCheck(WorkerRequirement{Name: "workers"}).Run(context.Background()); len(findings) != 0 {
		t.Fatalf("unconstrained worker check findings = %+v, want none", findings)
	}
}

func TestSchedulableWorkerCheckReportsCommandAndDecodeFailures(t *testing.T) {
	tmp := t.TempDir()
	kubectl := filepath.Join(tmp, "kubectl")
	writeExecutable(t, kubectl, "#!/bin/sh\necho invalid-json\n")
	t.Setenv("PATH", tmp)
	findings := SchedulableWorkerCheck(WorkerRequirement{Name: "workers"}).Run(context.Background())
	if len(findings) != 1 || findings[0].Level != "error" {
		t.Fatalf("malformed node response findings = %+v, want error", findings)
	}
	writeExecutable(t, kubectl, "#!/bin/sh\necho unavailable >&2\nexit 2\n")
	findings = SchedulableWorkerCheck(WorkerRequirement{Name: "workers"}).Run(context.Background())
	if len(findings) != 1 || findings[0].Level != "error" {
		t.Fatalf("kubectl failure findings = %+v, want error", findings)
	}
}

func TestCapabilityCheckAndPreflightDeduplication(t *testing.T) {
	tmp := t.TempDir()
	logPath := filepath.Join(tmp, "calls")
	writeExecutable(t, filepath.Join(tmp, "kubectl"), "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$CALL_LOG\"\n")
	t.Setenv("PATH", tmp)
	t.Setenv("CALL_LOG", logPath)
	called := 0
	check := CapabilityCheck(clustercheck.Capability{
		Name: "test capability",
		Check: func(_ context.Context, cli string) core.Finding {
			called++
			if cli != "kubectl" {
				return core.Finding{Level: "error"}
			}
			return core.Finding{Level: "info"}
		},
	}, "fast-sc")
	findings := RunPreflightChecks(context.Background(), []PreflightCheck{check, check})
	if called != 1 || len(findings) != 1 || findings[0].Level != "info" {
		t.Fatalf("called %d times, findings = %+v; want one capability execution", called, findings)
	}
}

func TestVolumePopulatorCheckRecordsResultsAndErrors(t *testing.T) {
	tmp := t.TempDir()
	kubectl := filepath.Join(tmp, "kubectl")
	writeExecutable(t, kubectl, "#!/bin/sh\nprintf '%s' '{\"items\":[{\"sourceKind\":{\"group\":\"cdi.kubevirt.io\",\"kind\":\"VolumeImportSource\"}}]}'\n")
	t.Setenv("PATH", tmp)
	registered := false
	callbacks := 0
	findings := VolumePopulatorCheck(func(value bool) { registered = value; callbacks++ }).Run(context.Background())
	if len(findings) != 1 || findings[0].Level != "info" || !registered || callbacks != 1 {
		t.Fatalf("registered=%t callbacks=%d findings=%+v, want info and registered callback", registered, callbacks, findings)
	}
	writeExecutable(t, kubectl, "#!/bin/sh\nprintf '%s' 'invalid-json'\n")
	registered = false
	callbacks = 0
	findings = VolumePopulatorCheck(func(value bool) { registered = value; callbacks++ }).Run(context.Background())
	if len(findings) != 1 || findings[0].Level != "error" || registered || callbacks != 0 {
		t.Fatalf("registered=%t callbacks=%d findings=%+v, want decode error without callback", registered, callbacks, findings)
	}
	writeExecutable(t, kubectl, "#!/bin/sh\nprintf '%s' '{\"items\":[]}'\n")
	callbacks = 0
	findings = VolumePopulatorCheck(func(value bool) { registered = value; callbacks++ }).Run(context.Background())
	if len(findings) != 1 || findings[0].Level != "info" || registered || callbacks != 1 {
		t.Fatalf("registered=%t callbacks=%d findings=%+v, want fallback callback", registered, callbacks, findings)
	}
	t.Setenv("PATH", t.TempDir())
	findings = VolumePopulatorCheck(nil).Run(context.Background())
	if len(findings) != 1 || findings[0].Level != "skip" {
		t.Fatalf("without a Kubernetes CLI findings = %+v, want skip", findings)
	}
}

func TestSnapshotSupportCheckSkipAndResolutionFailure(t *testing.T) {
	check := SnapshotSupportCheck(SnapshotSupport{Name: "snapshots", SnapshotCount: 0, SkipReason: "disabled"})
	findings := RunPreflightChecks(context.Background(), []PreflightCheck{check})
	if len(findings) != 1 || findings[0].Level != "skip" {
		t.Fatalf("skipped snapshot findings = %+v, want skip", findings)
	}
	findings = SnapshotSupportCheck(SnapshotSupport{Name: "snapshots", SnapshotCount: 1}).Run(context.Background())
	if len(findings) != 0 {
		t.Fatalf("snapshot check without storage class findings = %+v, want none", findings)
	}

	tmp := t.TempDir()
	writeExecutable(t, filepath.Join(tmp, "kubectl"), "#!/bin/sh\necho failed >&2\nexit 2\n")
	t.Setenv("PATH", tmp)
	recorded := false
	findings = SnapshotSupportCheck(SnapshotSupport{
		Name: "snapshots", StorageClass: "fast-sc", SnapshotCount: 1,
		Record: func(clustercheck.SnapshotSelection) { recorded = true },
	}).Run(context.Background())
	if len(findings) != 1 || findings[0].Level != "error" || recorded {
		t.Fatalf("recorded=%t findings=%+v, want resolution error without callback", recorded, findings)
	}
}

func TestDataSourceCheckRecordsSourcesAndAllowsEmptySource(t *testing.T) {
	tmp := t.TempDir()
	logPath := filepath.Join(tmp, "kubectl-args")
	kubectl := filepath.Join(tmp, "kubectl")
	writeExecutable(t, kubectl, "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$KUBECTL_LOG\"\ncase \"$SOURCE_MODE\" in\n"+
		"  populated) printf '%s' '{\"spec\":{\"source\":{\"pvc\":{\"name\":\"source-pvc\",\"namespace\":\"source-ns\"}}}}' ;;\n"+
		"  empty) printf '%s' '{\"spec\":{\"source\":null}}' ;;\n"+
		"  empty-object) printf '%s' '{\"spec\":{\"source\":{}}}' ;;\n"+
		"  invalid) printf '%s' 'not-json' ;;\n"+
		"  failed) echo unavailable >&2; exit 4 ;;\n"+
		"esac\n")
	t.Setenv("PATH", tmp)
	t.Setenv("KUBECTL_LOG", logPath)
	t.Setenv("SOURCE_MODE", "populated")

	var got map[string]NamespacedSource
	calls := 0
	check := DataSourceCheck(DataSourceRequirement{
		Source: NamespacedSource{Name: "image-source", Namespace: "source-ns"}, Kubeconfig: "/tmp/cluster.kubeconfig",
		Record: func(sources map[string]NamespacedSource) { got = sources; calls++ },
	})
	findings := check.Run(context.Background())
	want := map[string]NamespacedSource{"pvc": {Name: "source-pvc", Namespace: "source-ns"}}
	if len(findings) != 1 || findings[0].Level != "info" || calls != 1 || !reflect.DeepEqual(got, want) {
		t.Fatalf("findings=%+v calls=%d sources=%+v, want successful source resolution", findings, calls, got)
	}
	args, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(args), "--kubeconfig /tmp/cluster.kubeconfig get datasource image-source -n source-ns -o json") {
		t.Fatalf("DataSource request did not preserve kubeconfig and object arguments: %s", args)
	}

	t.Setenv("SOURCE_MODE", "empty")
	findings = check.Run(context.Background())
	if len(findings) != 1 || findings[0].Level != "info" || calls != 2 || got != nil {
		t.Fatalf("empty source findings=%+v calls=%d sources=%+v, want success callback with nil source", findings, calls, got)
	}
	t.Setenv("SOURCE_MODE", "empty-object")
	findings = check.Run(context.Background())
	if len(findings) != 1 || findings[0].Level != "info" || calls != 3 || got == nil || len(got) != 0 {
		t.Fatalf("empty object findings=%+v calls=%d sources=%+v, want success callback with empty map", findings, calls, got)
	}

	for _, mode := range []string{"invalid", "failed"} {
		t.Setenv("SOURCE_MODE", mode)
		callsBefore := calls
		findings = check.Run(context.Background())
		if len(findings) != 1 || findings[0].Level != "error" || calls != callsBefore {
			t.Errorf("mode %q findings=%+v calls=%d, want error without callback", mode, findings, calls)
		}
	}
	for _, source := range []NamespacedSource{{Namespace: "source-ns"}, {Name: "image-source"}} {
		findings = DataSourceCheck(DataSourceRequirement{Source: source}).Run(context.Background())
		if len(findings) != 1 || findings[0].Level != "error" {
			t.Errorf("source %+v findings = %+v, want missing-identity error", source, findings)
		}
	}
}

func TestPVCSnapshotCompatibilityUsesSourceStorageClassDriver(t *testing.T) {
	tmp := t.TempDir()
	logPath := filepath.Join(tmp, "kubectl-args")
	kubectl := filepath.Join(tmp, "kubectl")
	writeExecutable(t, kubectl, "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$KUBECTL_LOG\"\ncase \"$PVC_MODE:$*\" in\n"+
		"  valid:*'get pvc source-pvc -n source-ns -o json'*) printf '%s' '{\"spec\":{\"storageClassName\":\"source-sc\"}}' ;;\n"+
		"  mismatch:*'get pvc source-pvc -n source-ns -o json'*) printf '%s' '{\"spec\":{\"storageClassName\":\"source-sc\"}}' ;;\n"+
		"  invalid:*'get pvc source-pvc -n source-ns -o json'*) printf '%s' 'not-json' ;;\n"+
		"  failed:*'get pvc source-pvc -n source-ns -o json'*) echo unavailable >&2; exit 4 ;;\n"+
		"  *'get storageclass source-sc -o json'*) printf '%s' '{\"provisioner\":\"driver.example\"}' ;;\n"+
		"  mismatch:*'get volumesnapshotclass selected-snap -o json'*) printf '%s' '{\"metadata\":{\"name\":\"selected-snap\",\"annotations\":{}},\"driver\":\"other.example\"}' ;;\n"+
		"  *'get volumesnapshotclass selected-snap -o json'*) printf '%s' '{\"metadata\":{\"name\":\"selected-snap\",\"annotations\":{}},\"driver\":\"driver.example\"}' ;;\n"+
		"  *) echo unexpected >&2; exit 9 ;;\n"+
		"esac\n")
	t.Setenv("PATH", tmp)
	t.Setenv("KUBECTL_LOG", logPath)
	t.Setenv("PVC_MODE", "valid")
	request := SnapshotSourceRequirement{
		Source:        NamespacedSource{Name: "source-pvc", Namespace: "source-ns"},
		SnapshotClass: "selected-snap", Kubeconfig: "/tmp/cluster.kubeconfig",
	}
	findings := PVCSnapshotCompatibilityCheck(request).Run(context.Background())
	if len(findings) != 1 || findings[0].Level != "info" {
		t.Fatalf("same-driver, different-storage-class findings = %+v, want info", findings)
	}
	args, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(args), "--kubeconfig /tmp/cluster.kubeconfig get pvc source-pvc -n source-ns -o json") {
		t.Fatalf("PVC request did not preserve kubeconfig and object arguments: %s", args)
	}

	t.Setenv("PVC_MODE", "mismatch")
	findings = PVCSnapshotCompatibilityCheck(request).Run(context.Background())
	if len(findings) != 1 || findings[0].Level != "error" {
		t.Fatalf("different-driver findings = %+v, want error", findings)
	}
	for _, mode := range []string{"invalid", "failed"} {
		t.Setenv("PVC_MODE", mode)
		findings = PVCSnapshotCompatibilityCheck(request).Run(context.Background())
		if len(findings) != 1 || findings[0].Level != "error" {
			t.Errorf("mode %q findings = %+v, want error", mode, findings)
		}
	}
	for _, source := range []NamespacedSource{{Namespace: "source-ns"}, {Name: "source-pvc"}} {
		request.Source = source
		findings = PVCSnapshotCompatibilityCheck(request).Run(context.Background())
		if len(findings) != 1 || findings[0].Level != "error" {
			t.Errorf("source %+v findings = %+v, want missing-identity error", source, findings)
		}
	}
}

func TestVolumeSnapshotCompatibilityRequiresExactClass(t *testing.T) {
	tmp := t.TempDir()
	kubectl := filepath.Join(tmp, "kubectl")
	writeExecutable(t, kubectl, "#!/bin/sh\ncase \"$SNAPSHOT_MODE\" in\n"+
		"  valid) printf '%s' '{\"spec\":{\"volumeSnapshotClassName\":\"selected-snap\"}}' ;;\n"+
		"  mismatch) printf '%s' '{\"spec\":{\"volumeSnapshotClassName\":\"other-snap\"}}' ;;\n"+
		"  invalid) printf '%s' 'not-json' ;;\n"+
		"  failed) echo unavailable >&2; exit 4 ;;\n"+
		"esac\n")
	t.Setenv("PATH", tmp)
	request := SnapshotSourceRequirement{
		Source:        NamespacedSource{Name: "source-snapshot", Namespace: "source-ns"},
		SnapshotClass: "selected-snap", Kubeconfig: "/tmp/cluster.kubeconfig",
	}
	t.Setenv("SNAPSHOT_MODE", "valid")
	findings := VolumeSnapshotCompatibilityCheck(request).Run(context.Background())
	if len(findings) != 1 || findings[0].Level != "info" {
		t.Fatalf("matching snapshot class findings = %+v, want info", findings)
	}
	for _, mode := range []string{"mismatch", "invalid", "failed"} {
		t.Setenv("SNAPSHOT_MODE", mode)
		findings = VolumeSnapshotCompatibilityCheck(request).Run(context.Background())
		if len(findings) != 1 || findings[0].Level != "error" {
			t.Errorf("mode %q findings = %+v, want error", mode, findings)
		}
	}
	for _, source := range []NamespacedSource{{Namespace: "source-ns"}, {Name: "source-snapshot"}} {
		request.Source = source
		findings = VolumeSnapshotCompatibilityCheck(request).Run(context.Background())
		if len(findings) != 1 || findings[0].Level != "error" {
			t.Errorf("source %+v findings = %+v, want missing-identity error", source, findings)
		}
	}
}

func TestSnapshotSupportCheckProbesWaitOnlyWhenRequested(t *testing.T) {
	tmp := t.TempDir()
	logPath := filepath.Join(tmp, "args")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$KUBECTL_LOG\"\ncase \"$*\" in\n" +
		"  'get storageclass fast-sc -o json') printf '%s' '{\"provisioner\":\"driver.example\"}' ;;\n" +
		"  'get volumesnapshotclass snap-fast -o json') printf '%s' '{\"metadata\":{\"name\":\"snap-fast\",\"annotations\":{}},\"driver\":\"driver.example\"}' ;;\n" +
		"  'get volumesnapshotclass -o json') printf '%s' '{\"items\":[{\"metadata\":{\"name\":\"snap-fast\",\"annotations\":{\"snapshot.storage.kubernetes.io/is-default-class\":\"true\"}},\"driver\":\"driver.example\"}]}' ;;\n" +
		"  'wait --for=create storageprofile/storage-cert-preflight-probe --timeout=0s') printf '%s' '{}' ;;\n" +
		"  *) exit 9 ;;\nesac\n"
	writeExecutable(t, filepath.Join(tmp, "kubectl"), script)
	t.Setenv("PATH", tmp)
	t.Setenv("KUBECTL_LOG", logPath)

	var selected clustercheck.SnapshotSelection
	request := SnapshotSupport{
		Name: "snapshot class", StorageClass: "fast-sc", SnapshotClass: "snap-fast",
		VMSnapshot: true, SnapshotCount: 1,
		Record: func(selection clustercheck.SnapshotSelection) { selected = selection },
	}
	findings := SnapshotSupportCheck(request).Run(context.Background())
	if len(findings) != 1 || findings[0].Level != "info" || selected.SnapshotClass != "snap-fast" {
		t.Fatalf("findings = %+v, selection = %+v; want class selection without client probe", findings, selected)
	}
	args, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(args), "wait --for=create") {
		t.Fatalf("unexpected wait-for-create probe: %s", args)
	}

	request.RequireVMClient = true
	findings = SnapshotSupportCheck(request).Run(context.Background())
	if len(findings) != 2 || findings[1].Level != "info" {
		t.Fatalf("findings = %+v, want snapshot selection and client support", findings)
	}
	args, err = os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(args), "wait --for=create") {
		t.Fatalf("missing wait-for-create probe: %s", args)
	}
}

func writeExecutable(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o700); err != nil {
		t.Fatal(err)
	}
}
