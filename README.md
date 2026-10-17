# bitbucket-custom-mcp

Custom [MCP](https://modelcontextprotocol.io) server for Bitbucket Cloud,
written in Go. It speaks MCP over stdio and calls the Bitbucket REST API on
behalf of the user.

Licensed under the [MIT License](LICENSE).

## Install with Homebrew

```sh
brew install edspc/extended/bitbucket-custom-mcp
```

To update an existing installation:

```sh
brew update
brew upgrade edspc/extended/bitbucket-custom-mcp
```

## Download the latest binary

Download the latest binary for macOS or Linux, on ARM64 or x86-64:

```sh
curl -fL -o bitbucket-custom-mcp "https://github.com/edspc/bitbucket-custom-mcp/releases/latest/download/bitbucket-custom-mcp-$(uname -s | tr '[:upper:]' '[:lower:]')-$(uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/')"
```

Install the downloaded binary:

```sh
mkdir -p "$HOME/.local/bin"
install -m 0755 bitbucket-custom-mcp "$HOME/.local/bin/bitbucket-custom-mcp"
```

Add `~/.local/bin` to your `PATH` if it is not already there, or use the full path
to the executable.

Verify either installation method with:

```sh
bitbucket-custom-mcp --version
```

## Documentation

- [CLI](docs/cli.md)
- [Configuration and access limits](docs/configuration.md)
- [Setup in Claude Code](docs/claude-code.md)
- [Tools](docs/tools.md)
- [Status subscriptions and rate limits](docs/subscriptions.md)
- [Security and secret entry](docs/security.md)
- [Development and checks](docs/development.md)
- [Releases](docs/releases.md)
