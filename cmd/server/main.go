// Command server is a read-only MCP server for Jenkins that runs with the
// user's own credentials (session cookie or API token).
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	authjenkins "github.com/vinaycharlie01/jenkins-mcp-go/internal/auth/jenkins"
	clientjenkins "github.com/vinaycharlie01/jenkins-mcp-go/internal/clients/jenkins"
	"github.com/vinaycharlie01/jenkins-mcp-go/internal/config"
	"github.com/vinaycharlie01/jenkins-mcp-go/internal/logging"
	"github.com/vinaycharlie01/jenkins-mcp-go/internal/mcp"
	toolsjenkins "github.com/vinaycharlie01/jenkins-mcp-go/internal/tools/jenkins"
	"github.com/vinaycharlie01/jenkins-mcp-go/internal/transport"
	"github.com/vinaycharlie01/jenkins-mcp-go/pkg/version"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "jenkins-mcp:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("configuration:\n%w", err)
	}
	// stderr only: stdout is the MCP JSON-RPC stream.
	log := logging.New(os.Stderr, cfg.Logging.Level, cfg.Logging.Format)
	slog.SetDefault(log)

	j := cfg.Jenkins
	var auth authjenkins.Authenticator
	switch j.AuthMode {
	case config.JenkinsAuthAPIToken:
		auth = authjenkins.APIToken{User: j.User, Token: authjenkins.NewSecret(j.APIToken, j.TokenFile)}
	default:
		auth = authjenkins.Cookie{Name: j.CookieName, Value: authjenkins.NewSecret(j.Session, j.SessionFile)}
	}
	c, err := clientjenkins.New(j.BaseURL, auth, clientjenkins.Options{
		BaseFolder: j.BaseFolder, CAFile: j.CAFile, InsecureSkipVerify: j.InsecureSkipVerify, Timeout: j.Timeout,
	})
	if err != nil {
		return fmt.Errorf("jenkins client: %w", err)
	}
	log.Info("jenkins integration enabled", "url", j.BaseURL, "auth_mode", j.AuthMode,
		"base_folder", c.BaseFolder(), "auth_input", authInput(j), "timeout", j.Timeout.String())
	if j.InsecureSkipVerify {
		log.Warn("TLS verification disabled for Jenkins (JENKINS_INSECURE=true)")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	log.Info("starting MCP server", "version", version.Version, "transport", "stdio")

	return transport.RunStdio(ctx, mcp.NewServer(toolsjenkins.New(c)))
}

func authInput(j config.Jenkins) string {
	if j.SessionFile != "" || j.TokenFile != "" {
		return "file"
	}

	return "env"
}
