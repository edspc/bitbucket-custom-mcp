package main

import (
	"os"
	"testing"
)

func clearPolicyEnv(t *testing.T) {
	t.Helper()
	for _, name := range []string{"BITBUCKET_ALLOWED_REPOS", "BITBUCKET_ALLOWED_PROJECTS", "BITBUCKET_ALLOWED_REFS", "BITBUCKET_ALLOWED_TOOLS", "BITBUCKET_CONFIRM_TOOLS"} {
		t.Setenv(name, "")
		if err := os.Unsetenv(name); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPolicyEnvDistinguishesUnsetAndDenyAll(t *testing.T) {
	clearPolicyEnv(t)
	p, err := policyFromEnv()
	if err != nil || p.Repositories != nil || p.Projects != nil || p.Refs != nil || p.Tools != nil {
		t.Fatalf("unset: %+v %v", p, err)
	}
	t.Setenv("BITBUCKET_ALLOWED_REPOS", `[]`)
	t.Setenv("BITBUCKET_ALLOWED_PROJECTS", `["APP","{project-id}"]`)
	t.Setenv("BITBUCKET_ALLOWED_REFS", `["branch:main","tag:v1"]`)
	t.Setenv("BITBUCKET_ALLOWED_TOOLS", `["run_pipeline"]`)
	p, err = policyFromEnv()
	if err != nil || p.Repositories == nil || len(p.Repositories) != 0 || len(p.Projects) != 2 || p.Projects[0] != "APP" || p.Projects[1] != "{project-id}" || len(p.Refs) != 2 || len(p.Tools) != 1 {
		t.Fatalf("configured: %+v %v", p, err)
	}
	t.Setenv("BITBUCKET_ALLOWED_PROJECTS", `[]`)
	p, err = policyFromEnv()
	if err != nil || p.Projects == nil || len(p.Projects) != 0 {
		t.Fatalf("empty project list: %+v %v", p, err)
	}
}

func TestPolicyEnvRejectsMalformedValues(t *testing.T) {
	for _, name := range []string{"BITBUCKET_ALLOWED_REPOS", "BITBUCKET_ALLOWED_PROJECTS", "BITBUCKET_ALLOWED_REFS", "BITBUCKET_ALLOWED_TOOLS", "BITBUCKET_CONFIRM_TOOLS"} {
		for _, value := range []string{"", `null`, `"repo-a"`, `[123]`, `[""]`, `[" main "]`, `invalid`} {
			t.Run(name+value, func(t *testing.T) {
				clearPolicyEnv(t)
				t.Setenv(name, value)
				if _, err := policyFromEnv(); err == nil {
					t.Fatalf("accepted malformed %s", name)
				}
			})
		}
	}
}

func TestConfirmationToolsEnv(t *testing.T) {
	clearPolicyEnv(t)
	p, err := policyFromEnv()
	if err != nil || p.ConfirmationTools != nil {
		t.Fatalf("default: %+v %v", p, err)
	}
	t.Setenv("BITBUCKET_CONFIRM_TOOLS", `[]`)
	p, err = policyFromEnv()
	if err != nil || p.ConfirmationTools == nil || len(p.ConfirmationTools) != 0 {
		t.Fatalf("disabled: %+v %v", p, err)
	}
	t.Setenv("BITBUCKET_CONFIRM_TOOLS", `["run_pipeline","get_pipeline"]`)
	p, err = policyFromEnv()
	if err != nil || len(p.ConfirmationTools) != 2 {
		t.Fatalf("configured: %+v %v", p, err)
	}
}
