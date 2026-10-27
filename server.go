package main

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// TestHeader, when present on a request (any value), reroutes the mail to the
// test inbox instead of the real recipients.
const TestHeader = "X-Mail-Test"

const maxBody = 1 << 20

type Server struct {
	cfg     *Config
	sender  Sender
	store   *Store
	limiter *limiter
}

func NewServer(cfg *Config, sender Sender, store *Store) *Server {
	return &Server{cfg: cfg, sender: sender, store: store, limiter: newLimiter(5, 10*time.Minute)}
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /mail/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})
	mux.HandleFunc("POST /mail/send", s.handleSend)
	mux.HandleFunc("POST /mail/schedule", s.handleSchedule)
	mux.HandleFunc("DELETE /mail/schedule/{id}", s.handleCancel)
	mux.HandleFunc("POST /mail/contact/{site}", s.handleContact)
	mux.HandleFunc("OPTIONS /mail/contact/{site}", s.handleContact)
	return mux
}

func isTest(r *http.Request) bool {
	_, ok := r.Header[http.CanonicalHeaderKey(TestHeader)]
	return ok
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"success": false, "message": msg})
}

func decode(w http.ResponseWriter, r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

func (s *Server) client(r *http.Request) (string, bool) {
	key := r.Header.Get("X-Api-Key")
	for k, name := range s.cfg.APIKeys {
		if subtle.ConstantTimeCompare([]byte(k), []byte(key)) == 1 {
			return name, true
		}
	}
	return "", false
}

func (s *Server) deliver(w http.ResponseWriter, r *http.Request, who string, m Mail) {
	test := isTest(r)
	msg, err := s.cfg.Build(m, test)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.sender.Send(r.Context(), msg); err != nil {
		log.Printf("send failed client=%s to=%v test=%t: %v", who, m.To, test, err)
		writeError(w, http.StatusBadGateway, "Failed to send email")
		return
	}
	log.Printf("sent client=%s to=%v test=%t subject=%q", who, m.To, test, m.Subject)
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": "Email sent successfully", "test": test})
}

// handleSend is the generic endpoint for backends on this machine.
func (s *Server) handleSend(w http.ResponseWriter, r *http.Request) {
	who, ok := s.client(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "invalid api key")
		return
	}
	var m Mail
	if err := decode(w, r, &m); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	if err := s.cfg.Validate(&m); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.deliver(w, r, who, m)
}

// ScheduleRequest is a Mail plus the time it should go out.
type ScheduleRequest struct {
	Mail
	SendAt time.Time `json:"sendAt"`
}

// handleSchedule stores a mail to be sent by the scheduler at sendAt. The test
// header is stored with it and applied when it is sent.
func (s *Server) handleSchedule(w http.ResponseWriter, r *http.Request) {
	who, ok := s.client(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "invalid api key")
		return
	}
	var req ScheduleRequest
	if err := decode(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	if req.SendAt.IsZero() {
		writeError(w, http.StatusBadRequest, "sendAt is required (RFC 3339, e.g. 2026-11-01T09:00:00+01:00)")
		return
	}
	if req.SendAt.After(time.Now().Add(maxScheduleAhead)) {
		writeError(w, http.StatusBadRequest, "sendAt is more than a year ahead")
		return
	}
	if err := s.cfg.Validate(&req.Mail); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	test := isTest(r)
	id, err := s.store.Add(who, req.Mail, req.SendAt, test)
	if err != nil {
		log.Printf("schedule failed client=%s: %v", who, err)
		writeError(w, http.StatusInternalServerError, "failed to schedule email")
		return
	}
	log.Printf("scheduled id=%d client=%s to=%v at=%s test=%t", id, who, req.To, req.SendAt.Format(time.RFC3339), test)
	writeJSON(w, http.StatusCreated, map[string]any{"success": true, "id": id, "sendAt": req.SendAt, "test": test})
}

func (s *Server) handleCancel(w http.ResponseWriter, r *http.Request) {
	who, ok := s.client(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "invalid api key")
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	switch err := s.store.Cancel(who, id); {
	case errors.Is(err, errNotFound):
		writeError(w, http.StatusNotFound, "no pending email with that id")
	case err != nil:
		log.Printf("cancel failed id=%d: %v", id, err)
		writeError(w, http.StatusInternalServerError, "failed to cancel")
	default:
		writeJSON(w, http.StatusOK, map[string]any{"success": true})
	}
}

// Contact is the body of a public contact form.
type Contact struct {
	Name    string `json:"name"`
	Email   string `json:"email"`
	Phone   string `json:"phone"`
	Message string `json:"message"`
}

func (c Contact) validate() error {
	if c.Email == "" && c.Phone == "" {
		return errors.New("email or phone is required")
	}
	if c.Email != "" && !validAddress(c.Email) {
		return errors.New("invalid email")
	}
	if strings.ContainsAny(c.Name+c.Email+c.Phone, "\r\n") {
		return errors.New("name, email and phone must be one line")
	}
	if len(c.Name) > 200 || len(c.Phone) > 50 || len(c.Message) > 10000 {
		return errors.New("field too long")
	}
	return nil
}

func (c Contact) text() string {
	var b strings.Builder
	if c.Message != "" {
		b.WriteString(c.Message + "\n\n")
	}
	if c.Name != "" {
		fmt.Fprintf(&b, "Namn: %s\n", c.Name)
	}
	if c.Email != "" {
		fmt.Fprintf(&b, "Email: %s\n", c.Email)
	}
	if c.Phone != "" {
		fmt.Fprintf(&b, "Telefonnummer: %s\n", c.Phone)
	}
	return b.String()
}

// handleContact is the public endpoint: the recipient comes from the site
// config, never from the request.
func (s *Server) handleContact(w http.ResponseWriter, r *http.Request) {
	site, ok := s.cfg.Sites[r.PathValue("site")]
	if !ok {
		writeError(w, http.StatusNotFound, "unknown site")
		return
	}
	if origin := r.Header.Get("Origin"); origin != "" && slices.Contains(site.Origins, origin) {
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Access-Control-Allow-Methods", "POST")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, "+TestHeader)
		w.Header().Set("Vary", "Origin")
	}
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if !s.limiter.allow(clientIP(r)) {
		writeError(w, http.StatusTooManyRequests, "too many requests, try again later")
		return
	}
	var c Contact
	if err := decode(w, r, &c); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	if err := c.validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	from := site.From
	if from == "" {
		from = s.cfg.DefaultFrom
	}
	who := c.Email
	if who == "" {
		who = c.Phone
	}
	m := Mail{
		From:     from,
		FromName: site.FromName,
		To:       site.To,
		ReplyTo:  c.Email,
		Subject:  site.Subject + " " + who,
		Text:     c.text(),
	}
	if err := s.cfg.Validate(&m); err != nil {
		writeError(w, http.StatusInternalServerError, "site misconfigured")
		log.Printf("site %s: %v", r.PathValue("site"), err)
		return
	}
	s.deliver(w, r, "contact:"+r.PathValue("site"), m)
}

// clientIP uses the last X-Forwarded-For hop, which Apache appends; the
// service only listens on localhost so the header can't be forged past it.
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		return strings.TrimSpace(parts[len(parts)-1])
	}
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	return host
}

// limiter allows n requests per window per key.
type limiter struct {
	mu     sync.Mutex
	n      int
	window time.Duration
	hits   map[string][]time.Time
}

func newLimiter(n int, window time.Duration) *limiter {
	return &limiter{n: n, window: window, hits: map[string][]time.Time{}}
}

func (l *limiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	recent := l.hits[key][:0]
	for _, t := range l.hits[key] {
		if now.Sub(t) < l.window {
			recent = append(recent, t)
		}
	}
	if len(recent) >= l.n {
		l.hits[key] = recent
		return false
	}
	l.hits[key] = append(recent, now)
	if len(l.hits) > 10000 {
		l.hits = map[string][]time.Time{}
	}
	return true
}
