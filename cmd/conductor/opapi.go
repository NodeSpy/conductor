package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/NodeSpy/conductor/internal/bot"
	"github.com/NodeSpy/conductor/internal/config"
)

// operatorAPISockPath is the operator API's socket (bot.OperatorAPI): HTTP
// for the dashboard, beside the control socket.
func operatorAPISockPath(cfg *config.Config) string {
	return filepath.Join(filepath.Dir(cfg.Store.StateFile), "operator-api.sock")
}

// serveOperatorAPI serves h on a unix socket until ctx ends. Same-user only,
// like the control socket: it takes operator messages and serves every
// attachment, so any local process reaching it is a privilege boundary.
// Fail closed if the mode can't be tightened.
func serveOperatorAPI(ctx context.Context, path string, h http.Handler, log func(string, ...any)) {
	_ = os.Remove(path)
	l, err := net.Listen("unix", path)
	if err != nil {
		log("operator API socket %s: %v", path, err)
		return
	}
	if err := os.Chmod(path, 0o600); err != nil {
		log("operator API socket %s perms: %v", path, err)
		l.Close()
		os.Remove(path)
		return
	}
	srv := &http.Server{Handler: h, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
		os.Remove(path)
	}()
	log("operator API listening at %s", path)
	if err := srv.Serve(l); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log("operator API: %v", err)
	}
}

// routeAdder is a ServeMux or a bot.Routes list.
type routeAdder interface {
	HandleFunc(pattern string, h func(http.ResponseWriter, *http.Request))
}

// operatorRoutes is the bot service's operator API routes, with the avatar
// endpoints (config changes, planned here in cmd) and the wallet reads.
func (m *botStack) operatorRoutes() bot.Routes {
	rs := bot.OperatorRoutes(m.svc)
	avatarRoutes(&rs, m.svc.Cfg, func() string { return botConfigPath })
	browserAPIRoutes(&rs)
	return rs
}

// operatorAPI is operatorRoutes on one handler, for the unix socket.
func (m *botStack) operatorAPI() http.Handler { return m.operatorRoutes().Mux() }
