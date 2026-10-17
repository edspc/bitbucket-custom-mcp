# Setup in Claude Code

1. [Install the binary](../README.md), then find its absolute path:

   ```sh
   command -v bitbucket-custom-mcp
   ```

2. Store the Atlassian API token in 1Password and copy its secret reference,
   `op://<vault>/<item>/<field>`. This needs the
   [1Password CLI](https://developer.1password.com/docs/cli/) signed in;
   the desktop app integration lets you approve access with biometrics.

3. Register the server for your user. Only the `op://` reference lands in
   `~/.claude.json`, never the token itself. Replace the binary path below with the
   absolute path from step 1 (`~` is not expanded inside the JSON):

   ```sh
   claude mcp add-json --scope user bitbucket-custom '{
     "type": "stdio",
     "command": "op",
     "args": ["run", "--no-masking", "--", "/absolute/path/to/bitbucket-custom-mcp"],
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
