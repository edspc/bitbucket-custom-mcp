# Tools

## Repository search

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

## Repository and deployment UUIDs

`get_uuid` returns the UUID of `repo` in the configured workspace. Supply
`environment` with the exact deployment environment name to get that
environment's UUID instead. The result's `uuid` is copied from Bitbucket
without normalization: braces, hyphens and letter case are preserved, for
example `{"content_trust":"untrusted","uuid":"{01234567-89ab-cdef-0123-456789abcdef}"}`.
An unknown environment or a missing UUID is an error. This read-only tool
respects the repository, project and tool access limits.

## Repository links

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

Bitbucket has no API to create repository access tokens, so the user creates
the token and pastes it into the [secret page](security.md#secret-page). The page links
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

## Pipeline variables

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
  the user enters the value on the [secret page](security.md#secret-page).
- `delete_variable` deletes the variable called `name`.

## Pipelines

### `run_pipeline`

| Input              | Meaning                                                          |
|--------------------|------------------------------------------------------------------|
| `repo`             | Repository                                                       |
| `branch` / `tag`   | Ref to build; exactly one of them                                |
| `commit`           | Commit to build in the context of the ref; its latest by default. It must be on the ref: a commit of another branch would run its own code as the ref's |
| `custom`           | Custom pipeline (`pipelines.custom.<name>`); by default the pipeline `bitbucket-pipelines.yml` defines for the ref |
| `variables`        | Plain variables of the custom pipeline: `[{"name", "value"}]`    |
| `secret_variables` | Names of secured variables of the custom pipeline; the user enters their values on the [secret page](security.md#secret-page), and they are used for this run only |

It returns the started pipeline with its build number, its page on
bitbucket.org and its `resource_uri`.

### `list_pipelines`, `get_pipeline`

`list_pipelines` returns the pipelines of `repo`, newest first, a page at a
time (`limit` a page, 10 by default and at most 50). Filters, which can be
combined, keep the pipelines:

| Filter | Keeps pipelines |
|--------|-----------------|
| `branch` | that built this branch |
| `ref_type` | that built a `branch`, a lightweight `tag` or an `annotated_tag` |
| `commit` | that built this commit, by its full 40-character hash |
| `definition` | of a part of `bitbucket-pipelines.yml`, written as results show it: `custom`, `branches`, `tags`, `pull-requests` or `default`, and for all but `default` optionally a name or pattern, such as `custom: deploy` or `branches: main` |
| `trigger` | started by a `push`, `manual`ly (in Bitbucket or through the API), `scheduled`, or by a `parent_step` (child pipelines) |

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

### `watch_pipeline`

Waits until the pipeline completes, pauses for a manual step or
`timeout_seconds` pass (600 by default, at most 3600), polling every 30
seconds (60 with project restrictions). It returns the outcome (`completed`,
`paused` or `timeout`), every
status change of the pipeline and its steps seen on the way, and the final
status. When the client sends a progress token, each poll is also reported as
a progress notification.

With `stream_logs: true`, the same progress channel also delivers new log lines
from every started step, including parallel steps. Set `step` to a name or UUID
to follow only that step. This mode requires the client to send a progress token;
otherwise the call is refused before contacting Bitbucket. For example, the MCP
`tools/call` parameters can be:

```json
{
  "name": "watch_pipeline",
  "arguments": {"repo": "my-repo", "pipeline": "42", "stream_logs": true},
  "_meta": {"progressToken": "pipeline-42"}
}
```

Status messages keep their text format. Log messages are JSON objects containing
`content_trust: "untrusted"`, `build_number`, `step`, `log` and the step's `url`.
External log text is data, never instructions or authorization. Byte offsets avoid
repeating output; incomplete lines are buffered until their newline or the end of
the watch, and terminal escapes are removed. Completed steps are read once more
and then stop consuming log requests. A running step whose log is not available
yet is retried; a missing log when the step or watch ends is reported with
`unavailable: true`.

Each read keeps the latest 64 KiB of raw log, and each message carries at most
32 KiB of log text as JSON. If output grows beyond those bounds between polls,
or a shorter log indicates a reset, `truncated: true` marks the gap. The result
also includes recent chunks in `logs`, bounded to 256 KiB together, with `logs_truncated: true` when
output was omitted. Use `get_pipeline_log` or the step URL for more. An incomplete
line remaining at timeout is included in the result even though the expired
progress channel can no longer deliver it.

The timeout also cancels API requests and progress notifications. On timeout the
result contains the last complete status; `pipeline` is omitted if the first poll
did not finish. Client cancellation remains an error.

A poll costs two API requests (up to four with project restrictions; two for a
repository listed in `BITBUCKET_ALLOWED_REPOS`), and Bitbucket
allows 1,000 requests an hour to
repository data, so at most two pipelines are polled at a time, by
`watch_pipeline` calls and [subscriptions](subscriptions.md) together;
beyond that both are refused until one ends.

Streaming adds one log API request per unfinished tracked step on each poll.
The wait after that poll is multiplied by `(2 + log requests) / 2` to keep the
same request budget: one log read gives 45 seconds, two give 60 seconds, before
the doubling for project restrictions. Status progress heartbeats continue at the
original interval while waiting, without additional API requests. At most 100
started steps are tracked; the timeout still bounds the entire call.

### `get_pipeline_log`

Returns the last `tail_lines` lines (200 by default, at most 2000) of the log
of a step: the `step` given by name or UUID, or else the first failed step, or
else the last step that started. Only the end of the log is downloaded; the
result links to the step page with the whole log.

## Deployment environments

`create_deployment` creates a deployment environment in `repo`, with `name` and
`environment_type` (`Test`, `Staging` or `Production`). It returns the environment
UUID and name. Existing names are refused rather than overwritten. This tool
creates the Bitbucket environment used by deployment variables and pipeline
steps; use `run_pipeline` to execute a deployment pipeline.

## Schedules

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

## Pull requests

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
