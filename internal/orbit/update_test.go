package orbit

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestUpdateUsesGoBackupAndRollsBackOnFailure(t *testing.T) {
	for _, failure := range []bool{false, true} {
		name := "success"
		if failure {
			name = "rollback"
		}
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			_ = os.MkdirAll(filepath.Join(root, "scripts"), 0700)
			_ = os.MkdirAll(filepath.Join(root, "bin"), 0700)
			source := must(os.ReadFile("../../scripts/update.sh"))
			_ = os.WriteFile(filepath.Join(root, "scripts", "update.sh"), source, 0700)
			_ = os.WriteFile(filepath.Join(root, "compose.go.yaml"), []byte("name: orbit-go-preview"), 0600)
			docker := `#!/usr/bin/env bash
set -eu
printf '%s\n' "$*" >> "$DOCKER_LOG"
case "$*" in
 'compose config --images') echo orbit-console:local ;;
 'ps -aq --filter label=com.docker.compose.project=orbit-go-preview --filter label=com.docker.compose.service=orbit-go') echo preview-container ;;
 'compose ps -a -q orbit') echo old-container ;;
 'inspect --format {{.Image}} old-container') echo sha256:old ;;
 'compose run --rm -T --no-deps --entrypoint orbit orbit backup') echo /data/backups/orbit-test.tar.gz ;;
 'compose cp orbit:/data/backups/orbit-test.tar.gz backups/orbit-test.tar.gz') printf backup > backups/orbit-test.tar.gz ;;
 'compose up -d --no-build --wait --wait-timeout 120') if [[ ${FAIL_HEALTH:-0} == 1 && ! -f "$ROLLBACK_MARK" ]]; then exit 1; fi ;;
 'image tag orbit-console:rollback-'*' orbit-console:local') touch "$ROLLBACK_MARK" ;;
esac
`
			_ = os.WriteFile(filepath.Join(root, "bin", "docker"), []byte(docker), 0700)
			cmd := exec.Command("bash", filepath.Join(root, "scripts", "update.sh"))
			cmd.Dir = root
			cmd.Env = append(os.Environ(), "PATH="+filepath.Join(root, "bin")+":"+os.Getenv("PATH"), "DOCKER_LOG="+filepath.Join(root, "commands"), "ROLLBACK_MARK="+filepath.Join(root, "rollback"), "ORBIT_IMAGE=")
			if failure {
				cmd.Env = append(cmd.Env, "FAIL_HEALTH=1")
			}
			out, err := cmd.CombinedOutput()
			if (err != nil) != failure {
				t.Fatalf("result %v %s", err, out)
			}
			log := string(must(os.ReadFile(filepath.Join(root, "commands"))))
			if strings.Contains(log, "python") {
				t.Fatal("Python required by updater")
			}
			stop := strings.Index(log, "compose stop orbit")
			backup := strings.Index(log, "entrypoint orbit orbit backup")
			if stop < 0 || backup < stop {
				t.Fatal("snapshot before stopping worker", log)
			}
			if failure {
				restore := strings.Index(log, "entrypoint orbit orbit restore /data/backups/orbit-test.tar.gz --keep-maintenance")
				retag := strings.LastIndex(log, "image tag orbit-console:rollback-")
				if restore < 0 || retag < restore {
					t.Fatal("invalid rollback order", log)
				}
			} else if !strings.Contains(log, "rm preview-container") {
				t.Fatal("preview not stopped")
			}
		})
	}
}
