package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"
)

const key = "0123456789abcdef"

func schedule(t *testing.T, s *Server, sendAt time.Time, headers map[string]string) int64 {
	t.Helper()
	if headers == nil {
		headers = map[string]string{}
	}
	headers["X-Api-Key"] = key
	body := fmt.Sprintf(`{"to":["user@example.com"],"subject":"Reminder","text":"hi","sendAt":%q}`, sendAt.Format(time.RFC3339))
	rec := do(s, "POST", "/mail/schedule", body, headers)
	if rec.Code != http.StatusCreated {
		t.Fatalf("schedule: got %d: %s", rec.Code, rec.Body)
	}
	var resp struct{ ID int64 }
	json.NewDecoder(rec.Body).Decode(&resp)
	return resp.ID
}

func TestScheduleSendsOnlyWhenDue(t *testing.T) {
	s, f := testServer(t)
	at := time.Now().Add(time.Hour)
	schedule(t, s, at, nil)

	s.SendDue(context.Background(), time.Now())
	if len(f.sent) != 0 {
		t.Fatal("sent before it was due")
	}
	s.SendDue(context.Background(), at.Add(time.Second))
	if len(f.sent) != 1 {
		t.Fatalf("sent %d, want 1", len(f.sent))
	}
	s.SendDue(context.Background(), at.Add(time.Hour))
	if len(f.sent) != 1 {
		t.Fatal("sent twice")
	}
}

func TestScheduleKeepsTestMode(t *testing.T) {
	s, f := testServer(t)
	schedule(t, s, time.Now(), map[string]string{TestHeader: "1"})
	s.SendDue(context.Background(), time.Now().Add(time.Second))
	if got := recipients(t, f.sent[0]); len(got) != 1 || got[0] != "test@wilhelm.my" {
		t.Fatalf("recipients %v", got)
	}
}

func TestScheduleCancel(t *testing.T) {
	s, f := testServer(t)
	id := schedule(t, s, time.Now(), nil)
	path := fmt.Sprintf("/mail/schedule/%d", id)
	if rec := do(s, "DELETE", path, "", map[string]string{"X-Api-Key": "wrong-key-wrong-key"}); rec.Code != http.StatusUnauthorized {
		t.Fatalf("cancel without key: got %d", rec.Code)
	}
	if rec := do(s, "DELETE", path, "", map[string]string{"X-Api-Key": key}); rec.Code != http.StatusOK {
		t.Fatalf("cancel: got %d: %s", rec.Code, rec.Body)
	}
	if rec := do(s, "DELETE", path, "", map[string]string{"X-Api-Key": key}); rec.Code != http.StatusNotFound {
		t.Fatalf("second cancel: got %d", rec.Code)
	}
	s.SendDue(context.Background(), time.Now().Add(time.Second))
	if len(f.sent) != 0 {
		t.Fatal("canceled mail was sent")
	}
}

func TestScheduleRetriesThenGivesUp(t *testing.T) {
	s, f := testServer(t)
	schedule(t, s, time.Now(), nil)
	f.fail = errors.New("smtp down")
	for range maxAttempts + 2 {
		s.SendDue(context.Background(), time.Now().Add(time.Second))
	}
	f.fail = nil
	s.SendDue(context.Background(), time.Now().Add(time.Second))
	if len(f.sent) != 0 {
		t.Fatalf("sent after %d failed attempts", maxAttempts)
	}
}

func TestScheduleValidation(t *testing.T) {
	s, _ := testServer(t)
	h := map[string]string{"X-Api-Key": key}
	for name, body := range map[string]string{
		"no sendAt":    `{"to":["user@example.com"],"subject":"x","text":"x"}`,
		"too far":      fmt.Sprintf(`{"to":["user@example.com"],"subject":"x","text":"x","sendAt":%q}`, time.Now().AddDate(2, 0, 0).Format(time.RFC3339)),
		"no recipient": fmt.Sprintf(`{"subject":"x","text":"x","sendAt":%q}`, time.Now().Format(time.RFC3339)),
	} {
		if rec := do(s, "POST", "/mail/schedule", body, h); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: got %d", name, rec.Code)
		}
	}
	if rec := do(s, "POST", "/mail/schedule", `{}`, nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("no key: got %d", rec.Code)
	}
}
