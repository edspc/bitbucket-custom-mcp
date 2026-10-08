# bitbucket-custom-mcp

Custom [MCP](https://modelcontextprotocol.io) server for Bitbucket Cloud,
written in Go. It speaks MCP over stdio and calls the Bitbucket REST API on
behalf of the user.

## Stack

- Go
- [modelcontextprotocol/go-sdk](https://github.com/modelcontextprotocol/go-sdk)
- `log/slog` to stderr — stdout carries the MCP protocol

## Running commands

| Task  | Command                                       |
|-------|-----------------------------------------------|
| Run   | `go run ./cmd/server`                         |
| Lint  | `golangci-lint run ./...`                     |
| Tests | `go test -race ./...`                         |

## Configuration

Configuration comes from environment variables only. The Bitbucket token is a
secret: in the MCP client's `env` reference it as `"${VAR}"` or keep it in a
user-scoped config — never a literal value in a committed file such as
`.mcp.json`.

## Tools

None yet.

## Workflow

See [.claude/rules/lifecycle.md](.claude/rules/lifecycle.md).
