// Package server assembles the single HTTP listener: the health endpoints the
// chart probes and the MCP streamable-HTTP endpoint behind the bearer guard —
// nothing else: the server has no inbound path but muster's and no shared
// secret. Without OAuth there is no authentication and no caller: only a
// server nothing but a trusted proxy can reach runs that way, and every tool
// then reports an anonymous caller who nothing acts as.
package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	mcpserver "github.com/mark3labs/mcp-go/server"
)

// Config configures the listener.
type Config struct {
	Addr    string
	MCPPath string
	// OAuth, when set, makes the MCP endpoint require a GitHub user token as
	// the bearer — behind muster the person's — verified with GET /user.
	OAuth *OAuthConfig
}

// Server is the assembled HTTP server.
type Server struct {
	http  *http.Server
	guard *bearerGuard
	log   *slog.Logger
}

// New builds the server around the MCP server.
func New(cfg Config, mcpSrv *mcpserver.MCPServer, log *slog.Logger) (*Server, error) {
	if log == nil {
		log = slog.Default()
	}
	if cfg.MCPPath == "" {
		cfg.MCPPath = "/mcp"
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", ok)
	// The server holds no state and depends on nothing at start: ready is up.
	// GitHub is not tracked; its failures are read from the tools' answers.
	mux.HandleFunc("GET /readyz", ok)

	s := &Server{log: log}
	if cfg.OAuth != nil {
		g, err := newBearerGuard(*cfg.OAuth, cfg.MCPPath, log)
		if err != nil {
			return nil, err
		}
		g.register(mux)
		s.guard = g
	}
	mux.Handle(cfg.MCPPath, s.protect(mcpserver.NewStreamableHTTPServer(mcpSrv, mcpserver.WithEndpointPath(cfg.MCPPath))))

	s.http = &http.Server{
		Addr:              cfg.Addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		// No WriteTimeout: MCP streams outlive any fixed value.
		IdleTimeout: 120 * time.Second,
	}
	return s, nil
}

func ok(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok\n"))
}

// protect requires a verified caller when OAuth is on.
func (s *Server) protect(next http.Handler) http.Handler {
	if s.guard == nil {
		return next
	}
	return s.guard.protect(next)
}

// Handler exposes the mux (tests).
func (s *Server) Handler() http.Handler { return s.http.Handler }

// Run serves until ctx is done, then shuts down gracefully. It returns early
// when the listener cannot serve.
func (s *Server) Run(ctx context.Context) error {
	errCh := make(chan error, 1)
	go func() {
		defer close(errCh)
		s.log.Info("listening", "addr", s.http.Addr)
		if err := s.http.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- fmt.Errorf("serve on %s: %w", s.http.Addr, err)
		}
	}()
	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := s.http.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}
	s.log.Info("stopped")
	return nil
}
