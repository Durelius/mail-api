package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	gomail "github.com/wneessen/go-mail"
)

type fakeSender struct{ sent []*gomail.Msg }

func (f *fakeSender) Send(_ context.Context, m *gomail.Msg) error {
	f.sent = append(f.sent, m)
	return nil
}

func testServer() (*Server, *fakeSender) {
	cfg := &Config{
		DefaultFrom:        "noreply@wilhelm.my",
		AllowedFromDomains: []string{"wilhelm.my"},
		TestRecipient:      "test@wilhelm.my",
		APIKeys:            map[string]string{"0123456789abcdef": "helptype"},
		Sites: map[string]Site{"dj": {
			To: []string{"carl@example.com"}, Subject: "Ny förfrågan från",
			Origins: []string{"https://djcarldurelius.se"},
		}},
	}
	f := &fakeSender{}
	return NewServer(cfg, f), f
}

func do(s *Server, method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	s.Routes().ServeHTTP(rec, req)
	return rec
}

func recipients(t *testing.T, m *gomail.Msg) []string {
	t.Helper()
	r, err := m.GetRecipients()
	if err != nil {
		t.Fatal(err)
	}
	for i := range r {
		r[i] = strings.Trim(r[i], "<>")
	}
	return r
}

const sendBody = `{"to":["user@example.com"],"subject":"Hi","text":"hello"}`

func TestSendRequiresKey(t *testing.T) {
	s, f := testServer()
	if rec := do(s, "POST", "/mail/send", sendBody, nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("got %d", rec.Code)
	}
	if rec := do(s, "POST", "/mail/send", sendBody, map[string]string{"X-Api-Key": "wrong-key-wrong-key"}); rec.Code != http.StatusUnauthorized {
		t.Fatalf("got %d", rec.Code)
	}
	if len(f.sent) != 0 {
		t.Fatal("mail sent without a valid key")
	}
}

func TestSendDelivers(t *testing.T) {
	s, f := testServer()
	rec := do(s, "POST", "/mail/send", sendBody, map[string]string{"X-Api-Key": "0123456789abcdef"})
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d: %s", rec.Code, rec.Body)
	}
	if got := recipients(t, f.sent[0]); len(got) != 1 || got[0] != "user@example.com" {
		t.Fatalf("recipients %v", got)
	}
}

func TestTestHeaderReroutes(t *testing.T) {
	s, f := testServer()
	rec := do(s, "POST", "/mail/send", sendBody, map[string]string{"X-Api-Key": "0123456789abcdef", TestHeader: ""})
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d: %s", rec.Code, rec.Body)
	}
	if got := recipients(t, f.sent[0]); len(got) != 1 || got[0] != "test@wilhelm.my" {
		t.Fatalf("recipients %v", got)
	}
	subj := f.sent[0].GetGenHeader(gomail.HeaderSubject)
	if len(subj) == 0 || !strings.Contains(subj[0], "user@example.com") {
		t.Fatalf("subject %v should name the original recipient", subj)
	}
}

func TestSendRejectsForeignFrom(t *testing.T) {
	s, _ := testServer()
	body := `{"from":"ceo@bank.com","to":["user@example.com"],"subject":"Hi","text":"hello"}`
	if rec := do(s, "POST", "/mail/send", body, map[string]string{"X-Api-Key": "0123456789abcdef"}); rec.Code != http.StatusBadRequest {
		t.Fatalf("got %d", rec.Code)
	}
}

func TestContactUsesSiteRecipient(t *testing.T) {
	s, f := testServer()
	body := `{"email":"guest@example.com","phone":"0701234567","message":"Boka DJ"}`
	rec := do(s, "POST", "/mail/contact/dj", body, map[string]string{"Origin": "https://djcarldurelius.se"})
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d: %s", rec.Code, rec.Body)
	}
	if got := recipients(t, f.sent[0]); len(got) != 1 || got[0] != "carl@example.com" {
		t.Fatalf("recipients %v", got)
	}
	if rec.Header().Get("Access-Control-Allow-Origin") != "https://djcarldurelius.se" {
		t.Fatal("missing CORS header for allowed origin")
	}
}

func TestContactIgnoresInjectedRecipient(t *testing.T) {
	s, f := testServer()
	body := `{"email":"guest@example.com","to":["victim@example.com"]}`
	if rec := do(s, "POST", "/mail/contact/dj", body, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("got %d", rec.Code)
	}
	if len(f.sent) != 0 {
		t.Fatal("sent mail with an unknown field")
	}
}

func TestContactUnknownSite(t *testing.T) {
	s, _ := testServer()
	if rec := do(s, "POST", "/mail/contact/nope", `{"email":"a@b.se"}`, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("got %d", rec.Code)
	}
}

func TestContactRateLimited(t *testing.T) {
	s, _ := testServer()
	s.limiter = newLimiter(2, time.Minute)
	body := `{"email":"guest@example.com","message":"hi"}`
	codes := []int{}
	for range 3 {
		codes = append(codes, do(s, "POST", "/mail/contact/dj", body, map[string]string{"X-Forwarded-For": "1.2.3.4"}).Code)
	}
	if codes[2] != http.StatusTooManyRequests {
		t.Fatalf("codes %v", codes)
	}
}
