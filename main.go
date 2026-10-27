// Command mail-api sends mail for the sites on this VPS through the local
// Postfix. See README.md.
package main

import (
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
	srv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           NewServer(cfg, SMTPSender{Host: cfg.SMTPHost, Port: cfg.SMTPPort}).Routes(),
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      30 * time.Second,
	}
	log.Printf("mail-api listening on %s, smtp %s:%d, %d sites", cfg.ListenAddr, cfg.SMTPHost, cfg.SMTPPort, len(cfg.Sites))
	log.Fatal(srv.ListenAndServe())
}
