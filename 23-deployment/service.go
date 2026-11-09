package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"runtime"
	"runtime/debug"
	"time"
)

// version is replaced at build time:
//
//	go build -ldflags="-X main.version=v1.2.3" ./23-deployment
//
// It has to be a package-level string VARIABLE. -X can't change a const or
// a value computed by a function call.
var version = "dev"

const (
	defaultPort = "8080"
	// Must fit inside what the platform allows after SIGTERM before SIGKILL
	// (lesson 19). docker stop gives 10 seconds.
	graceful = 8 * time.Second
)

// BuildInfo says exactly what's running, which is the first question in any
// incident. It's Laravel's APP_VERSION, but it can't get out of step with
// the code, because it's baked into the binary.
type BuildInfo struct {
	Version  string `json:"version"`
	Commit   string `json:"commit"`
	Modified bool   `json:"modified"` // built with uncommitted changes?
	Go       string `json:"go"`
	OS       string `json:"os"`
	Arch     string `json:"arch"`
}

// currentBuild combines the -X version with what the Go toolchain records
// automatically. `go build` inside a git repo stamps the commit, and you can
// read the same data from any binary with: go version -m ./binary
func currentBuild() BuildInfo {
	b := BuildInfo{
		Version: version,
		Commit:  "unknown", // e.g. `go run`, or a Docker build without .git
		Go:      runtime.Version(),
		OS:      runtime.GOOS,
		Arch:    runtime.GOARCH,
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return b
	}
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			b.Commit = s.Value
		case "vcs.modified":
			b.Modified = s.Value == "true"
		}
	}
	return b
}

// resolveAddr picks the listen address: -addr, then $PORT, then the default.
// $PORT is what most container platforms set.
//
// The default listens on ALL interfaces. "localhost:8080" is the classic
// container mistake: inside a container, localhost means only the container,
// so nothing outside can connect, even with the port published.
func resolveAddr(flagValue string, lookup func(string) (string, bool)) string {
	if flagValue != "" {
		return flagValue
	}
	if port, ok := lookup("PORT"); ok && port != "" {
		return ":" + port
	}
	return ":" + defaultPort
}

// healthURL turns a listen address into one a client on this machine can use.
func healthURL(addr string) (string, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "", fmt.Errorf("bad address %q: %w", addr, err)
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port) + "/health", nil
}

// runHealthcheck is the -healthcheck flag. Minimal images contain no curl,
// so the binary checks itself. See HEALTHCHECK in the Dockerfile.
func runHealthcheck(ctx context.Context, addr string) error {
	target, err := healthURL(addr)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("health check returned %d", resp.StatusCode)
	}
	return nil
}

func newMux(build BuildInfo) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "ok")
	})
	mux.HandleFunc("GET /version", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(build)
	})
	return mux
}

// serve runs until ctx is cancelled, then shuts down gracefully (lesson 19).
func serve(ctx context.Context, ln net.Listener, logger *slog.Logger, grace time.Duration) error {
	build := currentBuild()
	srv := &http.Server{Handler: newMux(build), ReadHeaderTimeout: 5 * time.Second}

	failed := make(chan error, 1)
	go func() { failed <- srv.Serve(ln) }()
	logger.Info("listening", "addr", ln.Addr().String(), "version", build.Version, "commit", build.Commit)

	select {
	case err := <-failed:
		return err
	case <-ctx.Done():
	}

	logger.Info("shutting down")
	graceCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), grace)
	defer cancel()
	if err := srv.Shutdown(graceCtx); err != nil {
		return errors.Join(err, srv.Close())
	}
	logger.Info("stopped")
	return nil
}

func runService(ctx context.Context, addr string) error {
	// JSON on stdout: Docker and systemd's journal collect it (lesson 18).
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	return serve(ctx, ln, logger, graceful)
}
