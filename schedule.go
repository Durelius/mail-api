package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"time"

	_ "modernc.org/sqlite"
)

// maxAttempts is how many times a due mail is tried before it is given up on.
const maxAttempts = 5

// maxScheduleAhead caps how far in the future a mail can be scheduled.
const maxScheduleAhead = 366 * 24 * time.Hour

// Store keeps scheduled mail in SQLite until it is due.
type Store struct {
	db *sql.DB
}

func OpenStore(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS scheduled (
		id          INTEGER PRIMARY KEY,
		client      TEXT    NOT NULL,
		send_at     INTEGER NOT NULL,
		mail        TEXT    NOT NULL,
		test        INTEGER NOT NULL,
		created_at  INTEGER NOT NULL,
		sent_at     INTEGER,
		canceled_at INTEGER,
		attempts    INTEGER NOT NULL DEFAULT 0,
		last_error  TEXT
	);
	CREATE INDEX IF NOT EXISTS scheduled_due ON scheduled (send_at) WHERE sent_at IS NULL AND canceled_at IS NULL`)
	if err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) Add(client string, m Mail, sendAt time.Time, test bool) (int64, error) {
	raw, err := json.Marshal(m)
	if err != nil {
		return 0, err
	}
	res, err := s.db.Exec(`INSERT INTO scheduled (client, send_at, mail, test, created_at) VALUES (?, ?, ?, ?, ?)`,
		client, sendAt.Unix(), string(raw), test, time.Now().Unix())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

var errNotFound = errors.New("not found")

// Cancel stops a mail that hasn't been sent. Clients can only cancel their own.
func (s *Store) Cancel(client string, id int64) error {
	res, err := s.db.Exec(`UPDATE scheduled SET canceled_at = ? WHERE id = ? AND client = ? AND sent_at IS NULL AND canceled_at IS NULL`,
		time.Now().Unix(), id, client)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errNotFound
	}
	return nil
}

type dueMail struct {
	id     int64
	client string
	mail   Mail
	test   bool
}

func (s *Store) due(now time.Time) ([]dueMail, error) {
	rows, err := s.db.Query(`SELECT id, client, mail, test FROM scheduled
		WHERE send_at <= ? AND sent_at IS NULL AND canceled_at IS NULL AND attempts < ?
		ORDER BY send_at`, now.Unix(), maxAttempts)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []dueMail
	for rows.Next() {
		var d dueMail
		var raw string
		if err := rows.Scan(&d.id, &d.client, &raw, &d.test); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(raw), &d.mail); err != nil {
			return nil, fmt.Errorf("scheduled %d: %w", d.id, err)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *Store) markSent(id int64) error {
	_, err := s.db.Exec(`UPDATE scheduled SET sent_at = ?, attempts = attempts + 1, last_error = NULL WHERE id = ?`, time.Now().Unix(), id)
	return err
}

func (s *Store) markFailed(id int64, sendErr error) error {
	_, err := s.db.Exec(`UPDATE scheduled SET attempts = attempts + 1, last_error = ? WHERE id = ?`, sendErr.Error(), id)
	return err
}

// SendDue sends every mail whose time has come. A failed mail stays queued and
// is retried on the next run, up to maxAttempts.
func (srv *Server) SendDue(ctx context.Context, now time.Time) {
	due, err := srv.store.due(now)
	if err != nil {
		log.Printf("scheduler: %v", err)
		return
	}
	for _, d := range due {
		msg, err := srv.cfg.Build(d.mail, d.test)
		if err == nil {
			err = srv.sender.Send(ctx, msg)
		}
		if err != nil {
			log.Printf("scheduled id=%d client=%s to=%v failed: %v", d.id, d.client, d.mail.To, err)
			if err := srv.store.markFailed(d.id, err); err != nil {
				log.Printf("scheduler: %v", err)
			}
			continue
		}
		log.Printf("scheduled id=%d sent client=%s to=%v test=%t subject=%q", d.id, d.client, d.mail.To, d.test, d.mail.Subject)
		if err := srv.store.markSent(d.id); err != nil {
			log.Printf("scheduler: %v", err)
		}
	}
}

// RunScheduler checks for due mail at start-up and then every interval.
func (srv *Server) RunScheduler(ctx context.Context, interval time.Duration) {
	srv.SendDue(ctx, time.Now())
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			srv.SendDue(ctx, now)
		}
	}
}
