package server

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/tylergoza/event-planner/internal/calendar"
	"github.com/tylergoza/event-planner/internal/store"
)

// The public calendar. Anyone can open it; what they see depends on who
// they are (see calAccess). Signed-in people get the same pages with the
// private events' details and links to each event's internal page.

// isCookieFree is true for the paths that ignore sessions and CSRF
// entirely: the events API and the calendar feed. They set no cookies.
func isCookieFree(path string) bool {
	return path == "/calendar.ics" || strings.HasPrefix(path, "/api/")
}

// isPublicPath is true for every path anyone may open without signing in:
// the calendar pages and feeds, the API (which has its own tokens) and the
// home page (the calendar, when nobody is signed in). None of them are
// wrapped in requireUser.
func isPublicPath(path string) bool {
	return path == "/" || path == "/calendar" || strings.HasPrefix(path, "/calendar/") || isCookieFree(path)
}

func (s *Server) calAccess(r *http.Request) store.CalendarAccess {
	if u := currentUser(r); u != nil {
		return store.CalendarSignedIn(u)
	}
	return store.CalendarPublic
}

// handleHome is the dashboard for people who are signed in and the public
// calendar for everyone else (before the first
// administrator exists, Sign in leads to /setup).
func (s *Server) handleHome(w http.ResponseWriter, r *http.Request) {
	if currentUser(r) != nil {
		w.Header().Set("Cache-Control", "private, no-store")
		s.handleDashboard(w, r)
		return
	}
	if s.calendarRedirect(w, r, func(c *calendar.Client) string { return c.PublicURL + "/calendar" }) {
		return
	}
	s.handleCalendar(w, r)
}

// calendarRedirect sends the visitor to the church calendar app when it is
// configured (CALENDAR_PLAN.md step 5), with 302 so the app can be taken
// out again. It returns false, and writes nothing, when the calendar isn't
// set, so this app works alone.
func (s *Server) calendarRedirect(w http.ResponseWriter, r *http.Request, to func(*calendar.Client) string) bool {
	c := s.Calendar()
	if !c.Configured() {
		return false
	}
	http.Redirect(w, r, to(c), http.StatusFound)
	return true
}

// calItem is one entry on one day.
type calItem struct {
	store.CalendarEntry
	When     string // "2:00–4:00 pm", "All day", "from 5:30 pm"...
	BusyText string // "Lifecenter, Kitchen unavailable · 2:00–4:00 pm"
}

type calDay struct {
	Date    string
	Day     int
	InMonth bool
	Today   bool
	Items   []calItem
}

func (s *Server) handleCalendar(w http.ResponseWriter, r *http.Request) {
	if s.calendarRedirect(w, r, func(c *calendar.Client) string {
		if r.URL.RawQuery != "" {
			return c.PublicURL + "/calendar?" + r.URL.RawQuery
		}
		return c.PublicURL + "/calendar"
	}) {
		return
	}
	today := s.today()
	month, err := time.Parse("2006-01", r.URL.Query().Get("month"))
	if err != nil {
		month, _ = time.Parse("2006-01", today[:7])
	}
	room := strings.TrimSpace(r.URL.Query().Get("room"))
	first := month
	last := first.AddDate(0, 1, -1)
	gridStart := first.AddDate(0, 0, -int(first.Weekday()))
	gridEnd := last.AddDate(0, 0, 6-int(last.Weekday()))

	entries, err := s.store.Calendar(gridStart.Format(store.DateLayout), gridEnd.Format(store.DateLayout), s.calAccess(r))
	if err != nil {
		s.serverError(w, r, err)
		return
	}

	// Rooms in use this month, for the filter.
	seen := map[string]bool{}
	var rooms []string
	for _, e := range entries {
		for _, n := range e.Rooms {
			if !seen[n] {
				seen[n] = true
				rooms = append(rooms, n)
			}
		}
	}
	sort.Strings(rooms)

	days := map[string]*calDay{}
	var grid [][]*calDay
	for d := gridStart; !d.After(gridEnd); d = d.AddDate(0, 0, 1) {
		ds := d.Format(store.DateLayout)
		cd := &calDay{Date: ds, Day: d.Day(), InMonth: d.Month() == first.Month(), Today: ds == today}
		days[ds] = cd
		if d.Weekday() == time.Sunday {
			grid = append(grid, nil)
		}
		grid[len(grid)-1] = append(grid[len(grid)-1], cd)
	}
	for _, e := range entries {
		if room != "" && !hasRoom(e.Rooms, room) {
			continue
		}
		lastDay := e.LastDate()
		start, err := time.Parse(store.DateLayout, e.StartsOn)
		if err != nil {
			continue
		}
		for d := start; ; d = d.AddDate(0, 0, 1) {
			ds := d.Format(store.DateLayout)
			if ds > lastDay || ds > gridEnd.Format(store.DateLayout) {
				break
			}
			if cd := days[ds]; cd != nil {
				cd.Items = append(cd.Items, newCalItem(e, ds))
			}
		}
	}
	var agenda []*calDay
	for _, week := range grid {
		for _, d := range week {
			if d.InMonth && len(d.Items) > 0 {
				agenda = append(agenda, d)
			}
		}
	}

	link := func(m time.Time) string {
		q := url.Values{"month": {m.Format("2006-01")}}
		if room != "" {
			q.Set("room", room)
		}
		return "/calendar?" + q.Encode()
	}
	currentMonth := today[:7] == first.Format("2006-01")
	data := map[string]any{
		"Title":    first.Format("January 2006"),
		"Month":    first.Format("2006-01"),
		"Heading":  first.Format("January 2006"),
		"Grid":     grid,
		"Agenda":   agenda,
		"Rooms":    rooms,
		"Room":     room,
		"PrevURL":  link(first.AddDate(0, -1, 0)),
		"NextURL":  link(first.AddDate(0, 1, 0)),
		"TodayURL": "/calendar",
		"IsToday":  currentMonth,
		"Feed":     s.baseURL(r) + "/calendar.ics",
		"WebcalURL": "webcal://" + strings.TrimPrefix(strings.TrimPrefix(s.baseURL(r), "https://"), "http://") +
			"/calendar.ics",
		"Weekdays": []string{"Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"},
	}
	s.renderCalendarPage(w, r, "calendar/index", data)
}

func (s *Server) handleCalendarEvent(w http.ResponseWriter, r *http.Request) {
	if id := pathID(r); id > 0 && s.calendarRedirect(w, r, func(c *calendar.Client) string { return c.BookingURL(id) }) {
		return
	}
	c, err := s.store.CalendarEvent(pathID(r), s.calAccess(r))
	if errors.Is(err, store.ErrNotFound) {
		s.notFoundFor(w, r)
		return
	} else if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.renderCalendarPage(w, r, "calendar/show", map[string]any{"Title": c.Title, "C": c, "When": whenText(*c)})
}

// notFoundFor renders the 404 page with the public header when nobody is
// signed in.
func (s *Server) notFoundFor(w http.ResponseWriter, r *http.Request) {
	s.renderCalendarPage(w, r, "error", map[string]any{
		"Title": "Not found", "Message": "We couldn't find that event.", "Status": http.StatusNotFound,
	})
}

func (s *Server) renderCalendarPage(w http.ResponseWriter, r *http.Request, page string, data map[string]any) {
	status := http.StatusOK
	if st, ok := data["Status"].(int); ok {
		status = st
	}
	w.Header().Set("Cache-Control", "private, no-store")
	if currentUser(r) == nil {
		data["PublicHeader"] = true
	} else if page == "calendar/index" {
		data["Live"] = true
	}
	s.render(w, r, status, page, data)
}

func hasRoom(rooms []string, room string) bool {
	for _, r := range rooms {
		if r == room {
			return true
		}
	}
	return false
}

// Text ---------------------------------------------------------------------

// clock turns "17:30" into "5:30 pm".
func clock(hm string) string {
	t, err := time.Parse("15:04", hm)
	if err != nil {
		return hm
	}
	return strings.ToLower(t.Format("3:04 pm"))
}

// timeRange is "2:00–4:00 pm", "11:00 am–1:00 pm", "5:30 pm" or "".
func timeRange(start, end string) string {
	if start == "" {
		return ""
	}
	if end == "" || end <= start {
		return clock(start)
	}
	a, b := clock(start), clock(end)
	if strings.HasSuffix(a, b[len(b)-2:]) {
		a = strings.TrimSuffix(a, " "+a[len(a)-2:])
	}
	return a + "–" + b
}

// dayWhen is the time to show for entry e on the day ds: a multi-day event
// starts, continues and ends across its days.
func dayWhen(e store.CalendarEntry, ds string) string {
	if e.StartTime == "" {
		return "All day"
	}
	if e.EndsOn == "" || e.EndsOn == e.StartsOn {
		return timeRange(e.StartTime, e.EndTime)
	}
	switch ds {
	case e.StartsOn:
		return "from " + clock(e.StartTime)
	case e.EndsOn:
		if e.EndTime != "" {
			return "until " + clock(e.EndTime)
		}
		return "All day"
	}
	return "All day"
}

func newCalItem(e store.CalendarEntry, ds string) calItem {
	it := calItem{CalendarEntry: e, When: dayWhen(e, ds)}
	if e.Busy {
		who := "Private event"
		if len(e.Rooms) > 0 {
			who = strings.Join(e.Rooms, ", ") + " unavailable"
		}
		if it.When == "All day" {
			if len(e.Rooms) > 0 {
				it.BusyText = who + " all day"
			} else {
				it.BusyText = who + " · all day"
			}
		} else {
			it.BusyText = who + " · " + it.When
		}
	}
	return it
}

// whenText is the full date and time line for the event page.
func whenText(c store.CalendarEntry) string {
	var b strings.Builder
	b.WriteString(fmtDate(c.StartsOn))
	if c.EndsOn != "" && c.EndsOn != c.StartsOn {
		fmt.Fprintf(&b, " to %s", fmtDate(c.EndsOn))
		if c.StartTime != "" {
			fmt.Fprintf(&b, ", from %s", clock(c.StartTime))
			if c.EndTime != "" {
				fmt.Fprintf(&b, " to %s", clock(c.EndTime))
			}
		}
		return b.String()
	}
	if c.StartTime == "" {
		b.WriteString(" · all day")
	} else {
		b.WriteString(" · " + timeRange(c.StartTime, c.EndTime))
	}
	return b.String()
}
