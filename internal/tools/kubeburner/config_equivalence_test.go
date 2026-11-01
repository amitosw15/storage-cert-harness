package kubeburner

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/openshift-storage-cert/storage-cert-harness/internal/core"
	"github.com/openshift-storage-cert/storage-cert-harness/internal/manifest"
)

type configCase struct {
	Name    string         `json:"name"`
	Params  map[string]any `json:"params"`
	Batches []int          `json:"batches"`
}

func TestConfigMatchesBaseline(t *testing.T) {
	runConfigEquivalence(t)
}

func runConfigEquivalence(t *testing.T) {
	t.Helper()
	root := filepath.Join("testdata", "config-equivalence")
	raw, err := os.ReadFile(filepath.Join(root, "cases.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cases []configCase
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	for _, tc := range cases {
		t.Run(tc.Name, func(t *testing.T) {
			p := resolveParams([]core.TestRequirement{{Params: tc.Params}})
			trID := "TR-VIRT-010"
			if tc.Name == "zero" {
				trID = "TR-VIRT-027"
			}
			if err := p.validateFor(trID); err != nil {
				t.Fatal(err)
			}
			rendered := []byte(renderConfig(t, p))
			docs, err := manifest.Documents(rendered)
			if err != nil {
				t.Fatal(err)
			}
			canonical, err := json.MarshalIndent(docs, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			canonical = append(canonical, '\n')
			jobs := docs[0]["jobs"].([]any)
			var batches []int
			for i, entry := range jobs[1:] {
				job := entry.(map[string]any)
				obj := job["objects"].([]any)[0].(map[string]any)
				batches = append(batches, obj["replicas"].(int))
				wantName := fmt.Sprintf("vmsnapshot-snapshot-%d", i+1)
				wantTemplate := "templates/vmsnapshot-batch.yaml"
				if tc.Name == "legacy" {
					wantName, wantTemplate = "vmsnapshot-snapshot", "templates/vmsnapshot.yaml"
				}
				if job["name"] != wantName || obj["objectTemplate"] != wantTemplate {
					t.Fatalf("unexpected snapshot job: %v", job)
				}
				if tc.Name != "legacy" && obj["inputVars"].(map[string]any)["snapshotBatch"] != i+1 {
					t.Fatalf("incorrect batch number: %v", obj)
				}
			}
			if len(batches) != len(tc.Batches) || (len(batches) > 0 && !reflect.DeepEqual(batches, tc.Batches)) {
				t.Fatalf("batches = %v, want %v", batches, tc.Batches)
			}
			path := filepath.Join(root, tc.Name+".golden.json")
			if tc.Name == "large" {
				path = filepath.Join(root, tc.Name+".golden.sha256")
				canonical = fmt.Appendf(nil, "%x\n", sha256.Sum256(canonical))
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(canonical, want) {
				t.Fatalf("rendered configuration differs from frozen baseline %s", path)
			}
		})
	}
}
