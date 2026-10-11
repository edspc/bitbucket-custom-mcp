package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/edspc/bitbucket-custom-mcp/internal/tools"
)

func policyFromEnv() (tools.Policy, error) {
	var policy tools.Policy
	for _, field := range []struct {
		name string
		list *[]string
	}{
		{"BITBUCKET_ALLOWED_REPOS", &policy.Repositories},
		{"BITBUCKET_ALLOWED_TOOLS", &policy.Tools},
		{"BITBUCKET_ALLOWED_REFS", &policy.Refs},
	} {
		value, set := os.LookupEnv(field.name)
		if !set {
			continue
		}
		if err := json.Unmarshal([]byte(value), field.list); err != nil || *field.list == nil {
			return tools.Policy{}, fmt.Errorf("%s must be a JSON array of exact allowed names (an empty array denies all)", field.name)
		}
		for _, entry := range *field.list {
			if entry == "" || strings.TrimSpace(entry) != entry {
				return tools.Policy{}, fmt.Errorf("%s must not contain empty names or surrounding whitespace", field.name)
			}
		}
	}
	return policy, nil
}
