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
| Snyk    | `make snyk` (`snyk test`, `snyk code test`)                |
| OSV     | `make osv` (OSV-Scanner on `go.mod` and the built server)  |

`make snyk` needs the [Snyk CLI](https://docs.snyk.io/snyk-cli), for example
`npm install --global snyk`, signed in with `snyk auth` or given a token in
`SNYK_TOKEN`; Snyk Code must be enabled in the Snyk organization.

`make osv` needs [OSV-Scanner](https://google.github.io/osv-scanner/):
`go install github.com/google/osv-scanner/v2/cmd/osv-scanner@v2.6.0`, with
`$(go env GOPATH)/bin` on `PATH`. It scans `go.mod` for the module versions
and the server built by `make build` for the Go standard library it was
built with, which OSV-Scanner does not take from `go.mod`. Every known
vulnerability fails it, whether the code reaches it or not.

GitHub Actions run both in the Security workflow on pushes to `main` and on
pull requests (`.github/workflows/security.yml`): the `snyk` job runs
`make snyk`, and the `osv-scanner` job runs the scans of `make osv` with the
official [OSV-Scanner action](https://github.com/google/osv-scanner-action).
The Snyk job reads the token from the repository secret `SNYK_TOKEN`.

## Configuration

Configuration comes from environment variables only. The Bitbucket token is a
secret: in the MCP client's `env` reference it as `"${VAR}"`, a 1Password
secret reference (see below) or keep it in a user-scoped config — never a
literal value in a committed file such as `.mcp.json`.

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

## Tools

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
server checks the token by reading something in A that its permissions allow
(the repository, pipelines or webhooks), which catches a token created in
another repository, and writes it into the secured variable.

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
| `commit`           | Commit to build in the context of the ref; its latest by default |
| `custom`           | Custom pipeline (`pipelines.custom.<name>`); by default the pipeline `bitbucket-pipelines.yml` defines for the ref |
| `variables`        | Plain variables of the custom pipeline: `[{"name", "value"}]`    |
| `secret_variables` | Names of secured variables of the custom pipeline; the user enters their values on the [secret page](#secret-page), and they are used for this run only |

It returns the started pipeline with its build number, its page on
bitbucket.org and its `resource_uri`.

#### `list_pipelines`, `get_pipeline`

`list_pipelines` returns the latest pipelines of `repo`, newest first
(`limit`, 10 by default and at most 50; `branch` to filter). `get_pipeline`
returns one `pipeline`, by build number or UUID, with the state of each step.
States read like `PENDING`, `IN_PROGRESS/RUNNING`, `IN_PROGRESS/PAUSED` (waits
for a manual step) or `COMPLETED/FAILED`.

#### `watch_pipeline`

Waits until the pipeline completes, pauses for a manual step or
`timeout_seconds` pass (600 by default, at most 3600), polling every 30
seconds. It returns the outcome (`completed`, `paused` or `timeout`), every
status change of the pipeline and its steps seen on the way, and the final
status. When the client sends a progress token, each poll is also reported as
a progress notification.

A poll costs two API requests, and Bitbucket allows 1,000 requests an hour to
repository data, so at most two pipelines are polled at a time, by
`watch_pipeline` calls and [subscriptions](#status-subscriptions) together;
beyond that both are refused until one ends.

#### `get_pipeline_log`

Returns the last `tail_lines` lines (200 by default, at most 2000) of the log
of a step: the `step` given by name or UUID, or else the first failed step, or
else the last step that started. Only the end of the log is downloaded; the
result links to the step page with the whole log.

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
  added twice.
- `delete_schedule` deletes the `schedule` with the given UUID from
  `list_schedules`.

### Pull requests

`list_pull_requests` lists the pull requests of `repo`, the most recently
updated first (`limit`, 10 by default and at most 50): open ones, or those
in `state` `MERGED`, `DECLINED`, `SUPERSEDED` or `ALL`; `source_branch` and
`destination_branch` keep those from or into a branch. Each comes with its
ID, title, state, draft flag, author, source and destination branches with
their commits and repositories, dates, comment and open task counts, and its
page on bitbucket.org. A call costs one API request.

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
- build results reported for it, such as pipeline runs;
- the paths of the changed files with their status and added and removed
  lines, and how many there are, 200 a page: `files_next` is passed back as
  `files_from` for the next page. Later pages leave out the description,
  reviewers and builds.

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

These sizes keep a result of ordinary code and paths under the 25,000 tokens
Claude Code allows a tool result by default; very long paths or content dense
in tokens may still go over (`MAX_MCP_OUTPUT_TOKENS` raises the limit).

Bitbucket gives up on the file list or the diff of a very large pull request;
the result then says why in `files_error` or `diff_error` and still holds the
rest, and fewer `paths` ask for a smaller diff. A call without `paths` costs
at least four API requests, a later page of files or a part of a diff three.

### Status subscriptions

Every pipeline is also an MCP resource,
`bitbucket://pipelines/<repo>/<build_number>`, whose content is the status
`get_pipeline` returns. A client that
subscribes to it (`resources/subscribe`, or `subscriptions/listen` from MCP
2026-07-28) gets `notifications/resources/updated` whenever the pipeline or one
of its steps changes state. The server polls a subscribed pipeline every 30
seconds until it completes or pauses for a manual step, the last subscriber
leaves, polling fails five times in a row or 24 hours pass. Subscriptions
share the polling limit of `watch_pipeline`; a pipeline that has already
completed or paused is accepted but not polled.

A refusal over the [rate limit](#rate-limit) is not a failure: polling rests
as long as the response says, but at least 30 seconds, or else for a minute,
doubling with every refusal in a row up to 15 minutes, and then goes on. When
polling stops after the failures, subscribers get one more
`notifications/resources/updated`: reading the resource then shows the error
while it lasts, and unsubscribing and subscribing again restarts polling.

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

The page closes as soon as the values arrive, when the user declines, or after
10 minutes; at most 3 pages are open at a time. The MCP client must support URL
elicitation (MCP 2025-11-25 or later).

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
