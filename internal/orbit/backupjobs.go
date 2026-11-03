package orbit

import (
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// Backup schedules: create, edit and delete Proxmox vzdump jobs
// (/cluster/backup) including retention. Changes are small configuration
// writes, so they are applied directly and audited instead of being queued.

var backupSchedulePattern = regexp.MustCompile(`^[a-z0-9 ,.:*/~-]{1,64}$`)
var backupJobIDPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`)

func backupJobFields(d doc, updating bool) url.Values {
	v := url.Values{}
	schedule := strings.TrimSpace(strings.ToLower(str(d["schedule"])))
	if !backupSchedulePattern.MatchString(schedule) {
		fail("Ungültiger Zeitplan. Beispiele: 01:00, sat 02:00, mon..fri 21:30, *-*-01 03:00")
	}
	v.Set("schedule", schedule)
	v.Set("storage", field(d, "storage", 64, `[a-zA-Z0-9_.-]+`))
	v.Set("mode", field(d, "mode", 10, "snapshot|suspend|stop"))
	v.Set("compress", defaultText(str(d["compress"]), "zstd"))
	if !contains("zstd,lzo,gzip,0", v.Get("compress")) {
		fail("Ungültige Kompression.")
	}
	v.Set("enabled", strconv.Itoa(boolInt(d["enabled"] == true)))
	selection := field(d, "selection", 10, "all|vmids|pool")
	switch selection {
	case "all":
		v.Set("all", "1")
		if ex := strings.TrimSpace(str(d["exclude"])); ex != "" {
			v.Set("exclude", vmidList(ex))
		}
	case "vmids":
		v.Set("vmid", vmidList(str(d["vmids"])))
		if updating {
			v.Set("all", "0")
		}
	case "pool":
		v.Set("pool", field(d, "pool", 64, `[a-zA-Z0-9_.-]+`))
		if updating {
			v.Set("all", "0")
		}
	}
	if node := strings.TrimSpace(str(d["node"])); node != "" {
		v.Set("node", field(d, "node", 64, `[a-zA-Z0-9_.-]+`))
	}
	keep := []string{}
	for _, k := range []string{"keep-last", "keep-hourly", "keep-daily", "keep-weekly", "keep-monthly", "keep-yearly"} {
		if raw, ok := d[k]; ok && raw != nil && str(raw) != "" && num(raw) > 0 {
			n := integer(d, k, 1, 9999)
			keep = append(keep, k+"="+strconv.Itoa(n))
		}
	}
	if len(keep) > 0 {
		v.Set("prune-backups", strings.Join(keep, ","))
	} else if updating {
		v.Set("prune-backups", "keep-all=1")
	}
	if mail := strings.TrimSpace(str(d["mailto"])); mail != "" {
		if len(mail) > 500 || !regexp.MustCompile(`^[^\s@,]+@[^\s@,]+(\s*,\s*[^\s@,]+@[^\s@,]+)*$`).MatchString(mail) {
			fail("Ungültige E-Mail-Adresse(n).")
		}
		v.Set("mailto", mail)
		v.Set("mailnotification", defaultText(str(d["mailnotification"]), "failure"))
		if !contains("always,failure", v.Get("mailnotification")) {
			fail("Ungültige Benachrichtigung.")
		}
	}
	if comment := strings.TrimSpace(str(d["comment"])); comment != "" {
		if len([]rune(comment)) > 200 || strings.ContainsAny(comment, "\r\n") {
			fail("Kommentar zu lang.")
		}
		v.Set("comment", comment)
	}
	return v
}

func vmidList(raw string) string {
	ids := []string{}
	for _, part := range strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == ' ' || r == ';' }) {
		n, err := strconv.Atoi(part)
		if err != nil || n < 100 || n > 999999999 {
			fail("Ungültige VMID: " + part)
		}
		ids = append(ids, strconv.Itoa(n))
	}
	if len(ids) == 0 || len(ids) > 500 {
		fail("Mindestens eine VMID angeben.")
	}
	return strings.Join(ids, ",")
}

// backupJobRoutes handles /api/connections/{id}/backup-jobs[/{job}].
func (s *Server) backupJobRoutes(w http.ResponseWriter, r *http.Request, d doc) (handled bool, err error) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/connections/"), "/")
	if len(parts) < 2 || parts[1] != "backup-jobs" || len(parts) > 3 {
		return false, nil
	}
	err = guarded(func() {
		handled = true
		c := s.conn(parts[0])
		if c.Kind != "pve" {
			fail("Backup-Zeitpläne benötigen Proxmox VE.")
		}
		who := requestUser(r.Context())
		switch {
		case len(parts) == 2 && r.Method == "GET":
			writeJSON(w, 200, s.call(r.Context(), c, "GET", "/cluster/backup", nil))
		case len(parts) == 2 && r.Method == "POST":
			fields := backupJobFields(d, false)
			if id := strings.TrimSpace(str(d["id"])); id != "" {
				if !backupJobIDPattern.MatchString(id) {
					fail("Ungültige Job-ID.")
				}
				fields.Set("id", id)
			}
			must(s.PVE.Call(r.Context(), c, "POST", "/cluster/backup", fields))
			_ = s.Store.AuditAs(who, "backup.job.created", c.ID+" "+fields.Get("schedule")+" → "+fields.Get("storage"))
			writeJSON(w, 201, doc{"ok": true})
		case len(parts) == 3 && (r.Method == "POST" || r.Method == "DELETE"):
			id := parts[2]
			if !backupJobIDPattern.MatchString(id) {
				fail("Ungültige Job-ID.")
			}
			path := "/cluster/backup/" + url.PathEscape(id)
			if r.Method == "DELETE" || d["delete"] == true {
				must(s.PVE.Call(r.Context(), c, "DELETE", path, nil))
				_ = s.Store.AuditAs(who, "backup.job.deleted", c.ID+" "+id)
				writeJSON(w, 200, doc{"ok": true})
				return
			}
			fields := backupJobFields(d, true)
			// Switching between all/vmids/pool must remove the other selectors.
			drop := []string{}
			if fields.Get("vmid") == "" {
				drop = append(drop, "vmid")
			}
			if fields.Get("pool") == "" {
				drop = append(drop, "pool")
			}
			if fields.Get("exclude") == "" {
				drop = append(drop, "exclude")
			}
			if fields.Get("mailto") == "" {
				drop = append(drop, "mailto")
			}
			if fields.Get("node") == "" {
				drop = append(drop, "node")
			}
			if fields.Get("comment") == "" {
				drop = append(drop, "comment")
			}
			if len(drop) > 0 {
				fields.Set("delete", strings.Join(drop, ","))
			}
			must(s.PVE.Call(r.Context(), c, "PUT", path, fields))
			_ = s.Store.AuditAs(who, "backup.job.updated", c.ID+" "+id)
			writeJSON(w, 200, doc{"ok": true})
		default:
			panic(problem("Nicht gefunden.", 404))
		}
	})
	return
}
