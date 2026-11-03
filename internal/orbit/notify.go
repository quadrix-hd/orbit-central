package orbit

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime"
	"net"
	"net/http"
	"net/smtp"
	"strconv"
	"strings"
	"time"
)

var notifyClient = &http.Client{Timeout: 10 * time.Second}

func alertHeadline(a doc) (string, string) {
	if a["state"] == "resolved" {
		return "BEHOBEN", "Behoben: " + str(a["title"])
	}
	level := "WARNUNG"
	if a["severity"] == "critical" {
		level = "KRITISCH"
	}
	return level, level + ": " + str(a["title"])
}

func (s *Server) alertText(a doc) string {
	_, headline := alertHeadline(a)
	conn := str(a["connection_id"])
	if c, err := s.Store.Connection(conn); err == nil {
		conn = c.Name
	}
	lines := []string{headline, str(a["message"]), "Cluster: " + conn, "Objekt: " + str(a["object"]), "Zeit: " + time.Unix(int64(num(a["last_seen"])), 0).Format("02.01.2006 15:04:05")}
	if s.Origin != "" {
		lines = append(lines, "Orbit: "+s.Origin)
	}
	return strings.Join(lines, "\n")
}

func webhookPayload(format, text string, a doc) (string, []byte) {
	_, headline := alertHeadline(a)
	switch format {
	case "teams":
		card := doc{"type": "message", "attachments": []any{doc{"contentType": "application/vnd.microsoft.card.adaptive", "content": doc{
			"$schema": "http://adaptivecards.io/schemas/adaptive-card.json", "type": "AdaptiveCard", "version": "1.4",
			"body": []any{
				doc{"type": "TextBlock", "text": headline, "weight": "Bolder", "size": "Medium", "wrap": true, "color": map[bool]string{true: "Attention", false: "Good"}[a["state"] != "resolved"]},
				doc{"type": "TextBlock", "text": strings.Join(strings.Split(text, "\n")[1:], "\n\n"), "wrap": true},
			}}}}}
		return "application/json", must(json.Marshal(card))
	case "slack":
		return "application/json", must(json.Marshal(doc{"text": text}))
	case "discord":
		if len(text) > 1900 {
			text = text[:1900]
		}
		return "application/json", must(json.Marshal(doc{"content": text}))
	case "ntfy":
		return "text/plain; charset=utf-8", []byte(text)
	}
	return "application/json", must(json.Marshal(doc{"source": "orbit-central", "state": a["state"], "severity": a["severity"], "rule": a["rule"], "title": a["title"], "message": a["message"], "object": a["object"], "connection_id": a["connection_id"], "first_seen": a["first_seen"], "last_seen": a["last_seen"], "id": a["id"]}))
}

func (s *Server) sendWebhook(ctx context.Context, c alertChannel, a doc) error {
	target, err := s.Store.Decrypt(c.URL)
	if err != nil {
		return fmt.Errorf("Webhook-URL kann nicht entschlüsselt werden")
	}
	contentType, body := webhookPayload(c.Format, s.alertText(a), a)
	req, err := http.NewRequestWithContext(ctx, "POST", target, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("ungültige Webhook-URL")
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("User-Agent", "Orbit-Central/"+Version)
	if c.Format == "ntfy" {
		level, _ := alertHeadline(a)
		req.Header.Set("Title", "Orbit "+level)
		req.Header.Set("Priority", map[bool]string{true: "high", false: "default"}[a["severity"] == "critical" && a["state"] != "resolved"])
	}
	resp, err := notifyClient.Do(req)
	if err != nil {
		return fmt.Errorf("Webhook nicht erreichbar")
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64*1024))
	if resp.StatusCode >= 300 {
		return fmt.Errorf("Webhook antwortet mit HTTP %d", resp.StatusCode)
	}
	return nil
}

func headerSafe(v string) string {
	return strings.NewReplacer("\r", " ", "\n", " ").Replace(v)
}

func (s *Server) sendMail(c alertChannel, a doc) error {
	_, headline := alertHeadline(a)
	recipients := []string{}
	for _, r := range strings.Split(c.To, ",") {
		if r = strings.TrimSpace(r); r != "" {
			recipients = append(recipients, r)
		}
	}
	msg := &bytes.Buffer{}
	fmt.Fprintf(msg, "From: %s\r\nTo: %s\r\nSubject: %s\r\nDate: %s\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: 8bit\r\n\r\n",
		headerSafe(c.From), headerSafe(strings.Join(recipients, ", ")), mime.QEncoding.Encode("utf-8", "[Orbit] "+headerSafe(headline)), time.Now().Format(time.RFC1123Z))
	msg.WriteString(strings.ReplaceAll(s.alertText(a), "\n", "\r\n"))
	address := net.JoinHostPort(c.Host, strconv.Itoa(c.Port))
	tlsConfig := &tls.Config{ServerName: c.Host, MinVersion: tls.VersionTLS12}
	var conn net.Conn
	var err error
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	if c.Security == "tls" {
		conn, err = tls.DialWithDialer(dialer, "tcp", address, tlsConfig)
	} else {
		conn, err = dialer.Dial("tcp", address)
	}
	if err != nil {
		return fmt.Errorf("SMTP-Server nicht erreichbar")
	}
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
	client, err := smtp.NewClient(conn, c.Host)
	if err != nil {
		conn.Close()
		return fmt.Errorf("SMTP-Begrüßung fehlgeschlagen")
	}
	defer client.Close()
	if c.Security == "starttls" {
		if err = client.StartTLS(tlsConfig); err != nil {
			return fmt.Errorf("STARTTLS fehlgeschlagen")
		}
	}
	if c.Username != "" {
		password, e := s.Store.Decrypt(c.Password)
		if e != nil {
			return fmt.Errorf("SMTP-Passwort kann nicht entschlüsselt werden")
		}
		if err = client.Auth(smtp.PlainAuth("", c.Username, password, c.Host)); err != nil {
			return fmt.Errorf("SMTP-Anmeldung abgelehnt")
		}
	}
	if err = client.Mail(c.From); err != nil {
		return fmt.Errorf("Absender abgelehnt")
	}
	for _, r := range recipients {
		if err = client.Rcpt(r); err != nil {
			return fmt.Errorf("Empfänger %s abgelehnt", r)
		}
	}
	w, err := client.Data()
	if err != nil {
		return fmt.Errorf("SMTP-DATA abgelehnt")
	}
	if _, err = w.Write(msg.Bytes()); err != nil {
		return fmt.Errorf("Nachricht konnte nicht übertragen werden")
	}
	if err = w.Close(); err != nil {
		return fmt.Errorf("Nachricht vom Server abgelehnt")
	}
	return client.Quit()
}

func (s *Server) notify(c alertChannel, a doc) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var err error
	if c.Kind == "smtp" {
		err = s.sendMail(c, a)
	} else {
		err = s.sendWebhook(ctx, c, a)
	}
	status := doc{"at": now(), "ok": err == nil}
	if err != nil {
		status["error"] = err.Error()
		log.Printf("alert_notification_failed channel=%s", c.ID)
	}
	s.Alerting.mu.Lock()
	s.Alerting.status[c.ID] = status
	s.Alerting.mu.Unlock()
	return err
}

func (s *Server) notifyAll(cfg alertConfig, a doc) {
	for _, c := range cfg.Channels {
		if !c.Enabled || (a["state"] == "resolved" && !c.Resolved) {
			continue
		}
		if c.MinLevel == "critical" && a["severity"] != "critical" {
			continue
		}
		_ = s.notify(c, a)
	}
}
