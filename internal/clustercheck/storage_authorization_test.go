package clustercheck

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCDICloneAuthorization(t *testing.T) {
	for _, tc := range []struct {
		name, kind                        string
		explicit, pods, pvcs, same, allow bool
	}{
		{name: "PVC explicit source permission", kind: "pvc", explicit: true, allow: true},
		{name: "PVC pod permission", kind: "pvc", pods: true, allow: true},
		{name: "snapshot needs pods and PVCs", kind: "snapshot", pods: true, pvcs: true, allow: true},
		{name: "snapshot pods alone denied", kind: "snapshot", pods: true},
		{name: "snapshot PVCs alone denied", kind: "snapshot", pvcs: true},
		{name: "snapshot explicit source permission", kind: "snapshot", explicit: true, allow: true},
		{name: "PVC denied", kind: "pvc"},
		{name: "same namespace", kind: "snapshot", same: true, allow: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := storageCLI(t)
			set := func(key string, value bool) {
				if value {
					t.Setenv(key, "true")
				} else {
					t.Setenv(key, "false")
				}
			}
			set("AUTH_EXPLICIT", tc.explicit)
			set("AUTH_PODS", tc.pods)
			set("AUTH_PVCS", tc.pvcs)
			target := "workload"
			if tc.same {
				target = "images"
			}
			source := storageSource{Kind: tc.kind, Reference: NamespacedName{Name: "image", Namespace: "images"}}
			allowed, err := checkCDICloneAccess(t.Context(), "/chosen", target, source)
			if err != nil || allowed != tc.allow {
				t.Fatalf("allowed=%t err=%v", allowed, err)
			}
			inputs, err := os.ReadFile(filepath.Join(dir, "commands.inputs"))
			if tc.same {
				if !os.IsNotExist(err) {
					t.Fatal("same namespace unexpectedly queried auth")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			for _, line := range strings.Split(strings.TrimSpace(string(inputs)), "\n") {
				if !strings.Contains(line, `"namespace":"images"`) || !strings.Contains(line, `"name":"image"`) || !strings.Contains(line, `"kind":"SelfSubjectAccessReview"`) {
					t.Fatalf("incorrect auth context: %s", line)
				}
			}
		})
	}
}

func TestAccessReviewUncertainResponse(t *testing.T) {
	for _, response := range []string{`{}`, `{"status":{"evaluationError":"authorizer unavailable"}}`, `not-json`} {
		t.Run(response, func(t *testing.T) {
			storageCLI(t)
			t.Setenv("AUTH_RAW", response)
			if _, err := ReviewOwnAccess(t.Context(), "", ResourceAccess{Namespace: "images", Verb: "create", Resource: "pods"}); err == nil {
				t.Fatal("unknown response accepted as authorization decision")
			}
		})
	}
}

func TestStorageAuthorizationFindings(t *testing.T) {
	for _, tc := range []struct{ name, response, level, fragment string }{
		{"confirmed denial", `{"status":{"allowed":false}}`, "error", "lacks CDI clone authorization"},
		{"unavailable decision", `{"status":{"evaluationError":"authorizer unavailable"}}`, "warn", "authorization could not be verified"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			storageCLI(t)
			t.Setenv("AUTH_RAW", tc.response)
			request := StorageRequest{Identity: "root", Namespace: "tenant", CDI: true, ExpectedClass: "nas", Claim: StorageClaim{StorageClassName: stringPointer("nas"), AccessModes: []string{"ReadWriteMany"}, VolumeMode: "Filesystem"}, SourceKind: "pvc", Source: NamespacedName{Name: "image", Namespace: "images"}}
			findings := CheckStorage(t.Context(), "", []StorageRequest{request})
			if !findingContains(findings, tc.level, tc.fragment) {
				t.Fatalf("unexpected findings: %+v", findings)
			}
		})
	}
}
