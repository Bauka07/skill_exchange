package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/rs/zerolog"
	zlog "github.com/rs/zerolog/log"

	smtpChannel "github.com/QosmuratSamat0/pairexx/notification-service/internal/channel/smtp"
	"github.com/QosmuratSamat0/pairexx/notification-service/internal/config"
	delivery "github.com/QosmuratSamat0/pairexx/notification-service/internal/delivery/http"
	"github.com/QosmuratSamat0/pairexx/notification-service/internal/usecase"
	"github.com/QosmuratSamat0/pairexx/pkg/mq"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func main() {
	cfg := config.Load()

	// ── Zerolog: human-readable console output in development ─────────────────
	zerolog.TimeFieldFormat = zerolog.TimeFormatUnix
	if cfg.AppEnv == "development" {
		zlog.Logger = zlog.Output(zerolog.ConsoleWriter{Out: os.Stderr})
	}

	// ── Startup config audit ───────────────────────────────────────────────────
	// Print every resolved SMTP and routing value immediately so a misconfigured
	// environment is caught before the first exchange request arrives.
	// stdlib log.Printf is used so the lines appear regardless of zerolog level.
	log.Printf("=== notification-service startup config ===")
	log.Printf("  APP_ENV            = %s", cfg.AppEnv)
	log.Printf("  PORT               = %s", cfg.Port)
	log.Printf("  SMTP_HOST          = %s", cfg.SMTPHost)
	log.Printf("  SMTP_PORT          = %s", cfg.SMTPPort)
	log.Printf("  SMTP_SENDER        = %q", cfg.SMTPSender)
	log.Printf("  SMTP_PASSWORD_LEN  = %d  (empty=%v)", len(cfg.SMTPPassword), cfg.SMTPPassword == "")
	log.Printf("  USER_SERVICE_URL   = %s", cfg.UserServiceURL)
	log.Printf("  INTERNAL_TOKEN_SET = %v  (len=%d)", cfg.InternalToken != "", len(cfg.InternalToken))
	log.Printf("==========================================")

	validateConfig(cfg)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	js, err := mq.NewJetStream(cfg.NATSURL)
	if err != nil {
		log.Printf("failed to init NATS JetStream: %v", err)
	} else {
		defer js.Close()
		worker := usecase.NewNotificationWorker(js)
		go worker.Start(ctx)
	}

	// ── SMTP channel ──────────────────────────────────────────────────────────
	smtpPort, err := strconv.Atoi(cfg.SMTPPort)
	if err != nil || smtpPort == 0 {
		smtpPort = 587
	}
	// smtp.New prints its own startup verification lines (host, sender, password_len).
	emailChannel := smtpChannel.New(
		cfg.SMTPHost,
		smtpPort,
		cfg.SMTPSender,
		cfg.SMTPPassword,
		cfg.UserServiceURL,
		cfg.InternalToken,
	)

	// Usecase fans out to all channels; the SMTP channel ignores non-email types.
	uc := usecase.New(emailChannel)

	r := chi.NewRouter()
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)

	delivery.New(r, uc, cfg.InternalToken)

	r.Handle("/metrics", promhttp.Handler())

	log.Printf("notification-service listening on port %s", cfg.Port)
	if err := http.ListenAndServe(":"+cfg.Port, r); err != nil {
		log.Fatalf("failed to start server: %v", err)
	}
}

func validateConfig(cfg *config.Config) {
	if cfg.SMTPSender == "" || cfg.SMTPPassword == "" {
		log.Printf("WARNING: SMTP_SENDER or SMTP_PASSWORD is empty — email delivery will fail. " +
			"Check config.env and ensure SMTP_SENDER and SMTP_PASSWORD are set.")
	}
	if cfg.AppEnv != "development" {
		if cfg.InternalToken == "" || cfg.InternalToken == "dev-internal-token" {
			log.Fatalf("INTERNAL_TOKEN must be set to a strong random value in non-development environments")
		}
	}
}
