package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestEmptySelectorRejectedBeforeLoadingInputs(t *testing.T) {
	for _, flag := range []string{"id", "tool"} {
		for _, command := range []string{"run", "preflight"} {
			for _, tc := range []struct {
				name string
				args []string
			}{
				{"empty", []string{"--" + flag, ""}},
				{"whitespace", []string{"--" + flag, " "}},
				{"empty comma entry", []string{"--" + flag, "value,"}},
			} {
				t.Run(command+"/"+flag+"/"+tc.name, func(t *testing.T) {
					cmd := newRootCmd()
					cmd.SetOut(&bytes.Buffer{})
					cmd.SetArgs(append([]string{command, "--catalog", "missing.json"}, tc.args...))
					err := cmd.Execute()
					if err == nil || !strings.Contains(err.Error(), "--"+flag+" requires nonempty values") {
						t.Fatalf("error = %v; want empty selector error", err)
					}
				})
			}
		}
	}
}
