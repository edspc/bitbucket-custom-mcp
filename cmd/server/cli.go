package main

import (
	"errors"
	"fmt"
	"io"
)

const version = "0.4.2"

// Informational commands exit before loading credentials or starting the stdio protocol.
func commandLine(args []string, out io.Writer) (bool, error) {
	if len(args) == 0 {
		return false, nil
	}
	if len(args) != 1 {
		return true, errors.New("expected no arguments, --help or --version")
	}
	var err error
	switch args[0] {
	case "--version":
		_, err = fmt.Fprintf(out, "bitbucket-custom-mcp %s\n", version)
	case "--help", "-h":
		_, err = io.WriteString(out, `Usage: bitbucket-custom-mcp [--help | --version]

Run without arguments to serve MCP over stdin/stdout.
The MCP client starts this process; no background service is needed.

Required environment variables for MCP mode:
  BITBUCKET_WORKSPACE   Workspace slug
  BITBUCKET_EMAIL       Atlassian account email
  BITBUCKET_API_TOKEN   Bitbucket API token; supply through a secret manager

Optional access limits (JSON arrays; unset allows all):
  BITBUCKET_ALLOWED_REPOS      Exact repository slugs; [] denies all
  BITBUCKET_ALLOWED_PROJECTS   Exact project keys or UUIDs; [] denies all
  BITBUCKET_ALLOWED_TOOLS      Exact tool names; [] or ["*"] allows all
  BITBUCKET_ALLOWED_REFS       branch:<name> or tag:<name>; [] denies all

Optional browser confirmation (JSON array; unset or [] confirms every write):
  BITBUCKET_CONFIRM_TOOLS      Exact tool names; ["*"] confirms every tool
  Secret entry always requires the browser page.

--help and --version do not require environment configuration.
Documentation: https://github.com/edspc/bitbucket-custom-mcp#readme
`)
	default:
		err = fmt.Errorf("unknown argument %q; use --help", args[0])
	}
	return true, err
}
