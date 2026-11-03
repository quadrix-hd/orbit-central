package orbit

import (
	"context"
	"strings"
	"testing"
)

func TestGuestConfigIdentity(t *testing.T) {
	s, f := actionServer(t)
	c := s.conn("test")
	if cfg := s.guestConfig(context.Background(), c, "/nodes/node1/qemu/100", "name", "vm-one"); cfg["digest"] != "v1" {
		t.Fatalf("%v", cfg)
	}
	f.config["name"] = "other"
	err := guarded(func() { s.guestConfig(context.Background(), c, "/nodes/node1/qemu/100", "name", "vm-one") })
	if err == nil || !strings.Contains(err.Error(), "„other“ statt „vm-one“") {
		t.Fatalf("mismatch must name both: %v", err)
	}
}
