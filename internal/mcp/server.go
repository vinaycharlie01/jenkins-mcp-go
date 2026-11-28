// Package mcp builds the MCP server from independent tool sets.
package mcp

import (
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/vinaycharlie01/jenkins-mcp-go/pkg/version"
)

// Registrar is implemented by each integration's tool set.
type Registrar interface {
	Register(s *mcp.Server)
}

// NewServer creates the MCP server and registers every given tool set.
func NewServer(sets ...Registrar) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{
		Name:    "jenkins-mcp-go",
		Title:   "Jenkins MCP",
		Version: version.Version,
	}, &mcp.ServerOptions{
		Instructions: "Read-only tools for Argo CD and Jenkins. Calls run with the configured user's own credentials, " +
			"so results are limited by that user's permissions. Errors are JSON: {\"error\":{\"code\",\"message\"}}.",
	})
	for _, set := range sets {
		set.Register(s)
	}

	return s
}
