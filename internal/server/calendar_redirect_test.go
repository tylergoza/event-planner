package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// The church calendar hands the public pages over with 302 when it is set
// (CALENDAR_PLAN.md step 5). The events API never moves.
func TestCalendarRedirects(t *testing.T) {
	srv, _ := newConnServer(t, map[string]string{"CALENDAR_URL": "https://cal.example.org", "CALENDAR_TOKEN": "cal_x"})
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

	for path, want := range map[string]string{
		"/":                       "https://cal.example.org/calendar",
		"/calendar":               "https://cal.example.org/calendar",
		"/calendar?month=2099-11": "https://cal.example.org/calendar?month=2099-11",
		"/calendar/7":             "https://cal.example.org/calendar/by-source/events/7",
		"/calendar.ics":           "https://cal.example.org/calendar.ics",
	} {
		resp, err := c.Get(ts.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != want {
			t.Errorf("GET %s: %d %q, want 302 %q", path, resp.StatusCode, resp.Header.Get("Location"), want)
		}
	}

	resp, err := c.Get(ts.URL + "/api/v1/events")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode == http.StatusFound {
		t.Errorf("GET /api/v1/events redirected to %q", resp.Header.Get("Location"))
	}
}

func TestCalendarNotConfiguredServesLocally(t *testing.T) {
	srv, _ := newConnServer(t, nil)
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

	for _, path := range []string{"/", "/calendar"} {
		resp, err := c.Get(ts.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s without calendar: %d, want 200", path, resp.StatusCode)
		}
	}
}
