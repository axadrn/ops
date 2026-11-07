package main

import (
	"context"
	"errors"
	"fmt"
	"github.com/axadrn/moin/controlplane/internal/dbbackup"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/axadrn/moin/controlplane/internal/app"
	"github.com/axadrn/moin/controlplane/internal/config"
	"github.com/axadrn/moin/controlplane/internal/logger"
	"github.com/axadrn/moin/controlplane/internal/routes"
	"github.com/joho/godotenv"
)

const shutdownTimeout = 10 * time.Second

func main() {
	envErr := godotenv.Load()
	if envErr != nil && !errors.Is(envErr, os.ErrNotExist) {
		logger.Errors("application stopped", fmt.Errorf("load .env: %w", envErr))
		os.Exit(1)
	}
	cfg, err := config.Load()
	if err != nil {
		logger.Errors("application stopped", err)
		os.Exit(1)
	}
	// A word after the binary is a one-off task for the operator, run next
	// to the server on the same database. Only its result goes to stdout.
	if len(os.Args) > 1 {
		slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})))
		err = command(cfg, os.Args[1:])
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	logger.Init(logger.Load(cfg.IsDevelopment()))
	err = run(cfg)
	if err != nil {
		logger.Errors("application stopped", err)
		os.Exit(1)
	}
}

func run(cfg *config.Config) error {
	// The database comes back from the bucket before anything opens it.
	bucket := dbbackup.Bucket{Endpoint: cfg.S3Endpoint, Region: cfg.S3Region, Bucket: cfg.S3Bucket, AccessKey: cfg.S3AccessKey, SecretKey: cfg.S3SecretKey}
	file := dbbackup.File(cfg.DBConnection)
	backup := cfg.BackupDatabase && file != ""
	if backup {
		restoreCtx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		err := dbbackup.RestoreIfMissing(restoreCtx, bucket, file)
		cancel()
		if err != nil {
			return err
		}
	} else if cfg.IsProduction() {
		slog.Warn("database backup off: a dead server takes the database with it; set S3_BUCKET and BACKUP_DATABASE=true")
	}
	if cfg.IsProduction() && cfg.CloudflareAccountID == "" {
		slog.Warn("email off: sign-in links and invitations go to this log instead; set CLOUDFLARE_ACCOUNT_ID and CLOUDFLARE_API_TOKEN, or print a sign-in link with ./main sign-in-link")
	}
	application, err := app.New(cfg)
	if err != nil {
		return fmt.Errorf("initialize app: %w", err)
	}
	if backup {
		stopBackup, err := dbbackup.Start(context.Background(), bucket, file)
		if err != nil {
			return err
		}
		defer stopBackup()
	}
	telemetry, stopTelemetry := context.WithCancel(context.Background())
	defer stopTelemetry()
	go application.TelemetryService.Run(telemetry)
	go application.UpdateService.Run(telemetry)
	go application.CatalogService.Run(telemetry)
	go application.OldDomainService.Run(telemetry)
	defer func() {
		if closeErr := application.Close(); closeErr != nil {
			slog.Error("failed to close app", "error", closeErr)
		}
	}()
	// The uptime checks stop before the database closes: this defer runs
	// first.
	if cfg.UptimeChecks {
		uptime, stopUptime := context.WithCancel(context.Background())
		stopped := make(chan struct{})
		go func() {
			application.AppService.RunUptime(uptime)
			close(stopped)
		}()
		defer func() {
			stopUptime()
			<-stopped
		}()
	}
	// Metrics write their last minutes before the database closes, too.
	metrics, stopMetrics := context.WithCancel(context.Background())
	metricsStopped := make(chan struct{})
	go func() {
		application.AppService.RunMetrics(metrics)
		close(metricsStopped)
	}()
	defer func() {
		stopMetrics()
		<-metricsStopped
	}()
	server := &http.Server{
		Addr: ":" + cfg.Port, Handler: routes.SetupRoutes(application),
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second,
		WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second,
	}
	slog.Info("server starting", "port", cfg.Port, "env", cfg.AppEnv, "url", "http://localhost:"+cfg.Port)
	if err := serve(server); err != nil {
		return err
	}
	slog.Info("server stopped")
	return nil
}

func serve(server *http.Server) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// ListenAndServe blocks. Keep SIGTERM responsive.
	serverError := make(chan error, 1)
	go func() {
		serverError <- server.ListenAndServe()
	}()

	select {
	case err := <-serverError:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("serve HTTP: %w", err)
	case <-ctx.Done():
	}

	slog.Info("server shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		// The grace period is over. Close what remains.
		_ = server.Close()
		return fmt.Errorf("shut down HTTP server: %w", err)
	}
	return nil
}
