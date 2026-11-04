// LESSON 18 — CONFIG & LOGGING (part 2: loading config)
//
// One Config struct, filled ONCE at startup, then passed to whatever needs
// it. Nothing else in the program calls os.Getenv.
//
//	Laravel                                Go
//	.env + config/app.php                  a Config struct + Load()
//	env('PORT', 8080)                      get(&env, "PORT", 8080, strconv.Atoi)
//	config('app.port')                     cfg.Port (a typed field, not a string key)
//	php artisan serve --port=7000          go run . -port=7000 (the flag package)
//
// Two differences to notice:
//   - Everything from the environment is a STRING. Laravel's env() quietly
//     turns "true" into true and "null" into null. Go makes you parse each
//     value, so "eighty" in PORT is an error at startup, not a surprise at
//     3am.
//   - All problems are collected and reported TOGETHER (errors.Join), so you
//     fix your .env in one go instead of one error per restart.
package main

import (
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Config holds every setting, already parsed into proper types.
type Config struct {
	AppEnv     string        // APP_ENV       local | staging | production
	Port       int           // PORT          1–65535
	LogLevel   slog.Level    // LOG_LEVEL     debug | info | warn | error
	LogFormat  string        // LOG_FORMAT    text | json
	Timeout    time.Duration // HTTP_TIMEOUT  "5s", "250ms", "1m30s"
	Workers    int           // WORKERS       jobs to run at once
	Origins    []string      // CORS_ORIGINS  comma-separated list
	DBPassword Secret        // DB_PASSWORD   required in production
}

// defaults are the values used when nothing else is set. Pick defaults
// that are safe for local development, like Laravel's .env.example.
func defaults() Config {
	return Config{
		AppEnv:    "local",
		Port:      8080,
		LogLevel:  slog.LevelInfo,
		LogFormat: "text",
		Timeout:   5 * time.Second,
		Workers:   4,
	}
}

// Load builds the Config in three layers. Each layer overrides the one
// before it:
//
//	defaults  ->  environment variables  ->  command-line flags
//
// lookup is how we read the environment. main passes os.LookupEnv. Tests
// pass a map (see mapEnv), so they never depend on the real environment.
// args is normally os.Args[1:].
func Load(args []string, lookup func(string) (string, bool)) (Config, error) {
	cfg := defaults()

	// Layer 2: environment variables. A bad value is recorded in env.errs
	// and the default is kept, so we can carry on and find EVERY problem.
	env := &envReader{lookup: lookup}
	cfg.AppEnv = get(env, "APP_ENV", cfg.AppEnv, parseString)
	cfg.Port = get(env, "PORT", cfg.Port, strconv.Atoi)
	cfg.LogLevel = get(env, "LOG_LEVEL", cfg.LogLevel, parseLevel)
	cfg.LogFormat = get(env, "LOG_FORMAT", cfg.LogFormat, parseString)
	cfg.Timeout = get(env, "HTTP_TIMEOUT", cfg.Timeout, time.ParseDuration)
	cfg.Workers = get(env, "WORKERS", cfg.Workers, strconv.Atoi)
	cfg.Origins = get(env, "CORS_ORIGINS", cfg.Origins, parseList)
	cfg.DBPassword = get(env, "DB_PASSWORD", cfg.DBPassword, parseSecret)

	// Layer 3: flags. Each flag's default is the value we have SO FAR, so
	// a flag only changes something if it's actually passed.
	//
	// flag.NewFlagSet instead of the global flag.Parse() (lesson 10) means
	// Load can be called many times, which the tests and main.go both do.
	// ContinueOnError returns an error instead of calling os.Exit.
	fs := flag.NewFlagSet("18-config-logging", flag.ContinueOnError)
	fs.StringVar(&cfg.AppEnv, "env", cfg.AppEnv, "local, staging or production")
	fs.IntVar(&cfg.Port, "port", cfg.Port, "port to listen on")
	// slog.Level knows how to parse "debug" etc. itself (it implements
	// encoding.TextUnmarshaler), so flag.TextVar can fill it directly.
	fs.TextVar(&cfg.LogLevel, "log-level", cfg.LogLevel, "debug, info, warn or error")
	fs.StringVar(&cfg.LogFormat, "log-format", cfg.LogFormat, "text or json")
	fs.IntVar(&cfg.Workers, "workers", cfg.Workers, "jobs to run at once")
	if err := fs.Parse(args); err != nil {
		return cfg, err // the flag package has already printed the usage
	}

	// Finally, check the values make sense TOGETHER.
	errs := append(env.errs, cfg.Validate())
	return cfg, errors.Join(errs...) // nil if every error is nil
}

// Validate checks rules that parsing alone can't, like ranges and
// settings that depend on each other. Like Laravel's validation rules,
// but for your own config.
func (c Config) Validate() error {
	var errs []error
	if !slices.Contains([]string{"local", "staging", "production"}, c.AppEnv) {
		errs = append(errs, fmt.Errorf("APP_ENV must be local, staging or production, got %q", c.AppEnv))
	}
	if c.Port < 1 || c.Port > 65535 {
		errs = append(errs, fmt.Errorf("PORT must be 1–65535, got %d", c.Port))
	}
	if c.LogFormat != "text" && c.LogFormat != "json" {
		errs = append(errs, fmt.Errorf("LOG_FORMAT must be text or json, got %q", c.LogFormat))
	}
	if c.Timeout <= 0 {
		errs = append(errs, fmt.Errorf("HTTP_TIMEOUT must be positive, got %v", c.Timeout))
	}
	if c.Workers < 1 {
		errs = append(errs, fmt.Errorf("WORKERS must be at least 1, got %d", c.Workers))
	}
	if c.AppEnv == "production" && c.DBPassword == "" {
		errs = append(errs, errors.New("DB_PASSWORD is required in production"))
	}
	return errors.Join(errs...)
}

// LogValue controls how Config looks in logs (see logging.go). Without it,
// slog would print the struct with fmt, all on one line. With it, each
// field becomes its own key: config.port=8080, or nested JSON.
func (c Config) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("env", c.AppEnv),
		slog.Int("port", c.Port),
		slog.String("log_level", c.LogLevel.String()),
		slog.String("log_format", c.LogFormat),
		slog.Duration("timeout", c.Timeout),
		slog.Int("workers", c.Workers),
		slog.Any("origins", c.Origins),
		slog.Any("db_password", c.DBPassword), // Secret redacts itself
	)
}

// ---------------------------------------------------------------------
// Reading environment variables
// ---------------------------------------------------------------------

// envReader reads variables and collects parse errors as it goes.
type envReader struct {
	lookup func(string) (string, bool)
	errs   []error
}

// get reads one variable and parses it into type T. It's a generic
// FUNCTION, not a method, because Go methods can't have their own type
// parameters (lesson 09). One function handles ints, durations, levels,
// lists and more, since the caller passes the right parser:
//
//	get(env, "PORT", 8080, strconv.Atoi)              // T is int
//	get(env, "HTTP_TIMEOUT", 5*time.Second, time.ParseDuration)
//
// An unset or blank variable means "use the default". So `PORT=` in a
// .env file doesn't become port 0.
func get[T any](r *envReader, key string, def T, parse func(string) (T, error)) T {
	raw, ok := r.lookup(key)
	raw = strings.TrimSpace(raw)
	if !ok || raw == "" {
		return def
	}
	v, err := parse(raw)
	if err != nil {
		// %w keeps the original error, so errors.Is(err, strconv.ErrSyntax)
		// still works on the joined error that Load returns.
		r.errs = append(r.errs, fmt.Errorf("%s=%q: %w", key, raw, err))
		return def
	}
	return v
}

func parseString(s string) (string, error) { return s, nil }
func parseSecret(s string) (Secret, error) { return Secret(s), nil }

// parseLevel turns "debug", "INFO", "warn" etc. into a slog.Level.
func parseLevel(s string) (slog.Level, error) {
	var l slog.Level
	err := l.UnmarshalText([]byte(s))
	return l, err
}

// parseList splits "a.com, b.com,,c.com" into [a.com b.com c.com],
// like explode(',', ...) followed by array_filter(array_map('trim', ...)).
func parseList(s string) ([]string, error) {
	var out []string
	for part := range strings.SplitSeq(s, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out, nil
}

// mapEnv turns a map into a lookup function, so tests and examples can
// use a fake environment. A func type as a dependency is Go's lightweight
// alternative to an interface with one method.
func mapEnv(m map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		v, ok := m[key]
		return v, ok
	}
}

// ---------------------------------------------------------------------
// Secrets
// ---------------------------------------------------------------------

// Secret is a string that won't print itself. fmt, slog and encoding/json
// all show "[redacted]". Call Reveal() when you really need the value,
// for example to build a database DSN.
//
// PHP 8.2 has the same idea: the #[\SensitiveParameter] attribute hides
// a value from stack traces.
type Secret string

const redacted = "[redacted]"

// Reveal returns the real value. It's a separate, obvious method so a
// leak is easy to spot in code review.
func (s Secret) Reveal() string { return string(s) }

// String is used by fmt's %v, %s and %+v (the fmt.Stringer interface).
// An empty secret shows as "" so you can still see it isn't set.
func (s Secret) String() string {
	if s == "" {
		return ""
	}
	return redacted
}

// GoString is used by %#v, which would otherwise show the raw string.
func (s Secret) GoString() string { return fmt.Sprintf("Secret(%q)", s.String()) }

// LogValue is used by slog (the slog.LogValuer interface).
func (s Secret) LogValue() slog.Value { return slog.StringValue(s.String()) }

// MarshalText is used by encoding/json, so a Config sent to json.Marshal,
// or logged by a JSON handler, doesn't leak either.
func (s Secret) MarshalText() ([]byte, error) { return []byte(s.String()), nil }
