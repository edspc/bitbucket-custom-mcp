# Security

## Untrusted repository content

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

## Secret page

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
