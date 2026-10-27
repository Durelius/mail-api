// Command mail-api sends mail for the sites on this VPS through the local
// Postfix. See README.md.
package main

import (
	"context"
	"log"
	"net/http"
	"time"
)

func main() {
	cfg, err := LoadConfig()
	if err != nil {
		log.Fatal(err)
	}
	if len(cfg.APIKeys) == 0 {
		log.Print("warning: no API_KEYS set, POST /mail/send will reject everything")
	}
	store, err := OpenStore(cfg.DBPath)
	if err != nil {
		log.Fatalf("open %s: %v", cfg.DBPath, err)
	}
	defer store.Close()
	api := NewServer(cfg, SMTPSender{Host: cfg.SMTPHost, Port: cfg.SMTPPort}, store)
	go api.RunScheduler(context.Background(), cfg.CheckInterval)

	srv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           api.Routes(),
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      30 * time.Second,
	}
	log.Printf("mail-api listening on %s, smtp %s:%d, %d sites, checking scheduled mail every %s", cfg.ListenAddr, cfg.SMTPHost, cfg.SMTPPort, len(cfg.Sites), cfg.CheckInterval)
	log.Fatal(srv.ListenAndServe())
}
