package orbit

import (
	"regexp"
	"strconv"
	"strings"
)

// Container recipes let catalog packages start Docker containers on the
// first boot of a cloud image. Every field is validated against a narrow
// pattern and turned into a fixed docker command line, so a catalog can not
// inject arbitrary shell commands.

var (
	containerNameRE  = regexp.MustCompile(`^[a-z][a-z0-9-]{0,40}$`)
	containerImageRE = regexp.MustCompile(`^[a-z0-9]+(?:[._-][a-z0-9]+)*(?:/[a-z0-9]+(?:[._-][a-z0-9]+)*){0,2}:[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$`)
	containerPortRE  = regexp.MustCompile(`^(\d{1,5}):(\d{1,5})(/(?:tcp|udp))?$`)
	containerVolRE   = regexp.MustCompile(`^(?:[a-z][a-z0-9_.-]{0,40}|/srv/[a-z0-9][a-z0-9._-]{0,59}(?:/[a-z0-9][a-z0-9._-]{0,59}){0,3}|/var/run/docker\.sock):/[A-Za-z0-9_][A-Za-z0-9._-]{0,59}(?:/[A-Za-z0-9_][A-Za-z0-9._-]{0,59}){0,5}(?::ro)?$`)
	containerEnvRE   = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,40}=[A-Za-z0-9_.,:/@+-]{0,200}$`)
)

func stringList(v any, max int, re *regexp.Regexp, what string) []any {
	list, ok := v.([]any)
	if v == nil {
		return []any{}
	}
	if !ok || len(list) > max {
		fail("Ungültige Liste im Container-Rezept: " + what)
	}
	for _, item := range list {
		s, ok := item.(string)
		if !ok || !re.MatchString(s) {
			fail("Ungültiger Eintrag im Container-Rezept (" + what + "): " + str(item))
		}
	}
	return list
}

func validContainers(v any) []any {
	list, ok := v.([]any)
	if !ok || len(list) > 5 {
		fail("Ungültige Container-Liste im Rezept.")
	}
	out := []any{}
	names := map[string]bool{}
	for _, raw := range list {
		c, ok := raw.(map[string]any)
		if !ok {
			fail("Ungültiger Container im Rezept.")
		}
		name, image := str(c["name"]), str(c["image"])
		if !containerNameRE.MatchString(name) || names[name] || !containerImageRE.MatchString(image) {
			fail("Ungültiger Container-Name oder Image.")
		}
		names[name] = true
		ports := stringList(c["ports"], 10, containerPortRE, "ports")
		for _, p := range ports {
			m := containerPortRE.FindStringSubmatch(str(p))
			for _, n := range m[1:3] {
				if v, _ := strconv.Atoi(n); v < 1 || v > 65535 {
					fail("Ungültiger Port im Container-Rezept.")
				}
			}
		}
		out = append(out, doc{"name": name, "image": image, "ports": ports, "volumes": stringList(c["volumes"], 10, containerVolRE, "volumes"), "env": stringList(c["env"], 20, containerEnvRE, "env")})
	}
	return out
}

// containerCommands renders validated recipes as cloud-init runcmd entries.
// Recipes are validated again here because deployments may have been queued
// from an older catalog snapshot.
func containerCommands(raw []any) []any {
	if len(raw) == 0 {
		return nil
	}
	cmds := []any{}
	for _, item := range validContainers(raw) {
		c := item.(doc)
		for _, v := range anyList(c["volumes"]) {
			if host := strings.SplitN(str(v), ":", 2)[0]; strings.HasPrefix(host, "/srv/") {
				cmds = append(cmds, []string{"mkdir", "-p", host})
			}
		}
		args := []string{"docker", "run", "-d", "--name", str(c["name"]), "--restart", "unless-stopped"}
		for _, p := range anyList(c["ports"]) {
			args = append(args, "-p", str(p))
		}
		for _, v := range anyList(c["volumes"]) {
			args = append(args, "-v", str(v))
		}
		for _, e := range anyList(c["env"]) {
			args = append(args, "-e", str(e))
		}
		cmds = append(cmds, append(args, str(c["image"])))
	}
	return cmds
}
