package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/controller"
	"github.com/NodeSpy/conductor/internal/paseoplugin"
)

// paseoProviderSocket is the paseo provider relay's socket: the configured
// one, else <state>/paseo-provider.sock.
func paseoProviderSocket(cfg *config.Config) string {
	if cfg.PaseoProvider != nil && cfg.PaseoProvider.Socket != "" {
		return config.ExpandHome(cfg.PaseoProvider.Socket)
	}
	return filepath.Join(paseoStateDir(cfg), "paseo-provider.sock")
}

func paseoStateDir(cfg *config.Config) string {
	if cfg.Store.StateFile != "" {
		return filepath.Dir(cfg.Store.StateFile)
	}
	return config.StateDir()
}

// startPaseoProvider serves paseo's `conductor` provider (paseo_provider:)
// until ctx ends. A failure only loses the provider — paseo shows it
// unavailable — so it is logged, never fatal to the daemon.
func startPaseoProvider(ctx context.Context, cfg *config.Config, audit func(map[string]any)) {
	p := cfg.PaseoProvider
	if p == nil {
		return
	}
	cc, ok := cfg.MergedControllers()[p.Runtime]
	if !ok {
		logf("paseo provider: runtime %q is not configured — provider not served", p.Runtime)
		return
	}
	sock := paseoProviderSocket(cfg)
	if err := os.MkdirAll(filepath.Dir(sock), 0o700); err != nil {
		logf("paseo provider: %v — provider not served", err)
		return
	}
	_ = os.Remove(sock) // a stale socket from a previous daemon
	l, err := net.Listen("unix", sock)
	if err != nil {
		logf("paseo provider: listen %s: %v — provider not served", sock, err)
		return
	}
	_ = os.Chmod(sock, 0o600)
	relay := controller.NewPaseoRelay(cc, p, filepath.Join(paseoStateDir(cfg), "paseo-provider"), logf, audit)
	logf("paseo provider: serving on %s (runtime %s) — `conductor paseo install-plugin` makes paseo use it", sock, p.Runtime)
	go func() {
		if err := relay.Serve(ctx, l); err != nil {
			logf("paseo provider: %v", err)
		}
	}()
}

// cmdPaseo is `conductor paseo …`.
func cmdPaseo(args []string) error {
	if len(args) == 0 || args[0] != "install-plugin" {
		return errors.New("usage: conductor paseo install-plugin [--config FILE] [--paseo-home DIR] [--paseo-bin PATH]")
	}
	fs := flag.NewFlagSet("paseo install-plugin", flag.ContinueOnError)
	cfgFlag := fs.String("config", "", "config file")
	home := fs.String("paseo-home", "", "the paseo daemon home to install into (paseo's --home; default its own)")
	bin := fs.String("paseo-bin", "", "the paseo CLI (default: the paseo runtime's bin:, else paseo on PATH)")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	cfgArgs := []string{}
	if *cfgFlag != "" {
		cfgArgs = []string{"--config", *cfgFlag}
	}
	cfgFile, _ := configPath(cfgArgs)
	cfg, err := config.Load(cfgFile)
	if err != nil {
		return err
	}
	if cfg.PaseoProvider == nil {
		return fmt.Errorf("%s has no paseo_provider: block — add one naming an acp runtime first (see docs/wiki/Paseo-Provider.md)", cfgFile)
	}
	pbin := *bin
	if pbin == "" {
		for _, rt := range cfg.Runtimes {
			if u, err := rt.Resolved(); err == nil && u.IsBuiltin() && u.Name == "paseo" && rt.Bin != "" {
				pbin = config.ExpandHome(rt.Bin)
				break
			}
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	res, err := paseoplugin.Install(ctx, paseoplugin.Options{
		Dir:       filepath.Join(paseoStateDir(cfg), "paseo-plugin"),
		Socket:    paseoProviderSocket(cfg),
		PaseoBin:  pbin,
		PaseoHome: *home,
	})
	if err != nil {
		return err
	}
	fmt.Printf("paseo plugin %q %s (version %s, %s)\n", paseoplugin.ID, res.Action, res.Version, res.Dir)
	return nil
}
