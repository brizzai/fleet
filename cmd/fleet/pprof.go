package main

import (
	"fmt"
	"net"
	"net/http"
	"net/http/pprof"
	"os"

	"github.com/brizzai/fleet/internal/debuglog"
)

// pprofEnvVar opts a TUI into serving net/http/pprof, for profiling fleet on a
// real fleet of sessions rather than a fixture:
//
//	FLEET_PPROF=127.0.0.1:6061 fleet
//	go tool pprof -top http://127.0.0.1:6061/debug/pprof/profile?seconds=60
const pprofEnvVar = "FLEET_PPROF"

// startPprof serves pprof on addr when it is a loopback address. A profile
// exposes heap contents (prompts, pane text, tokens in memory), so anything
// reachable off the machine is refused rather than served. Empty addr is the
// normal case and does nothing.
func startPprof(addr string) error {
	if addr == "" {
		return nil
	}
	if err := checkLoopback(addr); err != nil {
		return err
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("%s: listen %s: %w", pprofEnvVar, addr, err)
	}
	// Its own mux and listener, so nothing about this depends on what else in
	// the binary uses http.DefaultServeMux. (The package's init registers on
	// DefaultServeMux too, but fleet never serves that mux.)
	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	go func() {
		if err := http.Serve(ln, mux); err != nil {
			debuglog.Logger.Warn("pprof server stopped", "addr", addr, "err", err)
		}
	}()
	debuglog.Logger.Info("pprof serving", "addr", ln.Addr().String())
	return nil
}

func checkLoopback(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("%s=%q: want host:port, e.g. 127.0.0.1:6061: %w", pprofEnvVar, addr, err)
	}
	if host == "localhost" {
		return nil
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return nil
	}
	return fmt.Errorf("%s=%q: only a loopback address is allowed (127.0.0.1, ::1, localhost) — a profile exposes process memory", pprofEnvVar, addr)
}

// startPprofFromEnv is the runTUI hook: a bad value is reported and ignored
// rather than fatal, since profiling is never why someone launched fleet.
func startPprofFromEnv() {
	if err := startPprof(os.Getenv(pprofEnvVar)); err != nil {
		fmt.Fprintln(os.Stderr, "fleet:", err)
		debuglog.Logger.Warn("pprof not started", "err", err)
	}
}
