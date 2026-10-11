package main

import (
	"os"
	"testing"
)

func clearPolicyEnv(t *testing.T) {
	t.Helper()
	for _, name := range []string{"BITBUCKET_ALLOWED_REPOS", "BITBUCKET_ALLOWED_REFS", "BITBUCKET_ALLOWED_TOOLS"} {
		t.Setenv(name, "")
		if err := os.Unsetenv(name); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPolicyEnvDistinguishesUnsetAndDenyAll(t *testing.T) {
	clearPolicyEnv(t)
	p, err := policyFromEnv()
	if err != nil || p.Repositories != nil || p.Refs != nil || p.Tools != nil {
		t.Fatalf("unset: %+v %v", p, err)
	}
	t.Setenv("BITBUCKET_ALLOWED_REPOS", `[]`)
	t.Setenv("BITBUCKET_ALLOWED_REFS", `["branch:main","tag:v1"]`)
	t.Setenv("BITBUCKET_ALLOWED_TOOLS", `["run_pipeline"]`)
	p, err = policyFromEnv()
	if err != nil || p.Repositories == nil || len(p.Repositories) != 0 || len(p.Refs) != 2 || len(p.Tools) != 1 {
		t.Fatalf("configured: %+v %v", p, err)
	}
}

func TestPolicyEnvRejectsMalformedValues(t *testing.T) {
	for _, name := range []string{"BITBUCKET_ALLOWED_REPOS", "BITBUCKET_ALLOWED_REFS", "BITBUCKET_ALLOWED_TOOLS"} {
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
