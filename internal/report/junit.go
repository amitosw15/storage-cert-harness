package report

import (
	"encoding/xml"
	"fmt"
	"io"
	"sort"
	"strconv"

	"gitlab.cee.redhat.com/eco-special-projects/storage-cert-harness/internal/core"
)

// writeJUnit renders the report as a JUnit <testsuites> document. Environment and
// attestations become dedicated suites; verdicts are grouped into one suite per
// partner level. See ADR-0014.
func writeJUnit(w io.Writer, r core.Report, hardwareAtRoot bool) error {
	root := juSuites{Name: "csi-cert-harness", Properties: runProperties(r)}

	// --- environment: hardware ---
	hwProps := &juProps{}
	hwProps.add("nodes", strconv.Itoa(r.Environment.Hardware.Nodes))
	addFacts(hwProps, r.Environment.Hardware.Facts)
	if hardwareAtRoot {
		for _, p := range hwProps.Property {
			root.Properties.add(p.Name, p.Value)
		}
	} else {
		root.Suites = append(root.Suites, juSuite{Name: "environment-hardware", Properties: hwProps})
	}

	// --- environment: platform ---
	p := r.Environment.Platform
	pfProps := &juProps{}
	pfProps.add("ocp_version", p.OCPVersion)
	pfProps.add("kube_version", p.KubeVersion)
	pfProps.add("cnv_version", p.CNVVersion)
	pfProps.add("csi_driver.name", p.CSIDriver.Name)
	pfProps.add("csi_driver.version", p.CSIDriver.Version)
	pfProps.add("csi_driver.attach_required", strconv.FormatBool(p.CSIDriver.AttachRequired))
	pfProps.add("csi_driver.fs_group_policy", p.CSIDriver.FSGroupPolicy)
	pfProps.add("csi_driver.volume_lifecycle_modes", join(p.CSIDriver.VolumeLifecycleModes))
	if len(p.CSIDriver.Capabilities) > 0 {
		pfProps.add("csi_driver.capabilities", join(p.CSIDriver.Capabilities))
	}
	if p.Backend != "" {
		pfProps.add("backend", p.Backend)
	}
	for _, op := range p.Operators {
		pfProps.add("operator."+op.Name+".version", op.Version)
	}
	for _, sc := range p.StorageClasses {
		base := "storageclass." + sc.Name
		pfProps.add(base+".provisioner", sc.Provisioner)
		pfProps.add(base+".is_default", strconv.FormatBool(sc.IsDefault))
		pfProps.add(base+".under_test", strconv.FormatBool(sc.UnderTest))
		pfProps.add(base+".volume_binding_mode", sc.VolumeBindingMode)
	}
	addFacts(pfProps, p.Facts)
	for _, s := range r.StorageSelections {
		base := "storage." + core.ExecutionLabel(s.TR, s.Variant)
		if s.Scenario != "" {
			base += "." + s.Scenario
		}
		pfProps.add(base+".storage_class", s.StorageClass)
		pfProps.add(base+".snapshot_class", s.SnapshotClass)
		if s.EffectiveStorageClass != "" {
			pfProps.add(base+".effective_storage_class", s.EffectiveStorageClass)
		}
	}
	root.Suites = append(root.Suites, juSuite{Name: "environment-platform", Properties: pfProps})

	// --- verdicts grouped by partner level ---
	byLevel := map[int][]core.Verdict{}
	var levels []int
	for _, v := range r.Verdicts {
		if _, ok := byLevel[v.Level]; !ok {
			levels = append(levels, v.Level)
		}
		byLevel[v.Level] = append(byLevel[v.Level], v)
	}
	sort.Ints(levels)
	for _, lvl := range levels {
		name := fmt.Sprintf("level-%d", lvl)
		if lvl == 0 {
			name = "uncategorized"
		}
		ts := juSuite{Name: name}
		for _, v := range byLevel[lvl] {
			name := v.Item
			if v.Scenario != "" {
				name += " [" + v.Scenario + "]"
			}
			tc := juCase{Name: name, Classname: core.ExecutionLabel(v.TR, v.Variant), Time: secs(v.DurationS)}
			props := &juProps{}
			if v.Scenario != "" {
				props.add("scenario", v.Scenario)
			}
			if v.Expected != "" {
				props.add("expected", v.Expected)
			}
			if v.Actual != "" {
				props.add("actual", v.Actual)
			}
			if len(props.Property) > 0 {
				tc.Properties = props
			}
			switch v.Outcome {
			case core.OutcomeFail:
				tc.Failure = &juMsg{Message: v.Reason}
				ts.Failures++
			case core.OutcomeError:
				tc.Error = &juMsg{Message: v.Reason}
				ts.Errors++
			case core.OutcomeSkip:
				tc.Skipped = &juMsg{Message: v.Reason}
				ts.Skipped++
			}
			ts.Tests++
			ts.Cases = append(ts.Cases, tc)
		}
		root.Suites = append(root.Suites, ts)
	}

	// --- selected measurements ---
	if len(r.Measurements) > 0 {
		ts := juSuite{Name: "measurements"}
		for _, m := range r.Measurements {
			name := measurementLabel(m)
			if m.Scenario != "" {
				name += " [" + m.Scenario + "]"
			}
			tc := juCase{Name: name, Classname: core.ExecutionLabel(m.TR, m.Variant)}
			tc.Properties = &juProps{}
			tc.Properties.add("value", strconv.FormatFloat(m.Value, 'f', -1, 64))
			if m.Unit != "" {
				tc.Properties.add("unit", m.Unit)
			}
			if m.Percentile != "" {
				tc.Properties.add("percentile", m.Percentile)
			}
			if m.Scenario != "" {
				tc.Properties.add("scenario", m.Scenario)
			}
			ts.Tests++
			ts.Cases = append(ts.Cases, tc)
		}
		root.Suites = append(root.Suites, ts)
	}

	// --- attestations (self-reported) ---
	if len(r.Attestations) > 0 {
		ts := juSuite{Name: "attestations"}
		if r.Attestations[0].SignedBy != "" {
			ts.Properties = &juProps{}
			ts.Properties.add("signed_by", r.Attestations[0].SignedBy)
		}
		for _, a := range r.Attestations {
			tc := juCase{Name: a.Claim, Properties: &juProps{}}
			tc.Properties.add("value", a.Value)
			ts.Tests++
			ts.Cases = append(ts.Cases, tc)
		}
		root.Suites = append(root.Suites, ts)
	}

	if len(r.Warnings) > 0 {
		props := &juProps{}
		for _, warning := range r.Warnings {
			props.add("warning", warning)
		}
		root.Suites = append(root.Suites, juSuite{Name: "report-warnings", Properties: props})
	}

	// roll totals up to the root
	for _, s := range root.Suites {
		root.Tests += s.Tests
		root.Failures += s.Failures
		root.Errors += s.Errors
		root.Skipped += s.Skipped
	}

	if _, err := io.WriteString(w, xml.Header); err != nil {
		return err
	}
	enc := xml.NewEncoder(w)
	enc.Indent("", "  ")
	if err := enc.Encode(root); err != nil {
		return err
	}
	_, err := io.WriteString(w, "\n")
	return err
}

// runProperties is deliberately small and stable. JUnit consumers use these
// fields to decide whether the run is clean; report.json remains the detailed
// source for incident diagnostics and full run composition.
func runProperties(r core.Report) *juProps {
	p := &juProps{}
	p.add("harness.junit_profile_version", "1")
	p.add("harness.run_id", r.RunID)
	p.add("harness.status", string(r.Status))
	p.add("harness.health", string(r.Health))
	p.add("harness.certifiable", strconv.FormatBool(r.Certifiable))
	p.add("harness.incidents_present", strconv.FormatBool(len(r.Incidents) > 0))
	p.add("harness.incident_count", strconv.Itoa(len(r.Incidents)))
	p.add("harness.report_json", "report.json")
	p.add("harness.composition.parts_count", strconv.Itoa(r.Composition.PartsCount))
	if r.CompletionReason != "" {
		p.add("harness.completion_reason", r.CompletionReason)
	}
	var parts []string
	for _, part := range r.Composition.Parts {
		parts = append(parts, part.PartID)
	}
	if len(parts) > 0 {
		p.add("harness.composition.parts", join(parts))
	}
	if len(r.Incidents) > 0 {
		var ids []string
		for _, incident := range r.Incidents {
			if incident.ID != "" {
				ids = append(ids, incident.ID)
			}
		}
		if len(ids) > 0 {
			p.add("harness.incident_ids", join(ids))
		}
	}
	return p
}

// secs formats a duration in seconds for a JUnit time attribute.
func secs(d float64) string { return strconv.FormatFloat(d, 'f', 3, 64) }

// measurementLabel is "name" or "name@percentile".
func measurementLabel(m core.Measurement) string {
	if m.Percentile != "" {
		return m.Name + "@" + m.Percentile
	}
	return m.Name
}

func addFacts(p *juProps, facts []core.Fact) {
	for _, f := range facts {
		p.add(f.Key, f.Value)
		if f.Source != "" {
			p.add("_source."+f.Key, f.Source)
		}
	}
}

func join(s []string) string {
	out := ""
	for i, v := range s {
		if i > 0 {
			out += ","
		}
		out += v
	}
	return out
}

// ---- JUnit XML wire structs ----

type juSuites struct {
	XMLName    xml.Name  `xml:"testsuites"`
	Name       string    `xml:"name,attr"`
	Tests      int       `xml:"tests,attr"`
	Failures   int       `xml:"failures,attr"`
	Errors     int       `xml:"errors,attr"`
	Skipped    int       `xml:"skipped,attr"`
	Properties *juProps  `xml:"properties,omitempty"`
	Suites     []juSuite `xml:"testsuite"`
}

type juSuite struct {
	Name       string   `xml:"name,attr"`
	Tests      int      `xml:"tests,attr"`
	Failures   int      `xml:"failures,attr"`
	Errors     int      `xml:"errors,attr"`
	Skipped    int      `xml:"skipped,attr"`
	Properties *juProps `xml:"properties,omitempty"`
	Cases      []juCase `xml:"testcase"`
}

type juProps struct {
	Property []juProp `xml:"property"`
}

func (p *juProps) add(name, value string) {
	p.Property = append(p.Property, juProp{Name: name, Value: value})
}

type juProp struct {
	Name  string `xml:"name,attr"`
	Value string `xml:"value,attr"`
}

type juCase struct {
	Name       string   `xml:"name,attr"`
	Classname  string   `xml:"classname,attr,omitempty"`
	Time       string   `xml:"time,attr,omitempty"` // seconds a verdict's test took; unset for informational measurements
	Properties *juProps `xml:"properties,omitempty"`
	Failure    *juMsg   `xml:"failure,omitempty"`
	Error      *juMsg   `xml:"error,omitempty"`
	Skipped    *juMsg   `xml:"skipped,omitempty"`
}

type juMsg struct {
	Message string `xml:"message,attr,omitempty"`
}
