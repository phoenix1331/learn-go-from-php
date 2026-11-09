// LESSON 23 — DEPLOYMENT: cross-compiling, Docker, running as a service
//
// Run with:  go run ./23-deployment         (from the repo root)
// Test with: go test -v ./23-deployment
//
// The tour builds real binaries for several platforms and inspects them. The
// program is also a tiny service, which is what actually gets deployed:
//
//	go run ./23-deployment -serve
//	curl localhost:8080/version
//
// The files:
//   - service.go               the service: version, health check, $PORT
//   - tour.go                  the guided tour
//   - release.sh               builds every platform into dist/
//   - Dockerfile               a multi-stage image build
//   - Dockerfile.dockerignore  what Docker is allowed to see
//   - deploy/                  systemd and Supervisor config
//
// WHY THIS IS DIFFERENT FROM PHP. Deploying Laravel means the server needs
// the right PHP, extensions, Composer packages, FPM and a web server, all
// kept in step. Deploying Go means copying one file. There's no runtime to
// install, and it doesn't matter what else is on the server.
//
// THREE WAYS TO RUN IT
//
//	systemd     Linux's own service manager. The simplest on a single VPS.
//	Supervisor  What Laravel Forge's daemons and many queue:work setups use.
//	Docker      When everything else on the platform is containers.
//
// COPYING A NEW VERSION ONTO A SERVER
//
//	scp dist/go-service-linux-amd64 server:/usr/local/bin/go-service.new
//	ssh server 'sudo mv /usr/local/bin/go-service.new /usr/local/bin/go-service \
//	            && sudo systemctl restart go-service'
//
// Don't scp straight over the live file: writing to a running binary fails
// with "text file busy". mv swaps the name instead, which is allowed, and
// the old process carries on using the old file until it's restarted.
//
// THE CSV IMPORTER (lesson 22) deploys the same way: build for the server's
// platform and place the file at bin/csvimport during your Laravel deploy.
// A binary built on a Mac won't run on a Linux server.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("go-service", flag.ContinueOnError)
	fs.SetOutput(stderr)
	serveMode := fs.Bool("serve", false, "run the HTTP service")
	healthcheck := fs.Bool("healthcheck", false, "check a running service, exit 0 if healthy")
	showVersion := fs.Bool("version", false, "print the version and exit")
	addrFlag := fs.String("addr", "", "listen address (default: $PORT, then :"+defaultPort+")")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}

	addr := resolveAddr(*addrFlag, os.LookupEnv)
	var err error
	switch {
	case *showVersion:
		b := currentBuild()
		fmt.Fprintf(stdout, "%s (commit %s, %s, %s/%s)\n", b.Version, shortCommit(b.Commit), b.Go, b.OS, b.Arch)
	case *healthcheck:
		err = runHealthcheck(ctx, addr)
	case *serveMode:
		err = runService(ctx, addr)
	default:
		err = tour(ctx)
	}
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return 1
	}
	return 0
}

func shortCommit(c string) string {
	if len(c) > 7 {
		return c[:7]
	}
	return c
}

// GOING FURTHER
//   - PIN IMAGES: replace `golang:1.25-alpine` with an exact digest so a
//     rebuild can't silently change. Dependabot or Renovate can keep it fresh.
//   - ZERO DOWNTIME: `systemctl restart` refuses connections for a moment.
//     Run two copies behind nginx, or use systemd socket activation.
//   - CI RELEASES: goreleaser builds, checksums and publishes everything
//     that release.sh does here.
//   - EMBEDDING FILES: //go:embed bundles templates or SQL migrations into
//     the binary, so it's still a single file to deploy.
//
// THINGS TO TRY
//   - Run ./23-deployment/release.sh and then
//     `go version -m dist/go-service-linux-amd64`.
//   - Build for linux/arm64 and try to run it here: "exec format error".
//   - Change `var version` to a const, and watch the -X stamp in section 3
//     stop working, with no error from the compiler.
//   - Build the image, then `docker run -p 8080:8080 go-service -addr=localhost:8080`
//     and see that curl can no longer connect.
