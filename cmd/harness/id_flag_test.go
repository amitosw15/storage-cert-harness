package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestEmptyIDRejectedBeforeLoadingInputs(t *testing.T) {
	for _, command := range []string{"run", "preflight"} {
		for _, tc := range []struct {
			name string
			args []string
		}{
			{"empty", []string{"--id", ""}},
			{"whitespace", []string{"--id", " "}},
			{"empty comma entry", []string{"--id", "EX-001,"}},
		} {
			t.Run(command+"/"+tc.name, func(t *testing.T) {
				cmd := newRootCmd()
				cmd.SetOut(&bytes.Buffer{})
				cmd.SetArgs(append([]string{command, "--catalog", "missing.json"}, tc.args...))
				err := cmd.Execute()
				if err == nil || !strings.Contains(err.Error(), "--id requires nonempty test IDs") {
					t.Fatalf("error = %v; want empty ID error", err)
				}
			})
		}
	}
}
