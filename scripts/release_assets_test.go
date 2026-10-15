package scripts_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A small Go CLI stand-in makes version failures and missing notices testable without
// compiling four platforms or depending on the machine's installed modules.
func releaseFixture(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	script, err := os.ReadFile("prepare-release-assets.sh")
	if err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"scripts/prepare-release-assets.sh": string(script),
		"bin/go": `#!/usr/bin/env bash
set -eu
case "$1" in
 run) echo 'bitbucket-custom-mcp 0.4.1' ;;
 env) echo "$PWD/goroot" ;;
 list) echo "example.com/module@v1.0.0 $PWD/module" ;;
 *) exit 1 ;;
esac
`,
		"goroot/LICENSE": "Go license",
		"module/LICENSE": "Module license",
		"module/NOTICE":  "Module notice",
	} {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return root, filepath.Join(root, "bin")
}

func prepareAssets(t *testing.T, root, bin, version, snapshot string) (string, error) {
	t.Helper()
	cmd := exec.Command("bash", filepath.Join(root, "scripts/prepare-release-assets.sh"), version, snapshot)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	output, err := cmd.CombinedOutput()
	return string(output), err
}

func TestReleaseAssetsRejectVersionMismatch(t *testing.T) {
	root, bin := releaseFixture(t)
	out, err := prepareAssets(t, root, bin, "0.5.0", "false")
	if err == nil || !strings.Contains(out, "tag must match") {
		t.Fatalf("mismatched tag accepted: %s %v", out, err)
	}
	if _, err = os.Stat(filepath.Join(root, ".scratch/THIRD_PARTY_LICENSES")); !os.IsNotExist(err) {
		t.Fatal("mismatch reached notice generation")
	}
}

func TestReleaseAssetsKeepSeparateNotices(t *testing.T) {
	for _, snapshot := range []string{"false", "true"} {
		t.Run(snapshot, func(t *testing.T) {
			root, bin := releaseFixture(t)
			version := "0.4.1"
			if snapshot == "true" {
				version = "0.0.0-SNAPSHOT"
			}
			out, err := prepareAssets(t, root, bin, version, snapshot)
			if err != nil {
				t.Fatalf("preparing assets: %v %s", err, out)
			}
			for name, want := range map[string]string{"go/LICENSE": "Go license", "example.com/module@v1.0.0/LICENSE": "Module license", "example.com/module@v1.0.0/NOTICE": "Module notice"} {
				data, err := os.ReadFile(filepath.Join(root, ".scratch/THIRD_PARTY_LICENSES", name))
				if err != nil || string(data) != want {
					t.Fatalf("notice %s: %q %v", name, data, err)
				}
			}
		})
	}
}

func TestReleaseAssetsRejectMissingModuleLicense(t *testing.T) {
	root, bin := releaseFixture(t)
	if err := os.Remove(filepath.Join(root, "module/LICENSE")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "module/NOTICE")); err != nil {
		t.Fatal(err)
	}
	out, err := prepareAssets(t, root, bin, "0.4.1", "false")
	if err == nil || !strings.Contains(out, "No license notices found") {
		t.Fatalf("missing license accepted: %s %v", out, err)
	}
}
