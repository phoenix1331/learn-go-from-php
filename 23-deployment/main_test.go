package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestResolveAddr(t *testing.T) {
	env := func(vars map[string]string) func(string) (string, bool) {
		return func(k string) (string, bool) {
			v, ok := vars[k]
			return v, ok
		}
	}
	tests := []struct {
		name string
		flag string
		env  map[string]string
		want string
	}{
		{"default binds all interfaces", "", nil, ":8080"},
		{"PORT", "", map[string]string{"PORT": "9000"}, ":9000"},
		{"empty PORT", "", map[string]string{"PORT": ""}, ":8080"},
		{"flag wins", "127.0.0.1:7000", map[string]string{"PORT": "9000"}, "127.0.0.1:7000"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := resolveAddr(tt.flag, env(tt.env)); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestHealthURL(t *testing.T) {
	tests := map[string]string{
		":8080":          "http://127.0.0.1:8080/health",
		"0.0.0.0:8080":   "http://127.0.0.1:8080/health",
		"[::]:8080":      "http://127.0.0.1:8080/health",
		"localhost:9000": "http://localhost:9000/health",
	}
	for addr, want := range tests {
		got, err := healthURL(addr)
		if err != nil || got != want {
			t.Errorf("healthURL(%q) = %q, %v; want %q", addr, got, err, want)
		}
	}
	if _, err := healthURL("no-port"); err == nil {
		t.Error("expected an error for an address with no port")
	}
}

func TestVersionEndpoint(t *testing.T) {
	want := BuildInfo{Version: "v9", Commit: "abc", Go: "go1", OS: "linux", Arch: "arm64"}
	rec := httptest.NewRecorder()
	newMux(want).ServeHTTP(rec, httptest.NewRequest("GET", "/version", nil))

	var got BuildInfo
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestRunHealthcheck(t *testing.T) {
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
	}))
	defer srv.Close()
	addr := strings.TrimPrefix(srv.URL, "http://")

	if err := runHealthcheck(t.Context(), addr); err != nil {
		t.Errorf("healthy service: %v", err)
	}
	status = http.StatusServiceUnavailable
	if err := runHealthcheck(t.Context(), addr); err == nil {
		t.Error("expected an error for a 503")
	}
}

func TestHealthcheckNothingListening(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	if err := runHealthcheck(t.Context(), addr); err == nil {
		t.Error("expected an error")
	}
}

func TestServeStopsWhenCancelled(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- serve(ctx, ln, slog.New(slog.DiscardHandler), time.Second)
	}()

	cancel()
	if err := <-done; err != nil {
		t.Errorf("serve: %v", err)
	}
}

func TestRunRejectsUnknownFlag(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run(t.Context(), []string{"-nope"}, &stdout, &stderr); code != 2 {
		t.Errorf("exit %d, want 2", code)
	}
}

// The tests below run the real toolchain, so -short skips them.

func TestCrossCompiledHeaders(t *testing.T) {
	if testing.Short() {
		t.Skip("compiles binaries")
	}
	t.Parallel()
	tests := []struct {
		target target
		format string
		cpu    string
	}{
		{target{"linux", "amd64"}, "ELF (Linux)", "EM_X86_64"},
		{target{"linux", "arm64"}, "ELF (Linux)", "EM_AARCH64"},
		{target{"darwin", "arm64"}, "Mach-O (macOS)", "CpuArm64"},
		{target{"windows", "amd64"}, "PE (Windows)", "0x8664"},
	}
	for _, tt := range tests {
		t.Run(tt.target.String(), func(t *testing.T) {
			t.Parallel()
			out := filepath.Join(t.TempDir(), "app")
			if err := buildBinary(t.Context(), out, tt.target, "0"); err != nil {
				t.Fatal(err)
			}
			got, err := inspectBinary(out)
			if err != nil {
				t.Fatal(err)
			}
			if got.Format != tt.format || got.CPU != tt.cpu {
				t.Errorf("got %+v, want %s %s", got, tt.format, tt.cpu)
			}
			// CGO_ENABLED=0 must need nothing installed on the server.
			if len(got.Libs) != 0 {
				t.Errorf("needs shared libraries: %v", got.Libs)
			}
		})
	}
}

func TestInspectRejectsOtherFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "notes.txt")
	if err := os.WriteFile(path, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := inspectBinary(path); err == nil {
		t.Error("expected an error")
	}
}

// The exact flags from the Dockerfile and release.sh, then the whole
// lifecycle that a platform puts a service through.
func TestReleaseBuildLifecycle(t *testing.T) {
	if testing.Short() {
		t.Skip("compiles a binary")
	}
	if runtime.GOOS == "windows" {
		t.Skip("needs Unix signals")
	}
	t.Parallel()

	bin := filepath.Join(t.TempDir(), "app")
	err := buildBinary(t.Context(), bin, hostTarget(), "0", "-trimpath", "-ldflags=-s -w -X main.version=v1.2.3")
	if err != nil {
		t.Fatal(err)
	}

	out, err := exec.CommandContext(t.Context(), bin, "-version").CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if !strings.HasPrefix(string(out), "v1.2.3 ") {
		t.Errorf("-version printed %q", out)
	}

	// liveService does the health check, a request, SIGTERM, and checks exit 0.
	if err := liveService(t.Context(), bin); err != nil {
		t.Error(err)
	}
}

// Keeps the Dockerfile in step with the code, as these tests do not build the image.
func TestDockerfileUsesExecFormAndHealthcheck(t *testing.T) {
	raw, err := os.ReadFile("Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`ENTRYPOINT ["/go-service", "-serve"]`,
		`HEALTHCHECK CMD ["/go-service", "-healthcheck"]`,
		"CGO_ENABLED=0",
		"-X main.version=",
	} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("Dockerfile is missing %s", want)
		}
	}
}
