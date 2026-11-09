package main

import (
	"bytes"
	"context"
	"debug/elf"
	"debug/macho"
	"debug/pe"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// pkg is the full import path, so builds work from any folder in the module.
const pkg = "github.com/phoenix1331/learn-go-from-php/23-deployment"

type target struct{ OS, Arch string }

func (t target) String() string { return t.OS + "/" + t.Arch }

func hostTarget() target { return target{runtime.GOOS, runtime.GOARCH} }

// buildBinary is `GOOS=x GOARCH=y CGO_ENABLED=z go build -o out [flags] pkg`.
func buildBinary(ctx context.Context, out string, t target, cgo string, flags ...string) error {
	args := append([]string{"build", "-o", out}, flags...)
	cmd := exec.CommandContext(ctx, "go", append(args, pkg)...)
	cmd.Env = append(os.Environ(), "GOOS="+t.OS, "GOARCH="+t.Arch, "CGO_ENABLED="+cgo)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("go build for %s: %w\n%s", t, err, output)
	}
	return nil
}

// binaryInfo is what a compiled file says about itself.
type binaryInfo struct {
	Format string
	CPU    string
	Libs   []string // shared libraries needed at startup (ELF only)
}

// inspectBinary reads the executable's header using only the standard
// library. Proof beats trust: this is what the operating system checks too.
func inspectBinary(path string) (binaryInfo, error) {
	if f, err := elf.Open(path); err == nil {
		defer f.Close()
		libs, err := f.ImportedLibraries()
		if err != nil {
			return binaryInfo{}, err
		}
		return binaryInfo{Format: "ELF (Linux)", CPU: f.Machine.String(), Libs: libs}, nil
	}
	if f, err := macho.Open(path); err == nil {
		defer f.Close()
		return binaryInfo{Format: "Mach-O (macOS)", CPU: f.Cpu.String()}, nil
	}
	if f, err := pe.Open(path); err == nil {
		defer f.Close()
		return binaryInfo{Format: "PE (Windows)", CPU: "0x" + strconv.FormatUint(uint64(f.Machine), 16)}, nil
	}
	return binaryInfo{}, errors.New("not a recognised executable")
}

func sizeMB(path string) (string, error) {
	st, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%.1f MB", float64(st.Size())/(1<<20)), nil
}

func tour(ctx context.Context) error {
	dir, err := os.MkdirTemp("", "go-deploy-tour")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)

	// ---------------------------------------------------------------
	// 1. WHAT AM I?
	// ---------------------------------------------------------------
	fmt.Println("1. This program, as built by `go run`")
	fmt.Printf("    %+v\n", currentBuild())
	fmt.Printf("    GOMAXPROCS=%d\n", runtime.GOMAXPROCS(0))
	// Since Go 1.25, GOMAXPROCS respects a container's CPU limit by itself.
	// Memory isn't automatic: in a container with a memory limit, set the
	// GOMEMLIMIT environment variable a little below it.

	// ---------------------------------------------------------------
	// 2. CROSS-COMPILING
	// ---------------------------------------------------------------
	fmt.Println("\n2. One command per platform, from this one machine")
	// Compare PHP, where the server needs the right PHP version, extensions
	// and Composer packages. Here, the build's result is the whole deployment.
	for _, t := range []target{
		{"linux", "amd64"},
		{"linux", "arm64"}, // AWS Graviton, Raspberry Pi
		{"darwin", "arm64"},
		{"windows", "amd64"},
	} {
		out := filepath.Join(dir, "app-"+t.OS+"-"+t.Arch)
		if err := buildBinary(ctx, out, t, "0"); err != nil {
			return err
		}
		info, err := inspectBinary(out)
		if err != nil {
			return err
		}
		size, err := sizeMB(out)
		if err != nil {
			return err
		}
		fmt.Printf("    %-14s %-15s %-14s %s\n", t, info.Format, info.CPU, size)
	}
	// Run the wrong one on a server and you get "exec format error".

	// ---------------------------------------------------------------
	// 3. STAMPING A VERSION
	// ---------------------------------------------------------------
	fmt.Println("\n3. Stamping a version into the binary with -ldflags")
	host := hostTarget()
	stamped := filepath.Join(dir, "stamped")
	if err := buildBinary(ctx, stamped, host, "0", "-ldflags=-X main.version=v1.2.3"); err != nil {
		return err
	}
	output, err := exec.CommandContext(ctx, stamped, "-version").CombinedOutput()
	if err != nil {
		return fmt.Errorf("running -version: %w\n%s", err, output)
	}
	fmt.Println("    " + strings.TrimSpace(string(output)))

	// ---------------------------------------------------------------
	// 4. SMALLER AND CLEANER
	// ---------------------------------------------------------------
	fmt.Println("\n4. Release flags")
	plain := filepath.Join(dir, "plain")
	release := filepath.Join(dir, "release")
	if err := buildBinary(ctx, plain, host, "0"); err != nil {
		return err
	}
	if err := buildBinary(ctx, release, host, "0", "-trimpath", "-ldflags=-s -w"); err != nil {
		return err
	}
	// Your machine's folder names end up inside a normal build.
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	for _, b := range []struct{ label, path string }{
		{"go build                  ", plain},
		{"-trimpath -ldflags=-s -w  ", release},
	} {
		size, err := sizeMB(b.path)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(b.path)
		if err != nil {
			return err
		}
		fmt.Printf("    %s %s, contains your folder path: %v\n", b.label, size, bytes.Contains(data, []byte(cwd)))
	}
	// -s -w removes debug tables (stack traces still work, a debugger won't).
	// -trimpath also makes builds repeatable across different machines.

	// ---------------------------------------------------------------
	// 5. STATIC vs DYNAMIC
	// ---------------------------------------------------------------
	fmt.Println("\n5. Does the binary need anything installed on the server?")
	static := binaryInfo{}
	if static, err = inspectBinary(plain); err != nil {
		return err
	}
	fmt.Printf("    CGO_ENABLED=0  libraries needed: %v\n", static.Libs)
	withC := filepath.Join(dir, "cgo")
	if err := buildBinary(ctx, withC, host, "1"); err != nil {
		fmt.Println("    CGO_ENABLED=1  skipped: no C compiler here")
	} else {
		dynamic, err := inspectBinary(withC)
		if err != nil {
			return err
		}
		fmt.Printf("    CGO_ENABLED=1  libraries needed: %v\n", dynamic.Libs)
	}
	// The second kind fails on a server without a matching C library, often
	// with a baffling "no such file or directory" for a file that's right
	// there. On Alpine, or in an empty image, that's the usual cause.
	// CGO_ENABLED=0 is why lesson 17 uses a pure-Go SQLite driver.

	// ---------------------------------------------------------------
	// 6. LIVE LIKE A SERVICE
	// ---------------------------------------------------------------
	fmt.Println("\n6. What Docker or systemd does with it")
	return liveService(ctx, stamped)
}

// liveService runs a built binary the way a platform would: configure it
// through the environment, health-check it, read it, then send SIGTERM.
func liveService(ctx context.Context, binary string) error {
	port, err := freePort()
	if err != nil {
		return err
	}
	env := append(os.Environ(), "PORT="+port)

	cmd := exec.CommandContext(ctx, binary, "-serve")
	cmd.Env = env
	var logs bytes.Buffer
	cmd.Stdout = &logs
	if err := cmd.Start(); err != nil {
		return err
	}
	defer cmd.Process.Kill()

	check := func() error {
		c := exec.CommandContext(ctx, binary, "-healthcheck")
		c.Env = env
		return c.Run()
	}
	var healthErr error
	for range 100 {
		if healthErr = check(); healthErr == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if healthErr != nil {
		return fmt.Errorf("service never became healthy: %w\n%s", healthErr, logs.String())
	}
	fmt.Println("    health check: exit 0 (healthy)")

	resp, err := http.Get("http://127.0.0.1:" + port + "/version")
	if err != nil {
		return err
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		return err
	}
	fmt.Println("    GET /version:", strings.TrimSpace(string(body)))

	fmt.Println("    sending SIGTERM, as `docker stop` does")
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		return err
	}
	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("service exited badly: %w\n%s", err, logs.String())
	}
	fmt.Println("    exit 0. Service logs:")
	for line := range strings.Lines(logs.String()) {
		fmt.Print("      ", line)
	}
	return nil
}

func freePort() (string, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	defer ln.Close()
	_, port, err := net.SplitHostPort(ln.Addr().String())
	return port, err
}
