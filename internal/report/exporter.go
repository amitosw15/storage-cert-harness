package report

import (
	"io"

	"github.com/openshift-storage-cert/storage-cert-harness/internal/core"
)

// Exporter renders a core.Report into a concrete wire format. Adding a format is
// one implementation; the assembled Report is the single source of truth. See
// ADR-0014.
type Exporter interface {
	Format() string // "json" | "markdown" | "junit"
	Export(w io.Writer, r core.Report) error
}

// JSONExporter renders the report as indented JSON.
type JSONExporter struct{}

func (JSONExporter) Format() string                          { return "json" }
func (JSONExporter) Export(w io.Writer, r core.Report) error { return WriteJSON(w, r) }

// MarkdownExporter renders the human-readable summary.
type MarkdownExporter struct{}

func (MarkdownExporter) Format() string                          { return "markdown" }
func (MarkdownExporter) Export(w io.Writer, r core.Report) error { return WriteMarkdown(w, r) }

// JUnitExporter renders the report as a JUnit <testsuites> document.
type JUnitExporter struct {
	// HardwareAtRoot emits environment facts as <testsuites>-level <properties>
	// instead of an "environment-hardware" testsuite. Less portable; off by default.
	HardwareAtRoot bool
}

func (JUnitExporter) Format() string { return "junit" }
func (e JUnitExporter) Export(w io.Writer, r core.Report) error {
	return writeJUnit(w, r, e.HardwareAtRoot)
}
