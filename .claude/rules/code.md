---
paths:
  - "**/*.go"
---

# Code

- Simplest working solution first: stdlib before a library, existing helpers
  before new ones, no interfaces or abstractions with a single user, no code
  "for later".
- Reuse a single `err` variable — no `writeErr`, `scanErr`. Keep
  `if err := ...; err != nil` as is.
- No global variables. Pass dependencies (logger, HTTP client, config) via
  struct fields or function parameters.
- Log with `log/slog` to stderr. Never write to stdout: it carries the MCP
  protocol. No `log`, `fmt.Println`, `fmt.Printf` for operational output.
- Call the Bitbucket API through one shared client package; tool handlers
  never build Bitbucket URLs or auth headers themselves.
- Security:
  - read the token only from the environment;
  - never log tokens, `Authorization` headers or full API responses;
  - never put the token into URLs or tool results.
- Return every API error to the MCP client with a clear message; do not
  swallow errors.
- Split a package into files by responsibility; no large files.
- Comments explain why, not what. No commented-out code.
