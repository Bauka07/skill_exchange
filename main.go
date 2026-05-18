package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

type serviceSpec struct {
	Name string
	Dir  string
	Env  map[string]string
}

type runningService struct {
	spec   serviceSpec
	binary string
	cmd    *exec.Cmd
}

func main() {
	dryRun := flag.Bool("dry-run", false, "print commands without starting services")
	flag.Parse()

	zerolog.TimeFieldFormat = time.RFC3339
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr})

	root, err := os.Getwd()
	if err != nil {
		log.Fatal().Err(err).Msg("failed to determine workspace root")
	}

	// Load infrastructure secrets first (.env), then service config (config.env).
	// loadDotEnv is idempotent — it never overwrites an already-set env var,
	// so credentials in .env always take precedence over config.env if both
	// happen to define the same key.
	loadDotEnv(filepath.Join(root, ".env"))
	loadDotEnv(filepath.Join(root, "config.env"))
	applyDefaults()

	services := []serviceSpec{
		{
			Name: "user-service",
			Dir:  filepath.Join(root, "services", "user-service"),
			Env: map[string]string{
				"APP_ENV":        "development",
				"PORT":           "8081",
				"GRPC_PORT":      "50081",
				"REPO_DRIVER":    "postgres",
				"DB_URL":         localPostgresDSN("users_db"),
				"REDIS_URL":      localRedisURL(),
				"JWT_SECRET":     envOrDefault("JWT_SECRET", "dev-jwt-secret-change-in-production-min-32chars"),
				"INTERNAL_TOKEN": envOrDefault("INTERNAL_TOKEN", "dev-internal-token-change-in-production"),
			},
		},
		{
			Name: "matchmaking-service",
			Dir:  filepath.Join(root, "services", "matchmaking-service"),
			Env: map[string]string{
				"APP_ENV":               "development",
				"PORT":                  "8082",
				"GRPC_PORT":             "50082",
				"REPO_DRIVER":           "redis",
				"REDIS_URL":             localRedisURL(),
				"NATS_URL":              localNATSURL(),
				"USER_SERVICE_URL":      "http://localhost:8081",
				"USER_SERVICE_GRPC_URL": "localhost:50081",
				"CHAT_SERVICE_URL":      "http://localhost:8083",
				// Routing: where to send exchange-request email notifications.
				// Loaded from config.env → NOTIFICATION_SERVICE_URL.
				"NOTIFICATION_SERVICE_URL": envOrDefault("NOTIFICATION_SERVICE_URL", "http://localhost:8085"),
				"INTERNAL_TOKEN":           envOrDefault("INTERNAL_TOKEN", "dev-internal-token-change-in-production"),
			},
		},
		{
			Name: "chat-service",
			Dir:  filepath.Join(root, "services", "chat-service"),
			Env: map[string]string{
				"APP_ENV":                "development",
				"PORT":                   "8083",
				"REPO_DRIVER":            "postgres",
				"DB_URL":                 localPostgresDSN("chat_db"),
				"REDIS_URL":              localRedisURL(),
				"MODERATION_SERVICE_URL": "http://localhost:8084",
				"INTERNAL_TOKEN":         envOrDefault("INTERNAL_TOKEN", "dev-internal-token-change-in-production"),
			},
		},
		{
			Name: "moderation-service",
			Dir:  filepath.Join(root, "services", "moderation-service"),
			Env: map[string]string{
				"APP_ENV":                  "development",
				"PORT":                     "8084",
				"REPO_DRIVER":              "postgres",
				"DB_URL":                   localPostgresDSN("moderation_db"),
				"USER_SERVICE_URL":         "http://localhost:8081",
				"CHAT_SERVICE_URL":         "http://localhost:8083",
				"NOTIFICATION_SERVICE_URL": "http://localhost:8085",
				"INTERNAL_TOKEN":           envOrDefault("INTERNAL_TOKEN", "dev-internal-token-change-in-production"),
			},
		},
		{
			Name: "notification-service",
			Dir:  filepath.Join(root, "services", "notification-service"),
			Env: map[string]string{
				"APP_ENV":        "development",
				"PORT":           "8085",
				"NATS_URL":       localNATSURL(),
				"INTERNAL_TOKEN": envOrDefault("INTERNAL_TOKEN", "dev-internal-token-change-in-production"),
				// Routing: user-service internal endpoint for email + preference lookup.
				// Loaded from config.env → USER_SERVICE_URL.
				"USER_SERVICE_URL": envOrDefault("USER_SERVICE_URL", "http://localhost:8081"),
				// SMTP credentials — all loaded from config.env.
				// The notification-service config strips spaces from the password
				// automatically, so the grouped App Password format is safe here.
				"SMTP_HOST":     envOrDefault("SMTP_HOST", "smtp.gmail.com"),
				"SMTP_PORT":     envOrDefault("SMTP_PORT", "587"),
				"SMTP_SENDER":   envOrDefault("SMTP_SENDER", ""),
				"SMTP_PASSWORD": envOrDefault("SMTP_PASSWORD", ""),
			},
		},
		{
			Name: "api-gateway",
			Dir:  filepath.Join(root, "services", "api-gateway"),
			Env: map[string]string{
				"APP_ENV":                       "development",
				"PORT":                          "8080",
				"REDIS_URL":                     localRedisURL(),
				"JWT_SECRET":                    envOrDefault("JWT_SECRET", "dev-jwt-secret-change-in-production-min-32chars"),
				"INTERNAL_TOKEN":                envOrDefault("INTERNAL_TOKEN", "dev-internal-token-change-in-production"),
				"USER_SERVICE_URL":              "http://localhost:8081",
				"USER_SERVICE_GRPC_ADDR":        "localhost:50081",
				"MATCHMAKING_SERVICE_URL":       "http://localhost:8082",
				"MATCHMAKING_SERVICE_GRPC_ADDR": "localhost:50082",
				"CHAT_SERVICE_URL":              "http://localhost:8083",
				"MODERATION_SERVICE_URL":        "http://localhost:8084",
				"DEV_ALLOWED_ORIGINS":           "http://localhost:3000,http://127.0.0.1:3000",
			},
		},
	}

	if *dryRun {
		printPlan(services)
		return
	}

	if err := ensureLocalDatabasesWithRetry(); err != nil {
		log.Warn().Err(err).Msg("postgres bootstrap skipped or failed")
	}
	if err := runMigrationsWithRetry(root); err != nil {
		log.Fatal().Err(err).Msg("initial migrations failed")
	}

	sigCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithCancel(sigCtx)
	defer cancel()

	running, err := startServices(ctx, services)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to start one or more services")
	}

	log.Info().Msg("all services started; press Ctrl+C to stop")

	waitErr := watchServices(sigCtx, cancel, running)
	stopRunning(running)

	if waitErr != nil && !errors.Is(waitErr, context.Canceled) {
		log.Fatal().Err(waitErr).Msg("service exited unexpectedly")
	}

	log.Info().Msg("launcher stopped")
}

func applyDefaults() {
	defaults := map[string]string{
		"APP_ENV":           "development",
		"POSTGRES_USER":     "pairexx",
		"POSTGRES_PASSWORD": "pairexx_pass",
		"POSTGRES_HOST":     "localhost",
		"POSTGRES_PORT":     "5432",
		"REDIS_PASSWORD":    "redis_pass",
		"REDIS_HOST":        "localhost",
		"REDIS_PORT":        "6379",
		"NATS_URL":          "nats://localhost:4222",
		"JWT_SECRET":        "dev-jwt-secret-change-in-production-min-32chars",
		"INTERNAL_TOKEN":    "dev-internal-token-change-in-production",
	}
	for key, value := range defaults {
		setDefaultEnv(key, value)
	}
}

func loadDotEnv(path string) {
	file, err := os.Open(path)
	if err != nil {
		return
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "export ") {
			line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		value = strings.Trim(value, `"`)
		if key == "" {
			continue
		}
		if _, exists := os.LookupEnv(key); !exists {
			_ = os.Setenv(key, value)
		}
	}
}

func ensureLocalDatabases(ctx context.Context) error {
	conn, err := pgx.Connect(ctx, localAdminDSN())
	if err != nil {
		return err
	}
	defer conn.Close(ctx)

	for _, db := range []string{"users_db", "chat_db", "moderation_db"} {
		if _, err := conn.Exec(ctx, "CREATE DATABASE "+db); err != nil && !isDuplicateDatabaseError(err) {
			return fmt.Errorf("create database %s: %w", db, err)
		}
	}
	return nil
}

func ensureLocalDatabasesWithRetry() error {
	var lastErr error
	for attempt := 1; attempt <= 10; attempt++ {
		dbCtx, dbCancel := context.WithTimeout(context.Background(), 5*time.Second)
		lastErr = ensureLocalDatabases(dbCtx)
		dbCancel()
		if lastErr == nil {
			return nil
		}
		log.Info().Err(lastErr).Int("attempt", attempt).Msg("waiting for postgres before bootstrapping databases")
		time.Sleep(2 * time.Second)
	}
	return lastErr
}

func runMigrations(root string) error {
	targets := []struct {
		service string
		dbName  string
		dir     string
	}{
		{service: "user-service", dbName: "users_db", dir: filepath.Join(root, "services", "user-service", "migrations")},
		{service: "chat-service", dbName: "chat_db", dir: filepath.Join(root, "services", "chat-service", "migrations")},
		{service: "moderation-service", dbName: "moderation_db", dir: filepath.Join(root, "services", "moderation-service", "migrations")},
	}

	for _, target := range targets {
		dsn := localPostgresDSN(target.dbName)
		if err := migrateDatabase(target.dir, dsn); err != nil {
			return fmt.Errorf("%s migrations: %w", target.service, err)
		}
		log.Info().Str("service", target.service).Str("database", target.dbName).Msg("migrations applied")
	}
	return nil
}

func runMigrationsWithRetry(root string) error {
	var lastErr error
	for attempt := 1; attempt <= 10; attempt++ {
		lastErr = runMigrations(root)
		if lastErr == nil {
			return nil
		}
		if !isStartupConnectionError(lastErr) {
			return lastErr
		}
		log.Info().Err(lastErr).Int("attempt", attempt).Msg("waiting for postgres before running migrations")
		time.Sleep(2 * time.Second)
	}
	return lastErr
}

func migrateDatabase(migrationsDir, dsn string) error {
	absDir, err := filepath.Abs(migrationsDir)
	if err != nil {
		return err
	}
	files, err := filepath.Glob(filepath.Join(absDir, "*.up.sql"))
	if err != nil {
		return err
	}
	sort.Strings(files)
	if len(files) == 0 {
		return nil
	}

	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return err
	}
	defer conn.Close(ctx)

	for _, file := range files {
		content, err := os.ReadFile(file)
		if err != nil {
			return err
		}
		statements := splitSQLStatements(string(content))
		for _, statement := range statements {
			if _, err := conn.Exec(ctx, statement); err != nil {
				if isIgnorableMigrationError(err) {
					log.Debug().Err(err).Str("file", filepath.Base(file)).Msg("skipping already applied migration statement")
					continue
				}
				return fmt.Errorf("%s: %w", filepath.Base(file), err)
			}
		}
	}
	return nil
}

func splitSQLStatements(content string) []string {
	var statements []string
	var current strings.Builder
	inDollarQuote := false
	inSingleQuote := false
	inLineComment := false
	inBlockComment := false

	runes := []rune(content)
	for i := 0; i < len(runes); i++ {
		r := runes[i]

		// Handle block comments /* ... */
		if !inLineComment && !inSingleQuote && !inDollarQuote {
			if !inBlockComment && i < len(runes)-1 && runes[i] == '/' && runes[i+1] == '*' {
				inBlockComment = true
				current.WriteRune(r)
				current.WriteRune(runes[i+1])
				i++
				continue
			}
			if inBlockComment && i < len(runes)-1 && runes[i] == '*' && runes[i+1] == '/' {
				inBlockComment = false
				current.WriteRune(r)
				current.WriteRune(runes[i+1])
				i++
				continue
			}
		}

		if inBlockComment {
			current.WriteRune(r)
			continue
		}

		// Handle line comments -- ...
		if !inSingleQuote && !inDollarQuote && !inBlockComment {
			if !inLineComment && i < len(runes)-1 && runes[i] == '-' && runes[i+1] == '-' {
				inLineComment = true
				current.WriteRune(r)
				current.WriteRune(runes[i+1])
				i++
				continue
			}
		}
		if inLineComment {
			if r == '\n' {
				inLineComment = false
			}
			current.WriteRune(r)
			continue
		}

		// Handle dollar quotes (naive: only checks for $$)
		if !inSingleQuote && i < len(runes)-1 && runes[i] == '$' && runes[i+1] == '$' {
			inDollarQuote = !inDollarQuote
			current.WriteRune(r)
			current.WriteRune(runes[i+1])
			i++
			continue
		}

		// Handle single quotes
		if !inDollarQuote && r == '\'' {
			// Check for escaped single quote ''
			if i < len(runes)-1 && runes[i+1] == '\'' {
				current.WriteRune(r)
				current.WriteRune(runes[i+1])
				i++
				continue
			}
			inSingleQuote = !inSingleQuote
			current.WriteRune(r)
			continue
		}

		if r == ';' && !inDollarQuote && !inSingleQuote && !inLineComment && !inBlockComment {
			stmt := strings.TrimSpace(current.String())
			if stmt != "" {
				statements = append(statements, stmt)
			}
			current.Reset()
		} else {
			current.WriteRune(r)
		}
	}

	stmt := strings.TrimSpace(current.String())
	if stmt != "" {
		statements = append(statements, stmt)
	}

	return statements
}

func isStartupConnectionError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "connection refused") ||
		strings.Contains(message, "cannot assign requested address") ||
		strings.Contains(message, "no such host") ||
		strings.Contains(message, "timeout") ||
		strings.Contains(message, "server closed the connection")
}

func isDuplicateDatabaseError(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "42P04"
}

func isIgnorableMigrationError(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	return pgErr.Code == "42P07" || pgErr.Code == "42710" || pgErr.Code == "42P04"
}

func localAdminDSN() string {
	return fmt.Sprintf("postgres://%s:%s@%s:%s/postgres?sslmode=disable", envOrDefault("POSTGRES_USER", "pairexx"), envOrDefault("POSTGRES_PASSWORD", "pairexx_pass"), envOrDefault("POSTGRES_HOST", "localhost"), envOrDefault("POSTGRES_PORT", "5432"))
}

func localPostgresDSN(dbName string) string {
	return fmt.Sprintf("postgres://%s:%s@%s:%s/%s?sslmode=disable", envOrDefault("POSTGRES_USER", "pairexx"), envOrDefault("POSTGRES_PASSWORD", "pairexx_pass"), envOrDefault("POSTGRES_HOST", "localhost"), envOrDefault("POSTGRES_PORT", "5432"), dbName)
}

func localRedisURL() string {
	return fmt.Sprintf("redis://:%s@%s:%s", envOrDefault("REDIS_PASSWORD", "redis_pass"), envOrDefault("REDIS_HOST", "localhost"), envOrDefault("REDIS_PORT", "6379"))
}

func localNATSURL() string {
	host := envOrDefault("NATS_HOST", "localhost")
	port := envOrDefault("NATS_PORT", "4222")
	return fmt.Sprintf("nats://%s:%s", host, port)
}

func startServices(ctx context.Context, services []serviceSpec) ([]*runningService, error) {
	running := make([]*runningService, 0, len(services))
	for _, svc := range services {
		binary, err := buildServiceBinary(svc)
		if err != nil {
			stopRunning(running)
			return nil, err
		}

		cmd := exec.CommandContext(ctx, binary)
		cmd.Dir = svc.Dir
		cmd.Env = mergedEnv(svc.Env)

		stdout, err := cmd.StdoutPipe()
		if err != nil {
			_ = os.Remove(binary)
			stopRunning(running)
			return nil, err
		}
		stderr, err := cmd.StderrPipe()
		if err != nil {
			_ = os.Remove(binary)
			stopRunning(running)
			return nil, err
		}

		if err := cmd.Start(); err != nil {
			_ = os.Remove(binary)
			stopRunning(running)
			return nil, err
		}

		rs := &runningService{spec: svc, binary: binary, cmd: cmd}
		running = append(running, rs)
		go streamOutput(svc.Name, stdout)
		go streamOutput(svc.Name, stderr)
		log.Info().Str("service", svc.Name).Str("dir", svc.Dir).Msg("started")
	}
	return running, nil
}

func buildServiceBinary(svc serviceSpec) (string, error) {
	name := strings.ReplaceAll(svc.Name, string(os.PathSeparator), "-")
	ext := ""
	if runtime.GOOS == "windows" {
		ext = ".exe"
	}
	binary := filepath.Join(os.TempDir(), fmt.Sprintf("pairexx-%s-%d%s", name, time.Now().UnixNano(), ext))

	cmd := exec.Command("go", "build", "-o", binary, "./cmd")
	cmd.Dir = svc.Dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		_ = os.Remove(binary)
		return "", fmt.Errorf("build %s failed: %w\n%s", svc.Name, err, string(output))
	}
	return binary, nil
}

func mergedEnv(overrides map[string]string) []string {
	values := append([]string{}, os.Environ()...)
	for key, value := range overrides {
		values = append(values, key+"="+value)
	}
	return values
}

func streamOutput(prefix string, reader io.ReadCloser) {
	defer reader.Close()
	scanner := bufio.NewScanner(reader)
	buffer := make([]byte, 0, 64*1024)
	scanner.Buffer(buffer, 1024*1024)
	for scanner.Scan() {
		fmt.Printf("[%s] %s\n", prefix, scanner.Text())
	}
}

func stopRunning(running []*runningService) {
	for i := len(running) - 1; i >= 0; i-- {
		rs := running[i]
		if rs == nil || rs.cmd == nil || rs.cmd.Process == nil {
			continue
		}
		_ = rs.cmd.Process.Kill()
		_, _ = rs.cmd.Process.Wait()
		_ = os.Remove(rs.binary)
	}
}

func watchServices(ctx context.Context, cancel context.CancelFunc, running []*runningService) error {
	var wg sync.WaitGroup
	errCh := make(chan error, len(running))
	for _, rs := range running {
		wg.Add(1)
		go func(rs *runningService) {
			defer wg.Done()
			errCh <- rs.cmd.Wait()
		}(rs)
	}

	select {
	case <-ctx.Done():
		cancel()
		wg.Wait()
		return ctx.Err()
	case err := <-errCh:
		cancel()
		wg.Wait()
		return err
	}
}

func envOrDefault(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok && value != "" {
		return value
	}
	return fallback
}

func setDefaultEnv(key, value string) {
	if current, ok := os.LookupEnv(key); ok && current != "" {
		return
	}
	_ = os.Setenv(key, value)
}

func printPlan(services []serviceSpec) {
	fmt.Println("Launch plan:")
	for _, svc := range services {
		fmt.Printf("- %s -> go build -o <temp> ./cmd && <temp>\n", svc.Name)
	}
}
