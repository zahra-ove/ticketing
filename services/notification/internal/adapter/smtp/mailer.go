// Package smtp sends emails over SMTP.
package smtp

import (
	"bytes"
	"context"
	"fmt"
	"mime"
	"net"
	"net/mail"
	"net/smtp"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/zahra-ove/ticketing/pkg/telemetry"
	"github.com/zahra-ove/ticketing/services/notification/internal/app"
)

// Config holds the SMTP settings.
type Config struct {
	Addr string // host:port
	From string
}

// Mailer implements app.Mailer with the standard library SMTP client.
// Local development points it at Mailpit, which needs no authentication.
type Mailer struct {
	cfg    Config
	tracer trace.Tracer
}

var _ app.Mailer = (*Mailer)(nil)

// NewMailer returns a Mailer.
func NewMailer(cfg Config) *Mailer {
	return &Mailer{cfg: cfg, tracer: otel.Tracer("github.com/zahra-ove/ticketing/services/notification/internal/adapter/smtp")}
}

// Send implements app.Mailer.
func (m *Mailer) Send(ctx context.Context, e app.Email) (err error) {
	ctx, span := m.tracer.Start(ctx, "smtp send", trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(attribute.String("server.address", m.cfg.Addr)))
	defer func() { telemetry.End(span, err, nil) }()

	// net/smtp has no context support; bound the whole exchange with a deadline instead.
	deadline := time.Now().Add(10 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	dialer := net.Dialer{Deadline: deadline}
	conn, err := dialer.DialContext(ctx, "tcp", m.cfg.Addr)
	if err != nil {
		return fmt.Errorf("dial smtp: %w", err)
	}
	if err := conn.SetDeadline(deadline); err != nil {
		_ = conn.Close()
		return fmt.Errorf("set smtp deadline: %w", err)
	}

	host, _, _ := net.SplitHostPort(m.cfg.Addr)
	c, err := smtp.NewClient(conn, host)
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("smtp handshake: %w", err)
	}
	defer func() { _ = c.Close() }()

	// The envelope sender is the bare address; the display name belongs in the From header only.
	from, err := mail.ParseAddress(m.cfg.From)
	if err != nil {
		return fmt.Errorf("parse sender %q: %w", m.cfg.From, err)
	}
	if err := c.Mail(from.Address); err != nil {
		return fmt.Errorf("smtp MAIL FROM: %w", err)
	}
	if err := c.Rcpt(e.To); err != nil {
		return fmt.Errorf("smtp RCPT TO: %w", err)
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("smtp DATA: %w", err)
	}
	if _, err := w.Write(m.message(e)); err != nil {
		return fmt.Errorf("write message: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("finish message: %w", err)
	}
	return c.Quit()
}

func (m *Mailer) message(e app.Email) []byte {
	var b bytes.Buffer
	headers := []struct{ k, v string }{
		{"From", m.cfg.From},
		{"To", e.To},
		{"Subject", mime.QEncoding.Encode("utf-8", e.Subject)},
		{"Date", time.Now().UTC().Format(time.RFC1123Z)},
		{"Message-ID", "<" + uuid.NewString() + "@ticketing.local>"},
		{"MIME-Version", "1.0"},
		{"Content-Type", "text/plain; charset=utf-8"},
		{"Content-Transfer-Encoding", "8bit"},
	}
	for _, h := range headers {
		fmt.Fprintf(&b, "%s: %s\r\n", h.k, h.v)
	}
	b.WriteString("\r\n")
	b.WriteString(strings.ReplaceAll(e.Body, "\n", "\r\n"))
	return b.Bytes()
}
