package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/tylergoza/event-planner/internal/calendar"
	"github.com/tylergoza/event-planner/internal/store"
)

// The events API is read-only and lets other sites and apps (the church
// website, a phone's calendar) read the calendar. It is authorised by a
// token an admin makes on the API tokens page, sent as
// "Authorization: Bearer ep_…". A token sees public events in full and
// every other event as "busy" (its rooms and times); a token allowed
// private events also sees private events' details. Locked events are
// always busy, and notes, tasks, helpers and needs are never included.
//
// These routes ignore sessions and cookies, so they need no CSRF
// protection: routes() sends them past the session and CSRF middleware
// (see isCookieFree), so they never set a cookie.

const (
	maxAPIRangeDays     = 400
	defaultAPIRangeDays = 90
)

// apiRoutes registers the events API, the public calendar feed and the
// admin token pages.
func (s *Server) apiRoutes(mux *http.ServeMux, admin func(string, http.HandlerFunc)) {
	api := func(pattern string, h http.HandlerFunc) { mux.Handle(pattern, s.apiHandler(h)) }
	api("GET /api/v1/events", s.requireToken(s.apiEvents))
	api("GET /api/v1/events/{id}", s.requireToken(s.apiEvent))
	api("GET /api/v1/events.ics", s.requireTokenOrQuery(s.apiICS))
	api("GET /calendar.ics", s.publicICS)
	api("OPTIONS /api/", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	api("OPTIONS /calendar.ics", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	api("/api/", func(w http.ResponseWriter, r *http.Request) { apiError(w, http.StatusNotFound, "no such endpoint") })

	admin("GET /admin/tokens", s.handleTokens)
	admin("POST /admin/tokens", s.handleTokenCreate)
	admin("POST /admin/tokens/{id}/private", s.handleTokenPrivate)
	admin("POST /admin/tokens/{id}/delete", s.handleTokenDelete)
}

// apiHandler adds CORS headers.
func (s *Server) apiHandler(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Access-Control-Allow-Origin", "*")
		h.Set("Access-Control-Allow-Methods", "GET, OPTIONS")
		h.Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
		h.Set("Access-Control-Max-Age", "86400")
		next(w, r)
	}
}

type tokenHandler func(w http.ResponseWriter, r *http.Request, tok *store.APIToken)

func (s *Server) requireToken(next tokenHandler) http.HandlerFunc {
	return s.tokenAuth(false, next)
}

// requireTokenOrQuery also takes ?token=, for the .ics feed: calendar apps
// subscribe by URL and can't send headers.
func (s *Server) requireTokenOrQuery(next tokenHandler) http.HandlerFunc {
	return s.tokenAuth(true, next)
}

func (s *Server) tokenAuth(allowQuery bool, next tokenHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "private, no-store")
		raw, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok && allowQuery {
			raw, ok = r.URL.Query().Get("token"), r.URL.Query().Get("token") != ""
		}
		if !ok {
			w.Header().Set("WWW-Authenticate", `Bearer realm="api"`)
			apiError(w, http.StatusUnauthorized, "send an API token as: Authorization: Bearer <token>")
			return
		}
		tok, err := s.store.AuthenticateToken(strings.TrimSpace(raw))
		if errors.Is(err, store.ErrNotFound) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="api", error="invalid_token"`)
			apiError(w, http.StatusUnauthorized, "that API token isn't valid; it may have been revoked")
			return
		} else if err != nil {
			s.apiServerError(w, r, err)
			return
		}
		next(w, r, tok)
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.Encode(v)
}

func apiError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func (s *Server) apiServerError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, store.ErrNotFound) {
		apiError(w, http.StatusNotFound, "not found")
		return
	}
	s.log.Error("api", "path", r.URL.Path, "err", err)
	apiError(w, http.StatusInternalServerError, "something went wrong")
}

// JSON ---------------------------------------------------------------------

type apiEventJSON struct {
	ID          int64    `json:"id,omitempty"` // omitted when busy
	Busy        bool     `json:"busy"`
	Title       string   `json:"title,omitempty"`
	Kind        string   `json:"kind,omitempty"`
	Status      string   `json:"status,omitempty"`
	StartsOn    string   `json:"starts_on"`
	StartTime   string   `json:"start_time"`
	EndsOn      string   `json:"ends_on"`
	EndTime     string   `json:"end_time"`
	Tentative   bool     `json:"tentative"` // the date is pencilled in, not confirmed
	Rooms       []string `json:"rooms"`
	Description string   `json:"description,omitempty"`
	Public      bool     `json:"public"`
	URL         string   `json:"url,omitempty"` // omitted when busy
}

func (s *Server) apiEventJSON(r *http.Request, c store.CalendarEntry) apiEventJSON {
	e := apiEventJSON{
		ID: c.EventID, Busy: c.Busy, Title: c.Title, Kind: c.Kind, Status: c.Status,
		StartsOn: c.StartsOn, StartTime: c.StartTime, EndsOn: c.EndsOn, EndTime: c.EndTime, Tentative: c.Tentative,
		Rooms: c.Rooms, Description: c.PublicDescription, Public: c.Public,
	}
	if e.Rooms == nil {
		e.Rooms = []string{}
	}
	if !c.Busy {
		e.URL = s.baseURL(r) + "/calendar/" + strconv.FormatInt(c.EventID, 10)
	}
	return e
}

// apiRange reads from and to (default: today, and 90 days later). It
// writes the error and returns false when they are unusable.
func (s *Server) apiRange(w http.ResponseWriter, r *http.Request, asJSON bool) (from, to string, ok bool) {
	fail := func(msg string) (string, string, bool) {
		if asJSON {
			apiError(w, http.StatusBadRequest, msg)
		} else {
			http.Error(w, msg, http.StatusBadRequest)
		}
		return "", "", false
	}
	q := r.URL.Query()
	from = q.Get("from")
	if from == "" {
		from = s.today()
	}
	f, err := time.Parse(store.DateLayout, from)
	if err != nil {
		return fail("from must be a date like 2026-11-01")
	}
	to = q.Get("to")
	if to == "" {
		to = f.AddDate(0, 0, defaultAPIRangeDays).Format(store.DateLayout)
	}
	t, err := time.Parse(store.DateLayout, to)
	if err != nil {
		return fail("to must be a date like 2026-11-01")
	}
	if t.Before(f) {
		return fail("to can't be before from")
	}
	if t.Sub(f) > maxAPIRangeDays*24*time.Hour {
		return fail(fmt.Sprintf("the range can be at most %d days", maxAPIRangeDays))
	}
	return from, to, true
}

func (s *Server) apiEvents(w http.ResponseWriter, r *http.Request, tok *store.APIToken) {
	from, to, ok := s.apiRange(w, r, true)
	if !ok {
		return
	}
	list, err := s.store.Calendar(from, to, tok.Access())
	if err != nil {
		s.apiServerError(w, r, err)
		return
	}
	out := make([]apiEventJSON, 0, len(list))
	for _, c := range list {
		out = append(out, s.apiEventJSON(r, c))
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": out})
}

// apiEvent gives 404 for an event the token can't see in full, so the
// answer doesn't reveal that it exists.
func (s *Server) apiEvent(w http.ResponseWriter, r *http.Request, tok *store.APIToken) {
	c, err := s.store.CalendarEvent(pathID(r), tok.Access())
	if err != nil {
		s.apiServerError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, s.apiEventJSON(r, *c))
}

// iCalendar ------------------------------------------------------------------

func (s *Server) apiICS(w http.ResponseWriter, r *http.Request, tok *store.APIToken) {
	s.writeICS(w, r, tok.Access())
}

// publicICS is the no-token feed: what an anonymous visitor sees.
func (s *Server) publicICS(w http.ResponseWriter, r *http.Request) {
	if s.calendarRedirect(w, r, func(c *calendar.Client) string { return c.PublicURL + "/calendar.ics" }) {
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=300")
	s.writeICS(w, r, store.CalendarPublic)
}

func (s *Server) writeICS(w http.ResponseWriter, r *http.Request, access store.CalendarAccess) {
	from, to, ok := s.apiRange(w, r, false)
	if !ok {
		return
	}
	list, err := s.store.Calendar(from, to, access)
	if err != nil {
		s.log.Error("ics", "err", err)
		http.Error(w, "something went wrong", http.StatusInternalServerError)
		return
	}
	host := r.Host
	if u := s.baseURL(r); strings.Contains(u, "://") {
		host = u[strings.Index(u, "://")+3:]
	}
	w.Header().Set("Content-Type", "text/calendar; charset=utf-8")
	w.Write([]byte(buildICS(s.SiteName(), host, s.baseURL(r), list, time.Now())))
}

// buildICS renders entries as an iCalendar feed. Timed events are written in
// UTC converted from the server's local time zone.
func buildICS(calName, host, baseURL string, list []store.CalendarEntry, now time.Time) string {
	var b strings.Builder
	line := func(s string) { b.WriteString(foldICS(s)) }
	line("BEGIN:VCALENDAR")
	line("VERSION:2.0")
	line("PRODID:-//Event Planner//EN")
	line("CALSCALE:GREGORIAN")
	line("X-WR-CALNAME:" + escapeICS(calName))
	stamp := now.UTC().Format("20060102T150405Z")
	for _, c := range list {
		line("BEGIN:VEVENT")
		if c.Busy {
			sum := sha256.Sum256([]byte(strings.Join([]string{c.StartsOn, c.StartTime, c.EndsOn, c.EndTime, strings.Join(c.Rooms, "\x00")}, "|")))
			line("UID:busy-" + hex.EncodeToString(sum[:8]) + "@" + host)
		} else {
			line("UID:event-" + strconv.FormatInt(c.EventID, 10) + "@" + host)
		}
		line("DTSTAMP:" + stamp)
		icsTimes(line, c)
		if c.Tentative {
			line("STATUS:TENTATIVE")
		}
		if c.Busy {
			if len(c.Rooms) > 0 {
				line("SUMMARY:" + escapeICS("Busy: "+strings.Join(c.Rooms, ", ")))
			} else {
				line("SUMMARY:Private event")
			}
			line("TRANSP:OPAQUE")
		} else {
			line("SUMMARY:" + escapeICS(c.Title))
			if len(c.Rooms) > 0 {
				line("LOCATION:" + escapeICS(strings.Join(c.Rooms, ", ")))
			}
			if c.PublicDescription != "" {
				line("DESCRIPTION:" + escapeICS(c.PublicDescription))
			}
			line("URL:" + baseURL + "/calendar/" + strconv.FormatInt(c.EventID, 10))
		}
		line("END:VEVENT")
	}
	line("END:VCALENDAR")
	return b.String()
}

func icsTimes(line func(string), c store.CalendarEntry) {
	const day = "20060102"
	start, err := time.ParseInLocation(store.DateLayout, c.StartsOn, time.Local)
	if err != nil {
		return
	}
	last, err := time.ParseInLocation(store.DateLayout, c.LastDate(), time.Local)
	if err != nil || last.Before(start) {
		last = start
	}
	if c.StartTime == "" {
		line("DTSTART;VALUE=DATE:" + start.Format(day))
		line("DTEND;VALUE=DATE:" + last.AddDate(0, 0, 1).Format(day)) // exclusive
		return
	}
	const utc = "20060102T150405Z"
	st, err := time.ParseInLocation(store.DateLayout+" 15:04", c.StartsOn+" "+c.StartTime, time.Local)
	if err != nil {
		line("DTSTART;VALUE=DATE:" + start.Format(day))
		line("DTEND;VALUE=DATE:" + last.AddDate(0, 0, 1).Format(day))
		return
	}
	line("DTSTART:" + st.UTC().Format(utc))
	if c.EndTime != "" {
		if en, err := time.ParseInLocation(store.DateLayout+" 15:04", c.LastDate()+" "+c.EndTime, time.Local); err == nil && en.After(st) {
			line("DTEND:" + en.UTC().Format(utc))
		}
	}
}

// escapeICS escapes a TEXT value: backslash, comma, semicolon, newline.
func escapeICS(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	return strings.NewReplacer(`\`, `\\`, `,`, `\,`, `;`, `\;`, "\n", `\n`).Replace(s)
}

// foldICS ends the line with CRLF, folding it so no line is over 75 octets
// (continuations start with a space) and never splitting a character.
func foldICS(s string) string {
	var b strings.Builder
	limit := 75
	n := 0
	for _, r := range s {
		size := utf8.RuneLen(r)
		if n+size > limit {
			b.WriteString("\r\n ")
			n, limit = 0, 74
		}
		b.WriteRune(r)
		n += size
	}
	b.WriteString("\r\n")
	return b.String()
}
