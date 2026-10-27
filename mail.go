package main

import (
	"context"
	"fmt"
	"net/mail"
	"strings"

	gomail "github.com/wneessen/go-mail"
)

// Mail is the request body for POST /mail/send.
type Mail struct {
	From     string   `json:"from"`
	FromName string   `json:"fromName"`
	To       []string `json:"to"`
	ReplyTo  string   `json:"replyTo"`
	Subject  string   `json:"subject"`
	Text     string   `json:"text"`
	HTML     string   `json:"html"`
}

const maxRecipients = 50

// Sender delivers a built message; swapped for a fake in tests.
type Sender interface {
	Send(ctx context.Context, msg *gomail.Msg) error
}

// SMTPSender hands mail to the local Postfix, which trusts 127.0.0.1 and
// DKIM-signs on the way out, so no SMTP login is needed.
type SMTPSender struct {
	Host string
	Port int
}

func (s SMTPSender) Send(ctx context.Context, msg *gomail.Msg) error {
	client, err := gomail.NewClient(s.Host,
		gomail.WithPort(s.Port),
		gomail.WithTLSPolicy(gomail.NoTLS),
		gomail.WithHELO("mail.wilhelm.my"),
	)
	if err != nil {
		return err
	}
	return client.DialAndSendWithContext(ctx, msg)
}

func validAddress(addr string) bool {
	parsed, err := mail.ParseAddress(addr)
	return err == nil && parsed.Address == addr
}

func (c *Config) fromAllowed(addr string) bool {
	at := strings.LastIndex(addr, "@")
	if at < 0 {
		return false
	}
	domain := strings.ToLower(addr[at+1:])
	for _, d := range c.AllowedFromDomains {
		if domain == d {
			return true
		}
	}
	return false
}

// Validate fills defaults and rejects anything that would make a bad message.
func (c *Config) Validate(m *Mail) error {
	if m.From == "" {
		m.From = c.DefaultFrom
	}
	if !validAddress(m.From) || !c.fromAllowed(m.From) {
		return fmt.Errorf("from must be an address at %s", strings.Join(c.AllowedFromDomains, ", "))
	}
	if len(m.To) == 0 || len(m.To) > maxRecipients {
		return fmt.Errorf("to must have 1-%d addresses", maxRecipients)
	}
	for _, to := range m.To {
		if !validAddress(to) {
			return fmt.Errorf("invalid to address %q", to)
		}
	}
	if m.ReplyTo != "" && !validAddress(m.ReplyTo) {
		return fmt.Errorf("invalid replyTo address")
	}
	if strings.TrimSpace(m.Subject) == "" {
		return fmt.Errorf("subject is required")
	}
	if strings.ContainsAny(m.Subject, "\r\n") {
		return fmt.Errorf("subject must be one line")
	}
	if m.Text == "" && m.HTML == "" {
		return fmt.Errorf("text or html is required")
	}
	return nil
}

// Build turns a validated Mail into a message. In test mode every recipient is
// replaced by the test inbox; the real ones are kept in the subject and an
// X-Original-To header so the test mail shows where it would have gone.
func (c *Config) Build(m Mail, test bool) (*gomail.Msg, error) {
	msg := gomail.NewMsg()
	if err := msg.FromFormat(m.FromName, m.From); err != nil {
		return nil, err
	}
	subject := m.Subject
	to := m.To
	if test {
		subject = fmt.Sprintf("[TEST to %s] %s", strings.Join(m.To, ", "), m.Subject)
		msg.SetGenHeader("X-Original-To", strings.Join(m.To, ", "))
		to = []string{c.TestRecipient}
	}
	if err := msg.To(to...); err != nil {
		return nil, err
	}
	if m.ReplyTo != "" {
		if err := msg.ReplyTo(m.ReplyTo); err != nil {
			return nil, err
		}
	}
	msg.Subject(subject)
	switch {
	case m.HTML != "" && m.Text != "":
		msg.SetBodyString(gomail.TypeTextPlain, m.Text)
		msg.AddAlternativeString(gomail.TypeTextHTML, m.HTML)
	case m.HTML != "":
		msg.SetBodyString(gomail.TypeTextHTML, m.HTML)
	default:
		msg.SetBodyString(gomail.TypeTextPlain, m.Text)
	}
	return msg, nil
}
