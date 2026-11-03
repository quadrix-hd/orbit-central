package orbit

import (
	"bufio"
	"net"
	"strings"
	"testing"
)

// fakeSMTP accepts exactly one plain SMTP session and returns the DATA part.
func fakeSMTP(t *testing.T) (string, int, <-chan string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	out := make(chan string, 1)
	go func() {
		defer ln.Close()
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		r, w := bufio.NewReader(conn), bufio.NewWriter(conn)
		reply := func(s string) { w.WriteString(s + "\r\n"); w.Flush() }
		reply("220 fake ESMTP")
		var data strings.Builder
		inData := false
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			if inData {
				if line == ".\r\n" {
					inData = false
					reply("250 queued")
					continue
				}
				data.WriteString(line)
				continue
			}
			switch cmd := strings.ToUpper(strings.TrimSpace(line)); {
			case strings.HasPrefix(cmd, "EHLO"), strings.HasPrefix(cmd, "HELO"):
				reply("250 fake")
			case strings.HasPrefix(cmd, "MAIL"), strings.HasPrefix(cmd, "RCPT"):
				reply("250 ok")
			case cmd == "DATA":
				inData = true
				reply("354 go")
			case cmd == "QUIT":
				reply("221 bye")
				out <- data.String()
				return
			default:
				reply("250 ok")
			}
		}
	}()
	addr := ln.Addr().(*net.TCPAddr)
	return addr.IP.String(), addr.Port, out
}

func TestSMTPNotification(t *testing.T) {
	s, _ := testServer(t)
	host, port, got := fakeSMTP(t)
	c := alertChannel{ID: "mail", Kind: "smtp", Host: host, Port: port, Security: "none", From: "orbit@example.com", To: "ops@example.com, oncall@example.com", Enabled: true}
	a := doc{"title": "Host offline: pve-02", "message": "Proxmox meldet den Host als offline.", "state": "firing", "severity": "critical", "object": "pve-02", "last_seen": now()}
	if err := s.notify(c, a); err != nil {
		t.Fatal(err)
	}
	mail := <-got
	for _, want := range []string{"From: orbit@example.com", "To: ops@example.com, oncall@example.com", "Subject: [Orbit] KRITISCH: Host offline: pve-02", "Proxmox meldet den Host als offline.", "Objekt: pve-02"} {
		if !strings.Contains(mail, want) {
			t.Fatalf("missing %q in\n%s", want, mail)
		}
	}
	if strings.Contains(strings.Split(mail, "\r\n\r\n")[0], "\n\n") {
		t.Fatal("header injection")
	}
}
