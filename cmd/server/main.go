package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"bitbucket-custom-mcp/internal/bitbucket"
	"bitbucket-custom-mcp/internal/tools"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if err := run(context.Background(), log); err != nil {
		log.Error("server stopped", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, log *slog.Logger) error {
	cfg, err := configFromEnv()
	if err != nil {
		return err
	}
	bb := bitbucket.New(&http.Client{Timeout: 30 * time.Second}, cfg)
	s := mcp.NewServer(&mcp.Implementation{Name: "bitbucket-custom-mcp", Version: "0.1.0"}, nil)
	tools.Register(s, bb, log)
	log.Info("serving MCP over stdio", "workspace", cfg.Workspace)
	return s.Run(ctx, &mcp.StdioTransport{})
}

func configFromEnv() (bitbucket.Config, error) {
	cfg := bitbucket.Config{
		Workspace: os.Getenv("BITBUCKET_WORKSPACE"),
		Email:     os.Getenv("BITBUCKET_EMAIL"),
		Token:     os.Getenv("BITBUCKET_API_TOKEN"),
	}
	var missing []string
	for _, name := range []string{"BITBUCKET_WORKSPACE", "BITBUCKET_EMAIL", "BITBUCKET_API_TOKEN"} {
		if os.Getenv(name) == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return cfg, fmt.Errorf("missing environment variables: %s", strings.Join(missing, ", "))
	}
	return cfg, nil
}
