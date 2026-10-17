# Status subscriptions

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

A refusal over the [rate limit](subscriptions.md#rate-limit) is not a failure: polling rests
as long as the response says, but at least one poll interval, or else for two
intervals, doubling with every refusal in a row up to 30 intervals, and then goes on. When
polling stops after the failures, subscribers get one more
`notifications/resources/updated`: reading the resource then shows the error
while it lasts, and unsubscribing and subscribing again restarts polling.

## Branch, pull request and deployment subscriptions

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

## Rate limit

Bitbucket counts the requests of a user over a rolling hour, 1,000 an hour to
repository data, and refuses more with `429`. Here the user is the account of
`BITBUCKET_EMAIL`, so every other tool that uses it draws on the same
allowance. A tool call that is refused fails without retrying, and its error
says when to try again: after the wait the response gives in `Retry-After`,
or else in Bitbucket's undocumented `X-RateLimit-Reset` (a wait over an hour
is ignored as a misread header), or in a few minutes when it gives none.
[Status subscriptions](subscriptions.md) rest instead.
