package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Exercise the executable, including its stdout framing and process shutdown, as installed
// by a package manager. Fake credentials and deny-all policies keep this check offline.
func TestInstalledBinary(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	binary := filepath.Join(t.TempDir(), "bitbucket-custom-mcp")
	build := exec.CommandContext(ctx, "go", "build", "-trimpath", "-o", binary, ".")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, output)
	}
	var env []string
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "BITBUCKET_") {
			env = append(env, entry)
		}
	}
	for _, arg := range []string{"--help", "-h", "--version", "--unknown"} {
		t.Run(arg, func(t *testing.T) {
			cmd := exec.CommandContext(ctx, binary, arg)
			// Even invalid configuration must not affect informational commands.
			cmd.Env = append(append([]string{}, env...), "BITBUCKET_ALLOWED_PROJECTS=invalid")
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			err := cmd.Run()
			if arg == "--unknown" {
				if err == nil || stdout.Len() != 0 || !strings.Contains(stderr.String(), "unknown argument") {
					t.Fatalf("unknown argument: %v, stdout=%q, stderr=%q", err, stdout.String(), stderr.String())
				}
				return
			}
			if err != nil || stderr.Len() != 0 {
				t.Fatalf("informational command: %v, stderr=%q", err, stderr.String())
			}
			if arg == "--version" && stdout.String() != "bitbucket-custom-mcp "+version+"\n" || arg != "--version" && !strings.Contains(stdout.String(), "Usage:") {
				t.Fatalf("unexpected output: %q", stdout.String())
			}
		})
	}
	cmd := exec.CommandContext(ctx, binary)
	cmd.Env = append(env,
		"BITBUCKET_WORKSPACE=offline-test", "BITBUCKET_EMAIL=test@example.invalid", "BITBUCKET_API_TOKEN=not-a-real-token",
		"BITBUCKET_ALLOWED_REPOS=[]", "BITBUCKET_ALLOWED_PROJECTS=[]", "BITBUCKET_ALLOWED_TOOLS=[]", "BITBUCKET_ALLOWED_REFS=[]")
	client := mcp.NewClient(&mcp.Implementation{Name: "package-test", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: cmd}, &mcp.ClientSessionOptions{ProtocolVersion: "2025-11-25"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	if info := session.InitializeResult().ServerInfo; info.Name != "bitbucket-custom-mcp" || info.Version != version {
		t.Fatalf("server info = %+v", info)
	}
	list, err := session.ListTools(ctx, nil)
	if err != nil || len(list.Tools) == 0 {
		t.Fatalf("list tools: %+v, %v", list, err)
	}
	if err = session.Close(); err != nil {
		t.Fatalf("stdio shutdown: %v", err)
	}
}
