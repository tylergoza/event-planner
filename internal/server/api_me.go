package server

import (
	"net/http"
	"strconv"
	"time"

	"github.com/tylergoza/event-planner/internal/store"
	"github.com/tylergoza/event-planner/internal/umtoken"
)

// The per-person read API, /api/v1/me/…, for the church MCP server (and
// later the website's members area, MEMBERS_PLAN.md). The gate (token
// check, local user) is me_auth.go; the shapes are APP_CONTRACTS.md in
// ../mcp_server (section 3.3).
//
// Everything here is what the person sees on the web pages when signed in:
//   - /me/events is the calendar page (/calendar) for that user
//     (store.CalendarSignedIn): full details of the events they can see,
//     locked events they aren't on as busy blocks. Only the public
//     description; never notes, tasks, helpers, needs or members.
//   - /me/signups/open is the Sign up buttons they can use: forms that are
//     open or full on events they can see (inAudience, as /signup/{code}).
//     Only spots-free facts; nobody else's sign-ups.
//   - /me/signups is /my/signups: only their own, with the names of the
//     people on each. No allergies, answers, phone or email.
//
// GET only, nothing is written apart from the local user (as at sign-in).

// meMaxItems bounds every list (the contract's cap).
const meMaxItems = 100

func (s *Server) meRoutes(mux *http.ServeMux) {
	s.meRoute(mux, "GET /api/v1/me/events", s.meEvents)
	s.meRoute(mux, "GET /api/v1/me/signups/open", s.meOpenSignups)
	s.meRoute(mux, "GET /api/v1/me/signups", s.meSignups)
	s.meCatchAll(mux)
}

// meUMConfig is the live single sign-on connection; the /me API is off
// without it.
func (s *Server) meUMConfig() (umtoken.Config, bool) {
	c := s.SSO()
	if !c.Enabled() || c.ClientID == "" {
		return umtoken.Config{}, false
	}
	return umtoken.Config{URL: c.InternalURL, ClientID: c.ClientID, ClientSecret: c.ClientSecret}, true
}

// meLocalUser is what the SSO callback and grant checks do (sso.go).
func (s *Server) meLocalUser(u umtoken.User) (*store.User, error) {
	return s.store.UpsertSSOUser(u.Sub, u.Username, u.Name, store.RoleFromSSO(u.Role))
}

// meLimit is ?limit= within 1..meMaxItems, meMaxItems when not given.
func meLimit(r *http.Request) int {
	n, err := strconv.Atoi(r.URL.Query().Get("limit"))
	if err != nil || n <= 0 || n > meMaxItems {
		return meMaxItems
	}
	return n
}

// meEvents is the calendar page as JSON, in the /api/v1/events shape.
func (s *Server) meEvents(w http.ResponseWriter, r *http.Request) {
	from, to, ok := s.apiRange(w, r, true)
	if !ok {
		return
	}
	list, err := s.store.Calendar(from, to, store.CalendarSignedIn(meUser(r)))
	if err != nil {
		s.apiServerError(w, r, err)
		return
	}
	limit := meLimit(r)
	out := make([]apiEventJSON, 0, min(len(list), limit))
	for _, c := range list {
		if len(out) == limit {
			break
		}
		out = append(out, s.apiEventJSON(r, c))
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": out})
}

type meFormJSON struct {
	Code     string `json:"code"`
	Label    string `json:"label"`
	State    string `json:"state"`     // open or full
	ClosesAt string `json:"closes_at"` // YYYY-MM-DD
	URL      string `json:"url"`
}

type meOpenJSON struct {
	EventID           int64      `json:"event_id"`
	Title             string     `json:"title"`
	StartsOn          string     `json:"starts_on"`
	StartTime         string     `json:"start_time"`
	EndsOn            string     `json:"ends_on"`
	EndTime           string     `json:"end_time"`
	Rooms             []string   `json:"rooms"`
	PublicDescription string     `json:"public_description"`
	Form              meFormJSON `json:"form"`
	SignedUp          int        `json:"signed_up"` // this person's sign-ups that are in
}

// meOpenSignups lists the forms the person could use now (open or full),
// soonest first, on events they can see in full.
func (s *Server) meOpenSignups(w http.ResponseWriter, r *http.Request) {
	u := meUser(r)
	forms, err := s.store.OpenSignupForms(s.today())
	if err != nil {
		s.apiServerError(w, r, err)
		return
	}
	mine, err := s.store.SignupsByUser(u.ID, s.today())
	if err != nil {
		s.apiServerError(w, r, err)
		return
	}
	in := map[int64]int{}
	for _, su := range mine {
		if su.Status == "in" {
			in[su.EventID]++
		}
	}
	now := time.Now()
	limit := meLimit(r)
	out := []meOpenJSON{}
	for i := range forms {
		if len(out) == limit {
			break
		}
		f := &forms[i]
		ev, err := s.store.GetEvent(f.EventID)
		if err != nil {
			s.apiServerError(w, r, err)
			return
		}
		if ok, err := s.inAudience(f, ev, u); err != nil {
			s.apiServerError(w, r, err)
			return
		} else if !ok {
			continue
		}
		state, err := s.store.SignupState(f, ev, now)
		if err != nil {
			s.apiServerError(w, r, err)
			return
		}
		if state != store.SignupOpen && state != store.SignupFull {
			continue
		}
		// What the calendar page shows of it (the same visibility again).
		c, err := s.store.CalendarEvent(ev.ID, store.CalendarSignedIn(u))
		if err != nil {
			continue
		}
		closes := ev.StartsOn // closes when the event starts
		if len(f.ClosesAt) >= 10 {
			closes = f.ClosesAt[:10]
		}
		rooms := c.Rooms
		if rooms == nil {
			rooms = []string{}
		}
		out = append(out, meOpenJSON{EventID: ev.ID, Title: c.Title, StartsOn: c.StartsOn, StartTime: c.StartTime,
			EndsOn: c.EndsOn, EndTime: c.EndTime, Rooms: rooms, PublicDescription: c.PublicDescription,
			Form: meFormJSON{Code: f.Code, Label: f.ButtonLabel(), State: state, ClosesAt: closes,
				URL: s.baseURL(r) + "/signup/" + f.Code},
			SignedUp: in[ev.ID]})
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": out})
}

type meSignupJSON struct {
	ID         int64    `json:"id"`
	EventID    int64    `json:"event_id"`
	EventTitle string   `json:"event_title"`
	StartsOn   string   `json:"starts_on"`
	StartTime  string   `json:"start_time"`
	FormLabel  string   `json:"form_label"`
	Status     string   `json:"status"` // in or withdrawn
	People     []string `json:"people"`
	Guests     int      `json:"guests"`
	CanChange  bool     `json:"can_change"`
	URL        string   `json:"url"` // the person's own page for it
}

// meSignups is /my/signups as JSON: the person's own sign-ups, upcoming
// first, then not yet scheduled, then past ones.
func (s *Server) meSignups(w http.ResponseWriter, r *http.Request) {
	u := meUser(r)
	list, err := s.store.SignupsByUser(u.ID, s.today())
	if err != nil {
		s.apiServerError(w, r, err)
		return
	}
	if limit := meLimit(r); len(list) > limit {
		list = list[:limit]
	}
	ids := make([]int64, len(list))
	for i, su := range list {
		ids[i] = su.ID
	}
	names, err := s.store.SignupPeopleNames(ids)
	if err != nil {
		s.apiServerError(w, r, err)
		return
	}
	type formInfo struct {
		label, state string
	}
	forms := map[int64]formInfo{}
	now := time.Now()
	out := make([]meSignupJSON, 0, len(list))
	for _, su := range list {
		fi, ok := forms[su.EventID]
		if !ok {
			f, err := s.store.SignupFormByEvent(su.EventID)
			if err != nil {
				s.apiServerError(w, r, err)
				return
			}
			ev, err := s.store.GetEvent(su.EventID)
			if err != nil {
				s.apiServerError(w, r, err)
				return
			}
			state, err := s.store.SignupState(f, ev, now)
			if err != nil {
				s.apiServerError(w, r, err)
				return
			}
			fi = formInfo{label: f.ButtonLabel(), state: state}
			forms[su.EventID] = fi
		}
		people := names[su.ID]
		if people == nil {
			people = []string{}
		}
		out = append(out, meSignupJSON{ID: su.ID, EventID: su.EventID, EventTitle: su.EventTitle,
			StartsOn: su.StartsOn, StartTime: su.StartTime, FormLabel: fi.label, Status: su.Status,
			People: people, Guests: su.Guests,
			// As suCtx.canChange: still in, and the form open or full.
			CanChange: su.Status == "in" && (fi.state == store.SignupOpen || fi.state == store.SignupFull),
			URL:       s.baseURL(r) + "/my/signups/" + strconv.FormatInt(su.ID, 10)})
	}
	writeJSON(w, http.StatusOK, map[string]any{"signups": out})
}
