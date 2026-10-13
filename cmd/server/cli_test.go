package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestCommandLine(t *testing.T) {
	for _, tc := range []struct {
		name    string
		args    []string
		handled bool
		want    string
		wantErr bool
	}{
		{"stdio", nil, false, "", false},
		{"version", []string{"--version"}, true, "bitbucket-custom-mcp " + version + "\n", false},
		{"help", []string{"--help"}, true, "Usage:", false},
		{"short help", []string{"-h"}, true, "Usage:", false},
		{"unknown", []string{"--unknown"}, true, "", true},
		{"positional", []string{"serve"}, true, "", true},
		{"extra argument", []string{"--version", "extra"}, true, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			handled, err := commandLine(tc.args, &out)
			if handled != tc.handled || (err != nil) != tc.wantErr || !strings.Contains(out.String(), tc.want) {
				t.Fatalf("handled=%v, err=%v, output=%q", handled, err, out.String())
			}
			if tc.want == "" && out.Len() != 0 {
				t.Fatalf("unexpected stdout: %q", out.String())
			}
		})
	}
}
