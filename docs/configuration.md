# Configuration

Configuration comes from environment variables only. The Bitbucket token is a
secret that reaches every repository its account can, so only this server's
process should get it: start the server through a secret manager that
resolves a reference at launch, such as 1Password's `op run` (see
[Setup in Claude Code](claude-code.md)). Do not export the token in a
shell profile, which `"${VAR}"` in the MCP client's `env` needs: the client
passes its environment to every MCP server, hook and shell command it starts,
so other servers and any command a prompt injection gets run could read it.
Do not write the token itself into any config either, `~/.claude.json`
included: every process of the user can read it.

| Variable              | Value                                              |
|-----------------------|----------------------------------------------------|
| `BITBUCKET_WORKSPACE` | Workspace slug; every repository the tools work with is in it |
| `BITBUCKET_EMAIL`     | Atlassian account email                            |
| `BITBUCKET_API_TOKEN` | Atlassian API token for Bitbucket (Basic auth)     |

The API token needs these scopes: `read:repository:bitbucket`,
`admin:repository:bitbucket`, `write:ssh-key:bitbucket`,
`delete:ssh-key:bitbucket`, `read:pipeline:bitbucket`,
`write:pipeline:bitbucket`, `admin:pipeline:bitbucket`,
`read:pullrequest:bitbucket`.

## Confirmation and access limits

By default, every tool that changes Bitbucket requires a one-time confirmation
on the server's local browser page, including operations that take no secrets.
Set `BITBUCKET_CONFIRM_TOOLS` to a JSON array of exact tool names to require
confirmation only for those tool calls, including read tools if desired. The
entry `"*"` matches every tool; `["*"]` confirms both reads and writes.
For example, `["run_pipeline","delete_variable","create_deployment"]` confirms
those three tools. An unset variable or `[]` keeps the default: confirm every
write, without confirmation for reads. Invalid arrays prevent startup. This
setting is independent of `BITBUCKET_ALLOWED_TOOLS` and never grants access denied by an allowlist.
Calls requiring secret entry always open the browser page, regardless of this
setting; secret values still cannot be passed as tool arguments. Resource reads
and subscriptions use access limits, not tool-call confirmation. The MCP
client must support URL elicitation (MCP 2025-11-25 or later) for calls requiring
confirmation or secret entry; without it, those calls are refused. Opening the
page or returning `accept` through MCP does not approve a change: the user must review the arguments and submit the approval checkbox.
Approvals are bound to the tool, its arguments and the MCP session, expire after
10 minutes and cannot be reused.

The page shows the workspace and complete non-secret arguments. For a pipeline
run, the server resolves the revision to a full commit SHA before approval and
runs that SHA even if the branch moves while the page is open. Both plain
variables and the names of secured variables are shown. A schedule authorizes
future runs of the selected branch, whose head can change over time.

Variable changes also show the resolved deployment environment UUID and the UUID
of a variable being replaced. If either destination or replacement changes while
approval is pending, the operation is refused and requires a fresh confirmation.
The destination, replacement UUID and secured flag are checked again after browser
submission, immediately before a variable is written or deleted. Token links also
finish the read-access probe before this final check. A key link likewise reads
the source's public key and the target's Access Keys again before it changes
them, and a schedule deletion reads the schedule again.

These optional environment variables narrow the server's authority. Each is a
JSON array of names. An unset variable allows all names within the configured
workspace. For `BITBUCKET_ALLOWED_TOOLS`, `[]` also keeps that default and the
entry `"*"` allows every tool. Repository, project and ref lists use exact names
without wildcard expansion; `[]` denies all for those three lists, except that
repository and project lists add up when both are set (see below). Invalid
arrays stop the server rather than silently removing the restriction.

| Variable | Example | Restriction |
|----------|---------|-------------|
| `BITBUCKET_ALLOWED_REPOS` | `["repo-a","repo-b"]` | These repositories, in any project; applies to both repositories of a link and all repository tools/resources; workspace variables are disabled when this list is set |
| `BITBUCKET_ALLOWED_PROJECTS` | `["APP","{01234567-89ab-cdef-0123-456789abcdef}"]` | Repositories in these projects in the configured workspace; exact project keys or UUIDs, not display names; workspace variables are disabled when this list is set |
| `BITBUCKET_ALLOWED_TOOLS` | `["get_pull_request","list_pull_requests"]` | Only these tools can execute; unset, `[]` or `["*"]` allows all; pipeline resources and subscriptions also require `get_pipeline` |
| `BITBUCKET_ALLOWED_REFS` | `["branch:main","tag:v1.2.0"]` | Only these refs can run pipelines, only at commits on them, or have schedules added/deleted |

Repository and project lists add up: when both are configured, a repository is
allowed if either list allows it. For example, `BITBUCKET_ALLOWED_PROJECTS='["APP","OPS"]'`
allows repositories in either project, and adding `BITBUCKET_ALLOWED_REPOS='["tools"]'`
also allows `tools` wherever it is. With both set, `[]` in one list adds nothing,
so access comes from the other list alone. Use project UUIDs to keep the
restriction tied to the same project if its key changes.

Earlier releases intersected the two lists: a repository had to satisfy both. A
configuration with both lists set now allows more; review it when upgrading.

With project restrictions, each repository API request first reads its current
`project.key` and `project.uuid` using the server's account, unless the
repository is listed in `BITBUCKET_ALLOWED_REPOS`. This adds one metadata
request per API request and requires repository read access. Missing project
metadata or a failed lookup refuses the operation. Membership is never cached,
so moves out of an allowed project are checked after browser approval and during
polling. Both repositories of a link are checked, including its source again
before the write. Project checks before a write must finish within its approval's
deadline; a write already sent can complete after that deadline. Workspace-wide
operations and commit lookups in a fork outside the configured workspace are
refused; `search_repositories` instead narrows its workspace listing to the
allowed repositories and projects and filters the answer (see
[Repository search](tools.md#repository-search)). Bitbucket provides no atomic project check
and write: an administrator can still move a repository between those requests.

Keep these settings in the operator's environment or user-scoped MCP config,
outside the model's control. Use a dedicated account/token with only the
Bitbucket permissions needed by that environment.
