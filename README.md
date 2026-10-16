# bitbucket-custom-mcp

Custom [MCP](https://modelcontextprotocol.io) server for Bitbucket Cloud,
written in Go. It speaks MCP over stdio and calls the Bitbucket REST API on
behalf of the user.

Licensed under the [MIT License](LICENSE).

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
| Smoke test | `make smoke` — builds and starts the executable, initializes MCP and lists tools without contacting Bitbucket |
| Release archives | `make release` — packages the clean tagged commit with GoReleaser into `dist/` |
| Snapshot archives | `make release-snapshot` — builds local archives without publishing |
| Snyk    | `make snyk` (`snyk test`, `snyk code test`)                |
| OSV     | `make osv` (OSV-Scanner on `go.mod` and the built server)  |

`make lint` needs [golangci-lint](https://golangci-lint.run/) built with the Go
of `go.mod` or later, which a build with an older Go refuses:
`go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.14.0`,
with `$(go env GOPATH)/bin` on `PATH`.

`make snyk` needs the [Snyk CLI](https://docs.snyk.io/snyk-cli), for example
the [standalone binary](https://docs.snyk.io/snyk-cli/install-or-update-the-snyk-cli),
signed in with `snyk auth` or given a token in
`SNYK_TOKEN`; Snyk Code must be enabled in the Snyk organization.

`make osv` needs [OSV-Scanner](https://google.github.io/osv-scanner/):
`go install github.com/google/osv-scanner/v2/cmd/osv-scanner@v2.6.0`, with
`$(go env GOPATH)/bin` on `PATH`. It scans `go.mod` for the module versions
and the server built by `make build` for the Go standard library it was
built with, which OSV-Scanner does not take from `go.mod`. Every known
vulnerability fails it, whether the code reaches it or not.

GitHub Actions run on pushes to `main` and on pull requests: `make lint` and
`make test` as separate jobs of the CI workflow (`.github/workflows/ci.yml`).
Tests, including the executable smoke test, run on Linux, macOS Apple Silicon
and macOS Intel. Both security scans run
in the Security workflow (`.github/workflows/security.yml`):
the `snyk` job runs `make snyk` with the standalone Snyk CLI, and the
`osv-scanner` job runs the scans of `make osv` with the scanner image of the
official [OSV-Scanner action](https://github.com/google/osv-scanner-action).
Actions are pinned by commit, the image by digest and the Snyk CLI by its
SHA-256, and checkouts keep no credentials. The Snyk job reads the token from the
repository secret `SNYK_TOKEN`.

Pull requests that change `.github/workflows/release.yml`, `.goreleaser.yaml` or
the check itself also run the Release check workflow
(`.github/workflows/release-check.yml`). Without committing or publishing, it
builds snapshot archives with GoReleaser, which runs the release hook, checks
the license notices in them, and raises the version with the pinned bump action.
It first checks that its GoReleaser and bump steps match those of the Release
workflow, and that the hook uses the same go-release-actions commit.

## CLI and Homebrew packaging

`bitbucket-custom-mcp --version` prints `bitbucket-custom-mcp 0.4.3` and exits.
`--help` (or `-h`) prints usage and configuration names. These commands need no
credentials and ignore environment configuration, even malformed policy arrays.
Unknown arguments fail. With no arguments the executable serves MCP over stdio;
only protocol messages go to stdout, with logs on stderr.

The version in `cmd/server/cli.go` is shared by `--version` and MCP `initialize`.
No linker flags or generated files are needed to build a release from a source
archive. The separate Homebrew tap should:

- Pin a versioned source archive and its SHA-256, and declare `license "MIT"`.
- Declare `depends_on "go" => :build`; the compiler must satisfy `go.mod`
  (currently Go 1.27.2 or newer).
- Build `./cmd/server` with Homebrew's `std_go_args` into
  `bin/bitbucket-custom-mcp`. The equivalent direct command is
  `go build -trimpath -o <prefix>/bin/bitbucket-custom-mcp ./cmd/server`.
- Test the installed executable with `--version` and an offline MCP handshake.
  Runtime credentials are supplied by the MCP client, never by the formula.

There is no daemon to register with `brew services`. After installation, use the
absolute path returned by `$(brew --prefix)/bin/bitbucket-custom-mcp` in the MCP
client's command configuration (see [Setup in Claude Code](#setup-in-claude-code)).
Homebrew does not replace the secret manager or the `BITBUCKET_ALLOWED_*` limits.

An offline `test do` for the separate formula can use Ruby's standard library:

```ruby
test do
  require "json"
  require "timeout"

  assert_equal "bitbucket-custom-mcp #{version}", shell_output("#{bin}/bitbucket-custom-mcp --version").strip
  env = {
    "BITBUCKET_WORKSPACE" => "offline-test",
    "BITBUCKET_EMAIL" => "test@example.invalid",
    "BITBUCKET_API_TOKEN" => "not-a-real-token",
    "BITBUCKET_ALLOWED_REPOS" => "[]",
    "BITBUCKET_ALLOWED_PROJECTS" => "[]",
    "BITBUCKET_ALLOWED_TOOLS" => "[]",
    "BITBUCKET_ALLOWED_REFS" => "[]",
  }
  IO.popen(env, ["#{bin}/bitbucket-custom-mcp"], "r+") do |io|
    io.puts JSON.generate(jsonrpc: "2.0", id: 1, method: "initialize", params: {
      protocolVersion: "2025-11-25", capabilities: {},
      clientInfo: { name: "homebrew-test", version: "1" },
    })
    io.flush
    response = Timeout.timeout(15) { JSON.parse(io.readline) }
    assert_equal 1, response.fetch("id")
    assert_equal "bitbucket-custom-mcp", response.fetch("result").fetch("serverInfo").fetch("name")
    assert_equal version.to_s, response.fetch("result").fetch("serverInfo").fetch("version")
    io.close_write
  end
end
```

### Releases

Releases use [GoReleaser](https://goreleaser.com/) v2.18.3 and the configuration
in `.goreleaser.yaml`. Install that version of its CLI to package locally;
GitHub Actions installs the pinned version through the GoReleaser action.
GoReleaser reads the version from the Git tag (`vMAJOR.MINOR.PATCH`). The tag
must match the version committed in `cmd/server/cli.go`, so binaries and installs
built from a source archive report the same version. A pre-release hook checks
this match and gathers the Go and module license notices with the `notices`
command of [go-release-actions](https://github.com/edspc/go-release-actions),
which the workflow also uses to raise the version; both are pinned by commit.

Run the **Release** workflow manually on the branch to release and choose
`bump`, the part of the version to increase: `patch` (the default), `minor` or
`major`. The parts after it reset to zero, so `minor` turns `X.Y.Z` into
`X.(Y+1).0`. The workflow computes the version from the one committed in
`cmd/server/cli.go`, updates the executable version and README examples, tests
the result, commits the version, and atomically pushes the commit and annotated
`v<version>` tag. GoReleaser then builds and publishes the GitHub Release,
including its changelog and assets.
Existing tags or releases are refused. The workflow needs permission to push to
the selected branch; branch protection can reject the atomic push without
creating a tag. If GoReleaser fails after the push, the commit and tag remain:
recover the release from that existing tag instead of dispatching the workflow
again. The workflow does not change the Homebrew tap.

For local release packaging, check out a clean commit tagged with its executable
version, then run `make release`. This validates the release and creates the
archives in `dist/` without publishing. `make release-snapshot` also works from
an untagged or dirty checkout, using a `-SNAPSHOT` version derived from the latest
tag; it skips the source-version check and never publishes. The executable in a
snapshot still reports its committed source version.

Both commands produce:

- `bitbucket-custom-mcp-<version>-source.tar.gz`: committed source with one
  top-level directory, ready for a source-build formula. Snapshot source archives
  also come from the committed tree, while snapshot binaries use the working tree.
- `bitbucket-custom-mcp-<version>-darwin-{arm64,amd64}.tar.gz` and
  `bitbucket-custom-mcp-<version>-linux-{arm64,amd64}.tar.gz`: executables built
  with `CGO_ENABLED=0` and `-trimpath`, accompanied by README, LICENSE and Go/module
  license notices under `THIRD_PARTY_LICENSES/`.
- `checksums.txt`: SHA-256 of each archive; verify with
  `shasum -a 256 -c checksums.txt` from the archive directory.

After publication, the source URL for the separate formula is:

```text
https://github.com/edspc/bitbucket-custom-mcp/releases/download/v0.4.3/bitbucket-custom-mcp-0.4.3-source.tar.gz
```

Use the matching entry in that release's `checksums.txt`. GitHub's automatically
generated tag archive is a different archive with a different checksum; do not
mix the two URLs and checksums. A private repository also requires authenticated
access from Homebrew to its source or release assets.

## Configuration

Configuration comes from environment variables only. The Bitbucket token is a
secret that reaches every repository its account can, so only this server's
process should get it: start the server through a secret manager that
resolves a reference at launch, such as 1Password's `op run` (see
[Setup in Claude Code](#setup-in-claude-code)). Do not export the token in a
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

### Confirmation and access limits

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
[Repository search](#repository-search)). Bitbucket provides no atomic project check
and write: an administrator can still move a repository between those requests.

Keep these settings in the operator's environment or user-scoped MCP config,
outside the model's control. Use a dedicated account/token with only the
Bitbucket permissions needed by that environment.

### Untrusted repository content

PR titles/descriptions, diffs, pipeline logs, variable values and other Bitbucket
content are untrusted data. Read results label external content with
`content_trust: "untrusted"`; neither that content nor tool annotations authorize
a write. For tools requiring browser confirmation, a prompt injection in this data
cannot replace that submission. Tools excluded from confirmation can execute
without user review; configure that list only in the operator environment. Clients must still treat external content as data, and must not
automatically confirm operations based on instructions in it.

The MCP client, browser and local machine are trusted. Do not give an untrusted
model arbitrary local execution or HTTP access to the approval pages: a process
with that access can impersonate a browser submission. This server's approval
barrier does not make a model immune to prompt injection or protect unrelated
tools provided by the client.

For requests containing secured values or a supplied bearer token, API,
transport and decoding errors withhold upstream details that could reflect a
secret. HTTP status, operation and rate-limit advice remain available. This
protects the secret input/error path; pipeline code is still responsible for
not exposing secrets in its own output.

## Tools

### Repository search

`search_repositories` finds repositories of the workspace whose name contains
`query`, optionally only in the project with key `project`, among those the
access limits allow; with no `query` it lists them all. They come by slug, a
page at a time (`limit` a page, 10 by default and at most 50): `next_page` is
passed back as `page`, with the same inputs, for more. Each comes with its slug,
which the `repo` input of the other tools takes, name, project, description,
language, privacy, main branch, last update and page on bitbucket.org.

The request itself asks Bitbucket only for allowed repositories: those listed
in `BITBUCKET_ALLOWED_REPOS`, and, with `BITBUCKET_ALLOWED_PROJECTS`, those in
an allowed project, by key or by UUID for entries in braces. The server then
checks every repository of the answer against the same rule, by the project it
shows, and drops any other. As only the page returned can be checked, `total`,
Bitbucket's count of every page, is left out while access limits are set.
So no project lookups are needed, and a call costs one API request; when the
limits allow nothing, it makes none.

### Repository and deployment UUIDs

`get_uuid` returns the UUID of `repo` in the configured workspace. Supply
`environment` with the exact deployment environment name to get that
environment's UUID instead. The result's `uuid` is copied from Bitbucket
without normalization: braces, hyphens and letter case are preserved, for
example `{"content_trust":"untrusted","uuid":"{01234567-89ab-cdef-0123-456789abcdef}"}`.
An unknown environment or a missing UUID is an error. This read-only tool
respects the repository, project and tool access limits.

### Repository links

Both tools link a source repository A to a target repository B in the
configured workspace. Before changing anything they check that pipelines are
enabled in both repositories, and they report a conflict instead of
overwriting it unless `force` is set.

#### `link_repos_by_key`

Adds the public SSH key of A's pipelines to the Access Keys of B, labelled with
A's slug, so A's pipelines can clone B.

| Input         | Meaning                                              |
|---------------|------------------------------------------------------|
| `source_repo` | A                                                    |
| `target_repo` | B                                                    |
| `force`       | Replace an Access Key labelled with A's slug that holds a different key |

If B already has the same key, nothing changes. A must have a pipelines SSH
key (Repository settings → Pipelines → SSH keys).

#### `link_repos_by_token`

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

Bitbucket has no API to create repository access tokens, so the user creates
the token and pastes it into the [secret page](#secret-page). The page links
to A's access token page and lists the name and the permissions to select. The
server checks only read access to one endpoint in A (the repository, pipelines
or webhooks), chosen from the requested permissions, and writes the token into
the secured variable. Success does not verify the token type, repository-only
scope or actual permissions: a broader token can pass, and a read-only token can
pass even when write permissions were requested. Bitbucket does not provide token
metadata through this probe. Verify the token's repository and permissions in
Bitbucket before submitting; do not paste a workspace, project or user token.
The approval page states this limit, and success reports
`token_validation: "read_access_only"`.

### Pipeline variables

The variable tools work at one of three places, chosen by `scope`:

| `scope`      | Other inputs          | Variables of                          |
|--------------|-----------------------|---------------------------------------|
| `workspace`  | —                     | the workspace, for every repository   |
| `repository` | `repo`                | all pipelines of the repository       |
| `deployment` | `repo`, `environment` | one deployment environment of `repo`  |

- `list_variables` lists names and values; values of secured variables are
  never returned.
- `set_variable` creates a variable (`name`), or overwrites it with
  `force: true`. A plain variable takes its `value` from the call. A secured
  variable (`secured: true`) never does: a `value` in the call is refused, and
  the user enters the value on the [secret page](#secret-page).
- `delete_variable` deletes the variable called `name`.

### Pipelines

#### `run_pipeline`

| Input              | Meaning                                                          |
|--------------------|------------------------------------------------------------------|
| `repo`             | Repository                                                       |
| `branch` / `tag`   | Ref to build; exactly one of them                                |
| `commit`           | Commit to build in the context of the ref; its latest by default. It must be on the ref: a commit of another branch would run its own code as the ref's |
| `custom`           | Custom pipeline (`pipelines.custom.<name>`); by default the pipeline `bitbucket-pipelines.yml` defines for the ref |
| `variables`        | Plain variables of the custom pipeline: `[{"name", "value"}]`    |
| `secret_variables` | Names of secured variables of the custom pipeline; the user enters their values on the [secret page](#secret-page), and they are used for this run only |

It returns the started pipeline with its build number, its page on
bitbucket.org and its `resource_uri`.

#### `list_pipelines`, `get_pipeline`

`list_pipelines` returns the pipelines of `repo`, newest first, a page at a
time (`limit` a page, 10 by default and at most 50). Filters, which can be
combined, keep the pipelines:

| Filter | Keeps pipelines |
|--------|-----------------|
| `branch` | that built this branch |
| `ref_type` | that built a `branch`, a lightweight `tag` or an `annotated_tag` |
| `commit` | that built this commit, by its full 40-character hash |
| `definition` | of a part of `bitbucket-pipelines.yml`, written as results show it: `custom`, `branches`, `tags`, `pull-requests` or `default`, and for all but `default` optionally a name or pattern, such as `custom: deploy` or `branches: main` |
| `trigger` | started by a `push`, `manual`ly (in Bitbucket or through the API) or `scheduled` |

Each pipeline also shows its `trigger`: `push`, `manual`, `scheduled`, or
`parent_step` for a child pipeline a step started. Filter values are checked
before any request, so a mistyped one is named in the error. `page`, 1 by default, picks the page;
`next_page` in the result is passed back as `page`, with the same filters and
`limit`, for older pipelines, and is omitted on the last page. `total` counts
the pipelines the filters keep when Bitbucket reports it.
Pages are counted from the newest pipeline, so a run started between two calls
moves the next page by one and repeats a pipeline. A call costs one API
request. `get_pipeline`
returns one `pipeline`, by build number or UUID (with or without its braces),
with the state of each step.
States read like `PENDING`, `IN_PROGRESS/RUNNING`, `IN_PROGRESS/PAUSED` (waits
for a manual step) or `COMPLETED/FAILED`.

#### `watch_pipeline`

Waits until the pipeline completes, pauses for a manual step or
`timeout_seconds` pass (600 by default, at most 3600), polling every 30
seconds (60 with project restrictions). It returns the outcome (`completed`,
`paused` or `timeout`), every
status change of the pipeline and its steps seen on the way, and the final
status. When the client sends a progress token, each poll is also reported as
a progress notification.

The timeout also cancels API requests and progress notifications. On timeout the
result contains the last complete status; `pipeline` is omitted if the first poll
did not finish. Client cancellation remains an error.

A poll costs two API requests (up to four with project restrictions; two for a
repository listed in `BITBUCKET_ALLOWED_REPOS`), and Bitbucket
allows 1,000 requests an hour to
repository data, so at most two pipelines are polled at a time, by
`watch_pipeline` calls and [subscriptions](#status-subscriptions) together;
beyond that both are refused until one ends.

#### `get_pipeline_log`

Returns the last `tail_lines` lines (200 by default, at most 2000) of the log
of a step: the `step` given by name or UUID, or else the first failed step, or
else the last step that started. Only the end of the log is downloaded; the
result links to the step page with the whole log.

### Deployment environments

`create_deployment` creates a deployment environment in `repo`, with `name` and
`environment_type` (`Test`, `Staging` or `Production`). It returns the environment
UUID and name. Existing names are refused rather than overwritten. This tool
creates the Bitbucket environment used by deployment variables and pipeline
steps; use `run_pipeline` to execute a deployment pipeline.

### Schedules

Bitbucket runs scheduled pipelines on branches only, by a cron pattern in UTC
with 7 fields: seconds, minutes, hours, day of month, month, day of week and
year (`0 0 2 * * ? *` runs at 02:00 UTC every day).

- `list_schedules` lists the schedules of `repo`: UUID, branch, pipeline, cron
  pattern and whether it is enabled.
- `add_schedule` schedules the `pipeline` of `branch` in `repo` by `cron`. The
  pipeline is written the way results show it: `custom: <name>`,
  `branches: <pattern>` with the pattern as in `bitbucket-pipelines.yml`, or
  `default`. An identical schedule is reported as `already_exists` and not
  added twice. The server serializes the duplicate check and creation after
  browser approval, so concurrent calls in this process cannot create
  identical schedules. Separate server processes and changes made directly in
  Bitbucket do not share this lock.
- `delete_schedule` deletes the `schedule` with the given UUID from
  `list_schedules`. The schedule is read again after browser approval, right
  before it is deleted: if it changed meanwhile (branch, pipeline, cron or
  whether it is enabled) or its branch is no longer allowed, nothing is
  deleted and the call has to be made again.

### Pull requests

`list_pull_requests` lists the pull requests of `repo`, the most recently
updated first, a page at a time (`limit` a page, 10 by default and at most
50): open ones, or those in `state` `MERGED`, `DECLINED`, `SUPERSEDED` or
`ALL`; `source_branch` and `destination_branch` keep those from or into a
branch. Each comes with its ID, title, state, draft flag, author, source and
destination branches with their commits and repositories, dates, comment and
open task counts, and its page on bitbucket.org. `page`, 1 by default, picks
the page; `next_page` is passed back as `page`, with the same filters and
`limit`, for the next one, and is omitted on the last page. `total` counts the
pull requests the filters pick when Bitbucket reports it. An update between two
calls reorders the list, so a pull request can repeat or be skipped. A call
costs one API request.

`get_pull_request` describes pull request `pull_request` (its ID) of
`repo`:

- title, description, state (`OPEN`, `MERGED`, `DECLINED` or `SUPERSEDED`),
  draft flag, author, dates, comment and open task counts, how it was closed
  (close source branch, merge commit, closed by, decline reason), and its
  page on bitbucket.org;
- the source branch with its commit and repository (another one for a pull
  request from a fork), and the destination branch it merges into;
- reviewers and other participants who decided, with their state
  (`approved`, `changes_requested`, or `pending` for a reviewer);
- the latest 50 build results reported for it, newest first, such as pipeline runs;
- the paths of the changed files with their status and added and removed
  lines, and how many there are, 200 a page: `files_next` is passed back as
  `files_from` for the next page. Later pages leave out the description,
  reviewers and builds.

Build results are fetched in one bounded API request. `builds_truncated` indicates
that more exist; `builds_total` is included only when Bitbucket reports the count.
File pages are fetched directly from the requested offset, with at most three
API page reads per call, rather than downloading all earlier or later pages.
`files_total` is omitted when Bitbucket does not report it. Each API collection
page is limited to 4 MiB; an oversized response fails the request.

With `paths`, some of the changed files, it returns their diff instead: of
the source branch against the commit where it forked from the destination,
as the pull request page shows it, with the fields above except the
description, reviewers, builds and files. A diff over 40 KB comes in parts,
cut at a line end unless a single line is longer: the result then has
`diff_next`, which is passed back as `diff_from` (with the same `paths`) to
read the next part. Each part downloads the diff up to its end.

A part of a diff or a page of files is refused once the pull request has new
commits, so they never mix two versions; start again without `diff_from` or
`files_from` then.

Bitbucket returns abbreviated commit hashes in PR metadata and redirects. The
server resolves them to full hashes through the commit API before comparing them.
For merged PRs, the redirect may use the merge commit instead of the source head.
A push between the metadata and redirect requests is refused, and every diff
request and file page uses the checked full commit pair. Continuations are bound
to that actual comparison, including its mode, so a merge that changes the diff
also requires starting again.

These sizes keep a result of ordinary code and paths under the 25,000 tokens
Claude Code allows a tool result by default; very long paths or content dense
in tokens may still go over (`MAX_MCP_OUTPUT_TOKENS` raises the limit).

Bitbucket gives up on the file list or the diff of a very large pull request;
the result then says why in `files_error` or `diff_error` and still holds the
rest, and fewer `paths` ask for a smaller diff. A call without `paths` costs
at least four API requests, a later page of files or a part of a diff three.
Abbreviated hashes usually add two commit lookups; each repository/revision pair
is resolved once per call.

### Status subscriptions

Every pipeline is also an MCP resource,
`bitbucket://pipelines/<repo>/<build_number>`, whose content is the status
`get_pipeline` returns. A client that
subscribes to it (`resources/subscribe`, or `subscriptions/listen` from MCP
2026-07-28) gets `notifications/resources/updated` whenever the pipeline or one
of its steps changes state. The server polls a subscribed pipeline every 30
seconds (60 with project restrictions) until it completes or pauses for a manual
step, the last subscriber
leaves, polling fails five times in a row or 24 hours pass. Subscriptions
share the polling limit of `watch_pipeline`; a pipeline that has already
completed or paused is accepted but not polled.

A refusal over the [rate limit](#rate-limit) is not a failure: polling rests
as long as the response says, but at least one poll interval, or else for two
intervals, doubling with every refusal in a row up to 30 intervals, and then goes on. When
polling stops after the failures, subscribers get one more
`notifications/resources/updated`: reading the resource then shows the error
while it lasts, and unsubscribing and subscribing again restarts polling.

#### Branch, pull request and deployment subscriptions

Subscribe to `bitbucket://pipeline-scopes/{repo}/{kind}/{selector}` to follow
new runs and status changes. The selector is percent-encoded as a single path
segment, so a branch `feature/new` becomes `feature%2Fnew`.

| Kind | Selector | Resource contents |
|------|----------|-------------------|
| `branch` | Branch name | Latest 10 pipelines, newest first, without step details |
| `pullrequest` | Positive PR ID | Latest 10 build statuses reported for the PR, including pipeline URLs; `truncated` indicates older results |
| `deployment` | Environment UUID, with or without braces | Deployment records, their result status and release pipeline URLs, filtered from the latest 50 repository deployments; `window_limit` and `truncated` describe that bounded window |

For example, subscribe to `bitbucket://pipeline-scopes/api/branch/main` or
`bitbucket://pipeline-scopes/api/pullrequest/42`. Obtain the environment UUID
from `get_uuid`, `create_deployment` or Bitbucket settings. Each resource needs `get_pipeline`
allowed by `BITBUCKET_ALLOWED_TOOLS` and the same repository/project access as a
pipeline resource. PR statuses may include builds from providers other than
Bitbucket Pipelines. A deployment outside the latest repository window is not
tracked; this keeps each poll to one bounded request.

These subscriptions continue after completion or pause so they can discover
future runs. They poll every 30 seconds (60 with project restrictions), share
the two-slot limit and rate-limit backoff of pipeline subscriptions, and stop
when the last subscriber leaves, after five consecutive failures, or after
24 hours. Resubscribe to start another period. Subscribe to a specific pipeline
resource for step changes.

### Secret page

Secrets — access tokens and values of secured variables — never pass through
the MCP client or the model, and tools refuse them as arguments. For the
duration of the call the server opens a one-off local page on `127.0.0.1`
(random port, unguessable path) and asks the MCP client, through URL-mode
elicitation, to send the user there. The page names the tool and says what is
needed from the user; for an access token it also links to the page where the
token is created. What the user enters goes from the browser straight to this
server, which uses it and does not return it. Spaces around each value are
removed.

The page belongs to the tab that opens it first: the server sends that tab on
to the page under a key only it learns, so opening the link again, in another
tab or browser, shows no form and cannot submit one. Use the tab that opened;
if the page says it was opened elsewhere and that was not you, decline the
request in the MCP client: a local process that read the URL, from the
browser's command line for one, got there first. After the values arrive the
page only shows that they did, so reloading it sends nothing again.

The page stops taking values as soon as they arrive, when the user declines,
when the call ends, or after 10 minutes; a tab left open says so on reload,
and the form reloads by itself when the 10 minutes are up. At most 3 pages
take values at a time. Its port stays bound until a minute after those 10
minutes, even when the page closes sooner, so a tab left open reaches this
server, which refuses the values, and not another local process that took
over a released port. At most 32 listeners, including those retained for closed
pages, can exist at once; reaching that limit refuses new pages until a listener
retires. The MCP client must support URL elicitation (MCP
2025-11-25 or later).

### Rate limit

Bitbucket counts the requests of a user over a rolling hour, 1,000 an hour to
repository data, and refuses more with `429`. Here the user is the account of
`BITBUCKET_EMAIL`, so every other tool that uses it draws on the same
allowance. A tool call that is refused fails without retrying, and its error
says when to try again: after the wait the response gives in `Retry-After`,
or else in Bitbucket's undocumented `X-RateLimit-Reset` (a wait over an hour
is ignored as a misread header), or in a few minutes when it gives none.
[Status subscriptions](#status-subscriptions) rest instead.

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
   in a Claude Code session lists its tools. 1Password asks to approve access
   when the server starts.

Never commit a `.mcp.json` with the token; a project `.mcp.json` may hold only
the `op://` reference.

## Workflow

See [.claude/rules/lifecycle.md](.claude/rules/lifecycle.md).
