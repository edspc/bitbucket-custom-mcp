# CLI

`bitbucket-custom-mcp --version` prints `bitbucket-custom-mcp 0.5.1` and exits.
`--help` (or `-h`) prints usage and configuration names. These commands need no
credentials and ignore environment configuration, even malformed policy arrays.
Unknown arguments fail. With no arguments the executable serves MCP over stdio;
only protocol messages go to stdout, with logs on stderr.

The version in `cmd/server/cli.go` is shared by `--version` and MCP `initialize`.
No linker flags or generated files are needed to build a release from a source
archive.
