# Change lifecycle

1. **Tests with the change.** Every behaviour change or bug fix adds or
   updates a Go test that fails without the change. Pure refactors keep
   existing tests green.
2. **Lint, tests, Snyk and OSV before commit.** Run lint, then tests, then
   the Snyk scan, then the OSV scan, on the whole repository (commands in
   docs/development.md), never single packages or tests. Red blocks the commit;
   never skip, delete or weaken a test or a linter check, and never ignore a Snyk
   or OSV issue, to get green.
3. **Review before commit.** Start a separate review agent on the staged
   diff. It only reviews (correctness, security, rules in this directory) and
   never edits files. It must answer either a list of findings or exactly
   `NO FINDINGS`.
4. **Commit only on `NO FINDINGS`.** Otherwise fix the findings, re-run lint,
   tests, Snyk and OSV and review again. Disagreeing with a finding is
   decided by the user, not by the agent.
