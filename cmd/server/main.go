package main

import (
	"log"
	"net/http"

	"pikpak2directlink/internal/app"
	"pikpak2directlink/internal/version"
)

func main() {
	cfg := app.LoadConfig()

	server, err := app.NewServer(cfg)
	if err != nil {
		log.Fatalf("create server: %v", err)
	}

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

	if err := http.ListenAndServe(cfg.Addr, server.Handler()); err != nil {
		log.Fatalf("server stopped: %v", err)
	}
}
