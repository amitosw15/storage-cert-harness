package main

import (
	"fmt"
	"io"
	"strings"
)

type preflightOutput struct {
	out                     io.Writer
	passed, failed, skipped int
	err                     error
}

func (p *preflightOutput) add(status, tool, message string) {
	switch status {
	case "PASS":
		p.passed++
	case "FAIL":
		p.failed++
	case "SKIP":
		p.skipped++
	}
	// Tool errors can include multiline command output. Keep each check on one line.
	line := strings.Join(strings.Fields(tool+": "+message), " ")
	if _, err := fmt.Fprintf(p.out, "[%s] %s\n", status, line); err != nil && p.err == nil {
		p.err = err
	}
}

func (p *preflightOutput) finish() error {
	if _, err := fmt.Fprintf(p.out, "Preflight summary: %d PASS, %d FAIL, %d SKIP\n", p.passed, p.failed, p.skipped); err != nil && p.err == nil {
		p.err = err
	}
	if p.err != nil {
		return p.err
	}
	if p.failed > 0 {
		return fmt.Errorf("%d preflight problem(s)", p.failed)
	}
	return nil
}
