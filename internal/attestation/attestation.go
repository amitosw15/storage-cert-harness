// Package attestation gathers partner-declared, unobservable claims (including
// the tested storage-array identity and Level-3 requirements a run cannot
// measure from the cluster) and records them on the report. Questions come from
// a bundled default set, overridable with a file; answers can be supplied
// non-interactively (the --attestations file) or collected via an interactive
// prompt. See ADR-0014.
package attestation

import (
	"bufio"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/openshift-storage-cert/storage-cert-harness/internal/core"
	"github.com/openshift-storage-cert/storage-cert-harness/internal/safefs"
)

//go:embed questions.yaml
var defaultQuestions []byte

// Question is one attestation prompt: the claim key it fills, the human-readable
// prompt, optional help text, and whether an answer is mandatory.
type Question struct {
	Claim    string `yaml:"claim" json:"claim"`
	Prompt   string `yaml:"prompt" json:"prompt"`
	Help     string `yaml:"help,omitempty" json:"help,omitempty"`
	Required bool   `yaml:"required,omitempty" json:"required,omitempty"`
}

// LoadQuestions returns the attestation questions. With an empty path it returns
// the bundled defaults; otherwise it reads the override file (same YAML shape).
// path may be absolute or relative to the working directory; ".." components are
// rejected, but any other resolved path is accepted as an operator-selected input.
func LoadQuestions(path string) ([]Question, error) {
	data := defaultQuestions
	if path != "" {
		if pathContainsDotDot(path) {
			return nil, fmt.Errorf("attestation questions: path traversal rejected")
		}
		b, err := safefs.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("attestation questions: %w", err)
		}
		data = b
	}
	var qs []Question
	if err := yaml.Unmarshal(data, &qs); err != nil {
		return nil, fmt.Errorf("attestation questions: %w", err)
	}
	for i, q := range qs {
		if q.Claim == "" {
			return nil, fmt.Errorf("attestation questions: entry %d has no claim", i)
		}
	}
	return qs, nil
}

// Prompt interactively gathers attestation answers. It first asks whether to fill
// them at all; a declined gate (or an empty question set) yields no attestations.
// Each answered question becomes a core.Attestation carrying the shared signer.
// Required questions are re-asked until answered; optional ones accept a blank
// skip. in/out are injectable for testing; a cancelled ctx aborts.
func Prompt(ctx context.Context, questions []Question, in io.Reader, out io.Writer) ([]core.Attestation, error) {
	if len(questions) == 0 {
		return nil, nil
	}
	r := bufio.NewReader(in)
	// Prompt writes go to a user terminal; a failed write is not worth aborting on.
	emit := func(format string, args ...any) { _, _ = fmt.Fprintf(out, format, args...) }

	emit("\nSome Level-3 requirements (reference architecture, published SLAs) cannot be\n" +
		"measured from the cluster and need a human answer.\n" +
		"Fill in manual attestations now? [y/N]: ")
	gate, err := readLine(ctx, r)
	if err != nil {
		return nil, err
	}
	if !isYes(gate) {
		return nil, nil
	}

	emit("Signed by (name / email): ")
	signedBy, err := readLine(ctx, r)
	if err != nil {
		return nil, err
	}
	signedBy = strings.TrimSpace(signedBy)

	var atts []core.Attestation
	for _, q := range questions {
		for {
			if q.Help != "" {
				emit("\n# %s\n", q.Help)
			}
			label := q.Prompt
			if !q.Required {
				label += " (optional, blank to skip)"
			}
			emit("%s: ", label)
			ans, err := readLine(ctx, r)
			if err != nil {
				return nil, err
			}
			ans = strings.TrimSpace(ans)
			if ans == "" {
				if q.Required {
					emit("  (required — please provide a value)\n")
					continue
				}
				break
			}
			atts = append(atts, core.Attestation{Claim: q.Claim, Value: ans, SignedBy: signedBy})
			break
		}
	}
	return atts, nil
}

// WriteFile writes attestations back to the {signed_by, claims{}} file shape that
// --attestations reads, so a set gathered interactively can be reused on a later
// run and kept as an auditable artifact.
func WriteFile(path string, atts []core.Attestation) error {
	doc := struct {
		SignedBy string            `json:"signed_by,omitempty"`
		Claims   map[string]string `json:"claims"`
	}{Claims: map[string]string{}}
	for _, a := range atts {
		doc.Claims[a.Claim] = a.Value
		if doc.SignedBy == "" {
			doc.SignedBy = a.SignedBy
		}
	}
	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	return safefs.WriteFile(path, append(b, '\n'), 0o600)
}

func readLine(ctx context.Context, r *bufio.Reader) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	line, err := r.ReadString('\n')
	if err != nil && err != io.EOF {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

func isYes(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "y", "yes":
		return true
	default:
		return false
	}
}

func pathContainsDotDot(path string) bool {
	for _, part := range strings.Split(filepath.ToSlash(path), "/") {
		if part == ".." {
			return true
		}
	}
	return false
}
