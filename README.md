# bitbucket-custom-mcp

Custom [MCP](https://modelcontextprotocol.io) server for Bitbucket Cloud,
written in Go. It speaks MCP over stdio and calls the Bitbucket REST API on
behalf of the user.

## Stack

- Go
- [modelcontextprotocol/go-sdk](https://github.com/modelcontextprotocol/go-sdk)
- `log/slog` to stderr — stdout carries the MCP protocol

## Running commands

| Task    | Command                                                    |
|---------|------------------------------------------------------------|
| Run     | `go run ./cmd/server`                                      |
| Build   | `make build` — binary in `bin/`                            |
| Install | `make install` — copies it to `~/.local/bin` (override with `BINDIR=…`) |
| Lint    | `make lint` (`golangci-lint run ./...`)                    |
| Tests   | `make test` (`go test -race ./...`)                        |

## Configuration

Configuration comes from environment variables only. The Bitbucket token is a
secret: in the MCP client's `env` reference it as `"${VAR}"`, a 1Password
secret reference (see below) or keep it in a user-scoped config — never a
literal value in a committed file such as `.mcp.json`.

| Variable              | Value                                              |
|-----------------------|----------------------------------------------------|
| `BITBUCKET_WORKSPACE` | Workspace slug; both repositories of a link are in it |
| `BITBUCKET_EMAIL`     | Atlassian account email                            |
| `BITBUCKET_API_TOKEN` | Atlassian API token for Bitbucket (Basic auth)     |

The API token needs these scopes: `read:repository:bitbucket`,
`admin:repository:bitbucket`, `write:ssh-key:bitbucket`,
`delete:ssh-key:bitbucket`, `read:pipeline:bitbucket`,
`admin:pipeline:bitbucket`.

## Tools

Both tools link a source repository A to a target repository B in the
configured workspace. Before changing anything they check that pipelines are
enabled in both repositories, and they report a conflict instead of
overwriting it unless `force` is set.

### `link_repos_by_key`

Adds the public SSH key of A's pipelines to the Access Keys of B, labelled with
A's slug, so A's pipelines can clone B.

| Input         | Meaning                                              |
|---------------|------------------------------------------------------|
| `source_repo` | A                                                    |
| `target_repo` | B                                                    |
| `force`       | Replace an Access Key labelled with A's slug that holds a different key |

If B already has the same key, nothing changes. A must have a pipelines SSH
key (Repository settings → Pipelines → SSH keys).

### `link_repos_by_token`

Stores a repository access token of A as a secured pipeline variable of B.

| Input           | Meaning                                                    |
|-----------------|------------------------------------------------------------|
| `source_repo`   | A                                                          |
| `target_repo`   | B                                                          |
| `variable_name` | Name of the variable in B                                  |
| `scope`         | `repository` (all pipelines) or `deployment`               |
| `environment`   | Deployment environment name in B, with `scope: deployment` |
| `permissions`   | Token permissions: `repository`, `repository:write`, `repository:admin`, `repository:delete`, `pullrequest`, `pullrequest:write`, `webhook`, `pipeline`, `pipeline:write`, `pipeline:variable` |
| `force`         | Overwrite the variable if it exists                        |

Bitbucket has no API to create repository access tokens, so this step is
manual. For the duration of the call the server opens a one-off local page on
`127.0.0.1` (random port, unguessable path) and asks the MCP client, through
URL-mode elicitation, to send the user there. The page links to A's access
token page and lists the permissions to select; the user creates the token and
pastes it into the page. The token goes from the browser straight to this
server, which checks it against A and writes it into the secured variable. It
never passes through the MCP client or the model and is not returned.

The page closes as soon as the token arrives, when the user declines, or after
10 minutes; at most 3 pages are open at a time. The MCP client must support URL
elicitation (MCP 2025-11-25 or later). The token is checked by reading
something in A that its permissions allow (the repository, pipelines or
webhooks), which catches a token created in another repository.

## Setup in Claude Code

1. Build and install the binary into `~/.local/bin`:

   ```sh
   make install
   ```

2. Store the Atlassian API token in 1Password and copy its secret reference,
   `op://<vault>/<item>/<field>`. This needs the
   [1Password CLI](https://developer.1password.com/docs/cli/) signed in;
   the desktop app integration lets you approve access with biometrics.

3. Register the server for your user. Only the `op://` reference lands in
   `~/.claude.json`, never the token itself. The binary path must be absolute
   (`~` is not expanded inside the JSON):

   ```sh
   claude mcp add-json --scope user bitbucket-custom '{
     "type": "stdio",
     "command": "op",
     "args": ["run", "--no-masking", "--", "/Users/<you>/.local/bin/bitbucket-custom-mcp"],
     "env": {
       "BITBUCKET_WORKSPACE": "<workspace>",
       "BITBUCKET_EMAIL": "<atlassian-email>",
       "BITBUCKET_API_TOKEN": "op://<vault>/<item>/<field>"
     }
   }'
   ```

   `op run` resolves environment variables that hold secret references and
   starts the server with the real values. `--no-masking` is required: masking
   rewrites the child's stdout, which carries the MCP protocol.

4. Check: `claude mcp list` shows `bitbucket-custom` as connected, and `/mcp`
   in a Claude Code session lists both tools. 1Password asks to approve access
   when the server starts.

Never commit a `.mcp.json` with the token; a project `.mcp.json` may hold only
the `op://` reference.

## Workflow

See [.claude/rules/lifecycle.md](.claude/rules/lifecycle.md).
