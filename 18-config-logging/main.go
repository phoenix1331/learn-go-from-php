// LESSON 18 — CONFIG & LOGGING (part 1)
//
// Run with:  go run ./18-config-logging
// Test with: go test -v ./18-config-logging
//
// Then try changing the config, and watch sections 5–8 change with it:
//
//	LOG_FORMAT=json go run ./18-config-logging
//	go run ./18-config-logging -log-level=debug
//	PORT=eighty WORKERS=0 go run ./18-config-logging
//	go run ./18-config-logging -h
//
// The files:
//   - config.go   Config, Load (defaults -> env -> flags) and the Secret type
//   - logging.go  NewLogger and a handler that adds request IDs
//   - main.go     this tour
//
// Logs go to os.Stdout here so they appear in order with the fmt output.
// In a container or behind systemd, stdout is also the norm: the platform
// collects it. For a CLI tool whose stdout is DATA (lesson 10, and the
// CSV importer in lesson 22), log to os.Stderr so logs don't end up in
// the output Laravel reads.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"
)

func main() {
	if err := run(); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return // -h: the flag package already printed the usage
		}
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run() error {
	// ---------------------------------------------------------------
	// 1. LOADING CONFIG AT STARTUP
	// ---------------------------------------------------------------
	fmt.Println("1. Config from defaults, your environment and flags")
	// This is the real config for this program. Load it first and stop if
	// it's wrong: a fast, clear failure at startup beats a confusing one
	// halfway through a job.
	cfg, err := Load(os.Args[1:], os.LookupEnv)
	if err != nil {
		return fmt.Errorf("invalid config:\n%w", err)
	}
	// %+v prints field names. DBPassword is a Secret, so it can't leak.
	fmt.Printf("    %+v\n", cfg)

	// ---------------------------------------------------------------
	// 2. WHICH VALUE WINS?
	// ---------------------------------------------------------------
	fmt.Println("\n2. Precedence: default < env < flag")
	// A fake environment, so this section gives the same result for you
	// as for me. (mapEnv is in config.go.)
	env := mapEnv(map[string]string{"PORT": "9000"})
	fromEnv, _ := Load(nil, env)
	fromFlag, _ := Load([]string{"-port=7000"}, env)
	fmt.Printf("    default %d | with PORT=9000: %d | plus -port=7000: %d\n",
		defaults().Port, fromEnv.Port, fromFlag.Port)

	// ---------------------------------------------------------------
	// 3. BAD CONFIG: REPORT EVERYTHING AT ONCE
	// ---------------------------------------------------------------
	fmt.Println("\n3. A bad environment")
	_, err = Load(nil, mapEnv(map[string]string{
		"APP_ENV":      "production",
		"PORT":         "eighty",
		"LOG_LEVEL":    "loud",
		"HTTP_TIMEOUT": "5", // 5 what? Go durations need a unit: "5s"
		"WORKERS":      "0",
	}))
	// errors.Join puts one error per line.
	fmt.Println("    " + strings.ReplaceAll(err.Error(), "\n", "\n    "))
	// The joined error still wraps the originals, so errors.Is can find
	// the strconv error buried inside the PORT problem.
	fmt.Println("    contains a strconv syntax error:", errors.Is(err, strconv.ErrSyntax))

	// ---------------------------------------------------------------
	// 4. slog BASICS: LEVELS, KEY/VALUES, TEXT vs JSON
	// ---------------------------------------------------------------
	fmt.Println("\n4. The same log calls, as text and as JSON")
	for _, format := range []string{"text", "json"} {
		l := NewLogger(os.Stdout, format, slog.LevelInfo, true)

		l.Debug("cache miss", "key", "user:42") // below Info: dropped
		// Arguments after the message alternate: key, value, key, value...
		l.Info("order placed", "order_id", 1042, "total_pence", 2400, "customer", "Ada")
		// Typed helpers like slog.Duration avoid mixing up keys and values,
		// and are a little faster.
		l.Warn("slow query", slog.String("table", "orders"), slog.Duration("took", 820*time.Millisecond))
		l.Error("payment failed", "order_id", 1042, "err", errors.New("card declined"))
	}
	// Note "took": JSON has no duration type, so slog writes nanoseconds
	// (820000000). Log took_ms with d.Milliseconds() if a human will read it.
	//
	// Text (logfmt) is easy to read in a terminal. JSON is what log tools
	// (CloudWatch, Datadog, Loki, Papertrail) can search by field, e.g.
	// "all errors where order_id = 1042". Hence the LOG_FORMAT setting.

	// ---------------------------------------------------------------
	// 5. THE APP LOGGER, AND CHILD LOGGERS WITH .With
	// ---------------------------------------------------------------
	fmt.Printf("\n5. The app logger (LOG_FORMAT=%s, LOG_LEVEL=%s)\n", cfg.LogFormat, cfg.LogLevel)
	// A LevelVar is a level that can change later (section 8). The logger
	// reads it on every call.
	var level slog.LevelVar
	level.Set(cfg.LogLevel)
	logger := NewLogger(os.Stdout, cfg.LogFormat, &level, true)

	// SetDefault makes the package-level slog.Info AND the old log package
	// go through our logger, including logs from libraries that use log.
	slog.SetDefault(logger)
	log.Print("an old-style log.Print call")

	// .With returns a CHILD logger that adds these fields to every line.
	// Like Laravel's Log::withContext(), but scoped: the parent logger is
	// unchanged. Pass jobLog into the code doing the work (dependency
	// injection, like type-hinting LoggerInterface in a constructor).
	jobLog := logger.With("job_id", "imp-77", "file", "customers.csv")
	jobLog.Info("import started")
	// slog.Group nests keys: rows.ok=980 in text, {"rows":{"ok":980}} in JSON.
	jobLog.Info("import finished",
		slog.Group("rows", slog.Int("ok", 980), slog.Int("failed", 20)),
		slog.Duration("took", 1500*time.Millisecond))

	// ---------------------------------------------------------------
	// 6. REQUEST IDs FROM CONTEXT
	// ---------------------------------------------------------------
	fmt.Println("\n6. Request IDs carried in ctx")
	// Middleware would normally do this once per request (lesson 11).
	for _, id := range []string{"req-a1", "req-b2"} {
		ctx := WithRequestID(context.Background(), id)
		handleRequest(ctx, logger, "/orders")
	}
	// No ctx (or a ctx without an ID): no request_id, and no error either.
	logger.Info("background tick")

	// ---------------------------------------------------------------
	// 7. KEEPING SECRETS OUT OF LOGS
	// ---------------------------------------------------------------
	fmt.Println("\n7. Secrets")
	prod, err := Load(nil, mapEnv(map[string]string{"APP_ENV": "production", "DB_PASSWORD": "hunter2"}))
	if err != nil {
		return err
	}
	fmt.Printf("    fmt %%v: %v   %%#v: %#v\n", prod.DBPassword, prod.DBPassword)
	fmt.Printf("    Reveal() gives the real value (%d chars) for building a DSN\n", len(prod.DBPassword.Reveal()))
	// Config.LogValue (config.go) turns the struct into a group of fields.
	logger.Info("config loaded", "config", prod)
	// A plain string, not a Secret. NewLogger's ReplaceAttr catches it by
	// its key name.
	logger.Warn("login failed", "user", "ada", "password", "hunter2")

	// ---------------------------------------------------------------
	// 8. CHANGING THE LEVEL WHILE RUNNING
	// ---------------------------------------------------------------
	fmt.Println("\n8. Turning debug on and off")
	logger.Debug("you only see this if you started with debug")
	level.Set(slog.LevelDebug)
	logger.Debug("debug is on", "how", "level.Set(slog.LevelDebug)")
	level.Set(cfg.LogLevel)
	logger.Debug("back to normal: hidden again (unless you started with debug)")
	// A real service might flip this on a signal (lesson 19) or from an
	// admin endpoint, to debug production without a restart or redeploy.

	return nil
}

// handleRequest stands in for an HTTP handler. It never mentions the
// request ID, yet every line it logs has one, because it passes ctx.
//
// Use the ...Context methods (InfoContext, not Info) whenever you have a
// ctx. Plain Info can't see the ctx, so contextHandler can't add the ID.
func handleRequest(ctx context.Context, logger *slog.Logger, path string) {
	logger.InfoContext(ctx, "request started", "path", path)
	logger.DebugContext(ctx, "querying orders") // hidden at info level
	logger.InfoContext(ctx, "request finished", "status", 200)
}

// GOING FURTHER
//   - .env FILES: Go doesn't read .env by itself. joho/godotenv loads one
//     into the environment at startup. In production, set real environment
//     variables (Docker, systemd, Laravel Forge) and skip the file.
//   - LIBRARIES: caarlos0/env and kelseyhightower/envconfig fill a struct
//     from struct tags like `env:"PORT" envDefault:"8080"`. spf13/viper also
//     handles config files. Load() above is what they do for you.
//   - go vet CHECKS slog CALLS: logger.Info("msg", "key") with a missing
//     value is reported by `go vet`, and logs as !BADKEY if you ignore it.
//   - SPEED: logger.LogAttrs(ctx, slog.LevelInfo, "msg", slog.Int("n", 1))
//     avoids allocations in hot loops. Measure first (lesson 24).
//
// THINGS TO TRY
//   - Run with LOG_FORMAT=json and pipe it into jq:
//       LOG_FORMAT=json go run ./18-config-logging | grep '^{' | jq .
//   - Delete the WithAttrs method from contextHandler in logging.go, then
//     run `go test ./18-config-logging`. TestRequestIDSurvivesWith fails.
//   - Comment out the isSensitive check in NewLogger and see hunter2
//     appear in section 7.
//   - Add a REDIS_URL setting: a field, a get() line, and a test.
