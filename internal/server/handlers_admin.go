package server

import (
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tylergoza/event-planner/internal/tracker"
)

// Settings & backup ------------------------------------------------------

func (s *Server) settingsData(data map[string]any) map[string]any {
	data["Title"] = "Settings"
	if _, ok := data["SiteNameValue"]; !ok {
		data["SiteNameValue"] = s.SiteName()
	}
	if _, ok := data["EmbedValue"]; !ok {
		data["EmbedValue"] = strings.Join(s.signupEmbedOrigins(), "\n")
	}
	return data
}

// signupEmbedOrigins are the sites allowed to show sign-up forms in a frame.
func (s *Server) signupEmbedOrigins() []string {
	var out []string
	for _, l := range strings.Split(s.store.Setting("signup_embed_origins", ""), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

// parseOrigins checks one origin per line: http(s)://host[:port], no path,
// query or login. It returns them cleaned and without repeats.
func parseOrigins(text string) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	for _, l := range strings.Split(text, "\n") {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		u, err := url.Parse(l)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" ||
			u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" || strings.Contains(l, " ") {
			return nil, errors.New("\"" + l + "\" isn't a site address like https://church.example (no path)")
		}
		o := u.Scheme + "://" + strings.ToLower(u.Host)
		if !seen[o] {
			seen[o] = true
			out = append(out, o)
		}
	}
	return out, nil
}

func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, http.StatusOK, "settings", s.settingsData(map[string]any{}))
}

// handleSettingsUpdate saves the site name. Connections have their own page.
func (s *Server) handleSettingsUpdate(w http.ResponseWriter, r *http.Request) {
	name := formStr(r, "site_name")
	if name == "" {
		s.render(w, r, http.StatusUnprocessableEntity, "settings", s.settingsData(map[string]any{
			"Errors": []string{"Site name is required."}, "SiteNameValue": name,
		}))
		return
	}
	embed := r.PostFormValue("signup_embed_origins")
	origins, err := parseOrigins(embed)
	if err != nil {
		s.render(w, r, http.StatusUnprocessableEntity, "settings", s.settingsData(map[string]any{
			"Errors": []string{err.Error()}, "SiteNameValue": name, "EmbedValue": embed,
		}))
		return
	}
	if err := s.store.SetSetting("site_name", name); err != nil {
		s.serverError(w, r, err)
		return
	}
	if err := s.store.SetSetting("signup_embed_origins", strings.Join(origins, "\n")); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.reloadSettings()
	s.redirect(w, r, "/admin/settings", "Settings saved.")
}

// normalizeURL checks an address and trims it to "scheme://host[:port][/path]"
// with no trailing slash. Blank is allowed.
func normalizeURL(v string) (string, error) {
	if v == "" {
		return "", nil
	}
	if !strings.Contains(v, "://") {
		v = "https://" + v
	}
	u, err := url.Parse(v)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("should look like https://example.org")
	}
	return u.Scheme + "://" + u.Host + strings.TrimRight(u.Path, "/"), nil
}

// handleBackup streams a consistent snapshot of the database. Restoring is
// just replacing the .db file on the target host while the app is stopped.
func (s *Server) handleBackup(w http.ResponseWriter, r *http.Request) {
	dir, err := os.MkdirTemp("", "pp-backup-")
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	defer os.RemoveAll(dir)
	dest := filepath.Join(dir, "backup.db")
	if err := s.store.Backup(r.Context(), dest); err != nil {
		s.serverError(w, r, err)
		return
	}
	f, err := os.Open(dest)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	defer f.Close()
	name := "events-" + time.Now().Format("2006-01-02-1504") + ".db"
	w.Header().Set("Content-Type", "application/vnd.sqlite3")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	io.Copy(w, f)
}

// trackerErr turns a tracker failure into something to show on a page.
func trackerErr(err error) string {
	if errors.Is(err, tracker.ErrNotConfigured) {
		return err.Error() + "."
	}
	return err.Error()
}
