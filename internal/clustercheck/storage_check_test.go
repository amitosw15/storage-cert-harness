package clustercheck

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openshift-storage-cert/storage-cert-harness/internal/core"
)

func storageCLI(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$STORAGE_COMMANDS"
if [ "$1" = --kubeconfig ]; then shift 2; fi
if [ "$1" = create ] && [ "$2" = --raw ] && [ "$3" = /apis/authorization.k8s.io/v1/selfsubjectaccessreviews ]; then
 review=$(/bin/cat)
 printf '%s\n' "$review" >> "$STORAGE_COMMANDS.inputs"
 if [ -n "$AUTH_RAW" ]; then printf '%s' "$AUTH_RAW"; exit 0; fi
 case "$review" in
  *'"resource":"datavolumes"'*) allowed=$AUTH_EXPLICIT ;;
  *'"resource":"pods"'*) allowed=$AUTH_PODS ;;
  *'"resource":"pvcs"'*) allowed=$AUTH_PVCS ;;
  *) exit 1 ;;
 esac
 printf '{"status":{"allowed":%s}}' "${allowed:-false}"
 exit 0
fi
if [ "$1" != get ]; then echo 'mutation attempted' >&2; exit 1; fi
case "$2" in
 storageclass) printf '%s' "$STORAGE_CLASSES" ;;
 storageprofile) if [ "$PROFILE_DENIED" = true ]; then echo Forbidden >&2; exit 1; fi; printf '%s' "$STORAGE_PROFILE" ;;
 cdiconfig) printf '%s' "$STORAGE_CDI" ;;
 dataimportcron) if [ "$CRON_DENIED" = true ]; then echo Forbidden >&2; exit 1; fi; printf '%s' "$STORAGE_CRON" ;;
 datasource) printf '%s' "$STORAGE_SOURCE" ;;
 pvc) printf '%s' "$STORAGE_PVC" ;;
 volumesnapshot) printf '%s' "$STORAGE_SNAPSHOT" ;;
 volumesnapshotcontent) printf '%s' "$STORAGE_CONTENT" ;;
 pv) printf '%s' "$STORAGE_PV" ;;
 *) echo 'unexpected resource' >&2; exit 1 ;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, "oc"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	t.Setenv("STORAGE_COMMANDS", filepath.Join(dir, "commands"))
	t.Setenv("STORAGE_CLASSES", `{"items":[{"metadata":{"name":"nas"}},{"metadata":{"name":"economy"}},{"metadata":{"name":"scratch"}},{"metadata":{"name":"san","annotations":{"storageclass.kubernetes.io/is-default-class":"true","storageclass.kubevirt.io/is-default-virt-class":"true"}}}]}`)
	t.Setenv("STORAGE_PROFILE", `{"status":{"claimPropertySets":[{"accessModes":["ReadWriteMany"],"volumeMode":"Filesystem"}]}}`)
	t.Setenv("STORAGE_CDI", `{"status":{"scratchSpaceStorageClass":"scratch"}}`)
	t.Setenv("STORAGE_SOURCE", `{"spec":{"source":{"pvc":{"name":"golden"}}},"status":{"conditions":[{"type":"Ready","status":"True"}]}}`)
	t.Setenv("STORAGE_PVC", `{"spec":{"storageClassName":"nas"},"status":{"phase":"Bound"}}`)
	t.Setenv("STORAGE_SNAPSHOT", `{"status":{"readyToUse":true}}`)
	t.Setenv("AUTH_EXPLICIT", "true")
	return dir
}

func findingContains(findings []core.Finding, level, fragment string) bool {
	for _, f := range findings {
		if f.Level == level && strings.Contains(f.Message, fragment) {
			return true
		}
	}
	return false
}

func TestStoragePreflight(t *testing.T) {
	for _, tc := range []struct {
		name            string
		class           *string
		expected        string
		source          bool
		scratch         bool
		setup           func(*testing.T)
		wantLevel, want string
		noErrors        bool
	}{
		{name: "explicit target", class: stringPointer("nas"), expected: "nas", wantLevel: "info", want: "CDI defaults: accessModes=ReadWriteMany volumeMode=Filesystem", noErrors: true},
		{name: "wrong default", expected: "nas", wantLevel: "error", want: `target StorageClass "san"`},
		{name: "explicit empty", class: stringPointer(""), expected: "nas", wantLevel: "error", want: "no target StorageClass"},
		{name: "nonexistent", class: stringPointer("typo"), expected: "typo", wantLevel: "error", want: "does not exist"},
		{name: "source mismatch", class: stringPointer("economy"), expected: "economy", source: true, wantLevel: "warn", want: "cloning might not be accelerated", noErrors: true},
		{name: "matching source", class: stringPointer("nas"), expected: "nas", source: true, wantLevel: "info", want: "matches target", noErrors: true},
		{name: "different scratch", class: stringPointer("nas"), expected: "nas", scratch: true, wantLevel: "info", want: "scratch StorageClass=scratch", noErrors: true},
		{name: "missing scratch", class: stringPointer("nas"), expected: "nas", scratch: true, setup: func(t *testing.T) { t.Setenv("STORAGE_CDI", `{"status":{"scratchSpaceStorageClass":"missing"}}`) }, wantLevel: "error", want: "scratch StorageClass"},
		{name: "empty profile mode fallback", class: stringPointer("nas"), expected: "nas", setup: func(t *testing.T) { t.Setenv("STORAGE_PROFILE", `{"status":{}}`) }, wantLevel: "info", want: "volumeMode=Filesystem", noErrors: true},
		{name: "forbidden profile incomplete", class: stringPointer("nas"), expected: "nas", setup: func(t *testing.T) { t.Setenv("PROFILE_DENIED", "true") }, wantLevel: "warn", want: "cannot resolve omitted", noErrors: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := storageCLI(t)
			if tc.setup != nil {
				tc.setup(t)
			}
			request := StorageRequest{Identity: "vm/root", ExpectedClass: tc.expected, CDIStorage: true, Claim: StorageClaim{StorageClassName: tc.class, AccessModes: []string{"ReadWriteMany"}}, ScratchRequired: tc.scratch}
			if tc.source {
				request.SourceKind = "DataSource"
				request.Source = NamespacedName{Name: "image", Namespace: "images"}
			}
			got := CheckStorage(t.Context(), "/chosen", []StorageRequest{request})
			if !findingContains(got, tc.wantLevel, tc.want) {
				t.Fatalf("missing %s %q: %+v", tc.wantLevel, tc.want, got)
			}
			if tc.noErrors {
				for _, f := range got {
					if f.Level == "error" {
						t.Fatalf("unexpected error: %+v", got)
					}
				}
			}
			commands, err := os.ReadFile(filepath.Join(dir, "commands"))
			if err != nil {
				t.Fatal(err)
			}
			for _, line := range strings.Split(strings.TrimSpace(string(commands)), "\n") {
				if !strings.HasPrefix(line, "--kubeconfig /chosen get ") {
					t.Fatalf("wrong context or mutation: %s", line)
				}
			}
		})
	}
}

func stringPointer(s string) *string { return &s }

func TestStorageProfileDefaulting(t *testing.T) {
	profile := StorageProfile{Status: StorageProfileStatus{ClaimPropertySets: []json.RawMessage{
		json.RawMessage(`{"volumeMode":"Block","accessModes":["ReadWriteMany"]}`),
		json.RawMessage(`{"volumeMode":"Filesystem","accessModes":["ReadWriteOnce"]}`),
	}}}
	for _, tc := range []struct {
		name         string
		claim        StorageClaim
		mode, access string
	}{
		{"both omitted", StorageClaim{}, "Block", "ReadWriteMany"},
		{"access omitted", StorageClaim{VolumeMode: "Filesystem"}, "Filesystem", "ReadWriteOnce"},
		{"mode omitted", StorageClaim{AccessModes: []string{"ReadWriteOnce"}}, "Filesystem", "ReadWriteOnce"},
		{"no matching mode", StorageClaim{AccessModes: []string{"ReadOnlyMany"}}, "Filesystem", "ReadOnlyMany"},
		{"explicit unsupported recommendation", StorageClaim{VolumeMode: "Filesystem", AccessModes: []string{"ReadWriteMany"}}, "Filesystem", "ReadWriteMany"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := defaultStorageClaim(tc.claim, profile)
			if err != nil || got.VolumeMode != tc.mode || strings.Join(got.AccessModes, ",") != tc.access {
				t.Fatalf("got %+v, %v", got, err)
			}
		})
	}
	_, err := defaultStorageClaim(StorageClaim{}, StorageProfile{})
	if err == nil {
		t.Fatal("missing accessModes defaults accepted")
	}
	_, err = defaultStorageClaim(StorageClaim{VolumeMode: "Block"}, StorageProfile{Status: StorageProfileStatus{ClaimPropertySets: profile.Status.ClaimPropertySets[1:]}})
	if err == nil {
		t.Fatal("incompatible accessModes defaults accepted")
	}
}

func TestStorageDefaultPrecedence(t *testing.T) {
	classes := []StorageClass{{Metadata: ResourceMetadata{Name: "kube", Annotations: map[string]string{kubeDefaultAnnotation: "true"}}}, {Metadata: ResourceMetadata{Name: "virt", Annotations: map[string]string{virtDefaultAnnotation: "true"}}}}
	for _, cdi := range []bool{true, false} {
		got, _, err := storageTarget(StorageRequest{CDIStorage: cdi}, classes)
		want := "kube"
		if cdi {
			want = "virt"
		}
		if err != nil || got != want {
			t.Fatalf("got %s %v", got, err)
		}
	}
	got, _, err := storageTarget(StorageRequest{CDIStorage: true}, classes[:1])
	if err != nil || got != "kube" {
		t.Fatalf("fallback: %s %v", got, err)
	}
	classes = append(classes, classes[1])
	if _, _, err := storageTarget(StorageRequest{CDIStorage: true}, classes); err == nil {
		t.Fatal("ambiguous resolution not reported")
	}
}

func TestSnapshotOriginalClassUnavailable(t *testing.T) {
	storageCLI(t)
	source, err := inspectStorageSource(t.Context(), "", "snapshot", NamespacedName{Name: "retained", Namespace: "images"})
	if err != nil || source.Class != "" {
		t.Fatalf("got %+v %v", source, err)
	}
}
