package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"pikpak2directlink/internal/app"
	"pikpak2directlink/internal/version"
)

const gracefulShutdownTimeout = 30 * time.Second

func main() {
	if err := run(); err != nil {
		log.Printf("server stopped: %v", err)
		os.Exit(1)
	}
}

func run() error {
	cfg := app.LoadConfig()
	if handled, err := runStorageCommand(os.Args[1:], cfg); handled {
		if err != nil {
			return fmt.Errorf("storage command failed: %w", err)
		}
		return nil
	}

	server, err := app.NewServer(cfg)
	if err != nil {
		return fmt.Errorf("create server: %w", err)
	}
	closed := false
	defer func() {
		if !closed {
			_ = server.Close()
		}
	}()

	log.Printf("Pikpak2DirectLink %s starting", version.Version)
	log.Printf("listening on %s", cfg.Addr)
	if !cfg.IsConfigured() {
		log.Printf("no bootstrap credentials detected; use the account management page to add PikPak accounts")
	}
	if cfg.HasFixedPassword() {
		log.Printf("access password is pinned via ACCESS_PASSWORD; the first-visitor setup flow is disabled")
	} else {
		log.Printf("access gate enabled; the first visitor will be prompted to set an admin password")
	}

	httpServer := &http.Server{Addr: cfg.Addr, Handler: server.Handler()}
	serveErr := make(chan error, 1)
	go func() { serveErr <- httpServer.ListenAndServe() }()

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)

	select {
	case sig := <-signals:
		log.Printf("received %s; shutting down", sig)
	case <-server.RestartRequested():
		log.Printf("online update installed; shutting down for restart")
	case err := <-serveErr:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), gracefulShutdownTimeout)
	shutdownErr := httpServer.Shutdown(ctx)
	cancel()
	closeErr := server.Close()
	closed = true
	if shutdownErr != nil {
		_ = httpServer.Close()
	}
	return errors.Join(shutdownErr, closeErr)
}
