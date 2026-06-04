package main

import (
	"log"
	"net/http"

	"pikpak2directlink/internal/app"
)

func main() {
	cfg := app.LoadConfig()

	server, err := app.NewServer(cfg)
	if err != nil {
		log.Fatalf("create server: %v", err)
	}

	log.Printf("listening on %s", cfg.Addr)
	if !cfg.IsConfigured() {
		log.Printf("no bootstrap credentials detected; use the account management page to add PikPak accounts")
	}

	if err := http.ListenAndServe(cfg.Addr, server.Handler()); err != nil {
		log.Fatalf("server stopped: %v", err)
	}
}
