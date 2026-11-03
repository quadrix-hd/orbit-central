package orbit

import (
	"strings"
	"testing"
)

func TestContainerRecipes(t *testing.T) {
	ok := []any{map[string]any{"name": "kuma", "image": "louislam/uptime-kuma:1", "ports": []any{"3001:3001"}, "volumes": []any{"kuma-data:/app/data", "/srv/kuma/backup:/backup:ro"}, "env": []any{"TZ=Europe/Berlin"}}}
	cmds := containerCommands(ok)
	joined := ""
	for _, c := range cmds {
		joined += strings.Join(c.([]string), " ") + "\n"
	}
	if !strings.Contains(joined, "mkdir -p /srv/kuma/backup") || !strings.Contains(joined, "docker run -d --name kuma --restart unless-stopped -p 3001:3001 -v kuma-data:/app/data -v /srv/kuma/backup:/backup:ro -e TZ=Europe/Berlin louislam/uptime-kuma:1") {
		t.Fatal(joined)
	}
	for name, bad := range map[string]map[string]any{
		"shell in image": {"name": "x", "image": "nginx:1;rm -rf /"},
		"no tag":         {"name": "x", "image": "nginx"},
		"host path":      {"name": "x", "image": "nginx:1", "volumes": []any{"/etc:/etc"}},
		"traversal":      {"name": "x", "image": "nginx:1", "volumes": []any{"/srv/../etc:/x"}},
		"port":           {"name": "x", "image": "nginx:1", "ports": []any{"0:80"}},
		"env":            {"name": "x", "image": "nginx:1", "env": []any{"A=$(id)"}},
		"name":           {"name": "X Y", "image": "nginx:1"},
	} {
		if err := guarded(func() { validContainers([]any{bad}) }); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	payload := seedPayloads("vm", "op", "ssh-ed25519 AAAATEST", "id", []any{"docker.io"}, ok...)
	if !strings.Contains(string(payload["user-data"]), `"docker","run"`) || !strings.Contains(string(payload["user-data"]), `["systemctl","enable","--now","docker"]`) {
		t.Fatalf("user-data %s", payload["user-data"])
	}
}
