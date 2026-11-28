// Package transport runs the MCP server over a transport. Only stdio is
// implemented; an HTTP transport can be added here without touching tools.
package transport

import (
	"context"
	"errors"
	"io"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// RunStdio serves s over stdin/stdout until ctx is cancelled or stdin closes.
func RunStdio(ctx context.Context, s *mcp.Server) error {
	err := s.Run(ctx, &mcp.StdioTransport{})
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, io.EOF) {
		return nil
	}

	return err
}
