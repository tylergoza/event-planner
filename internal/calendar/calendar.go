// Package calendar sends this app's events to the church calendar
// (../calendar) and asks it which rooms are busy. Every event date is a
// booking there with source "events" and the event's ID as source_id; the
// calendar never edits them. Authorised with a source token (cal_…) made
// on the calendar's API tokens page.
package calendar

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Source is this app's name on the calendar.
const Source = "events"

// ErrNotConfigured means there's no calendar address or token.
var ErrNotConfigured = errors.New("the calendar isn't set up; an admin can add it on Connections")

type Client struct {
	// PublicURL is the address people's browsers use (links, redirects);
	// InternalURL is for server-to-server calls (PublicURL if blank).
	PublicURL   string
	InternalURL string
	Token       string
	HTTP        *http.Client
}

func New(publicURL, internalURL, token string) *Client {
	pub := strings.TrimRight(strings.TrimSpace(publicURL), "/")
	in := strings.TrimRight(strings.TrimSpace(internalURL), "/")
	if in == "" {
		in = pub
	}
	return &Client{PublicURL: pub, InternalURL: in, Token: strings.TrimSpace(token),
		HTTP: &http.Client{Timeout: 10 * time.Second}}
}

func (c *Client) Configured() bool { return c != nil && c.PublicURL != "" && c.Token != "" }

// BookingURL is the calendar's page for one of our events, for redirects
// from this app's old /calendar/{id} links.
func (c *Client) BookingURL(eventID int64) string {
	return c.PublicURL + "/calendar/by-source/" + Source + "/" + strconv.FormatInt(eventID, 10)
}

// JSON shapes ---------------------------------------------------------------

type Room struct {
	PlaceID int64  `json:"place_id,omitempty"` // the tracker's place, 0 for a room typed in by hand
	Name    string `json:"name"`
}

// Booking is what's sent for one event. Locked events go with no title,
// kind or description. Visibility is "public", "private" or "locked";
// Status "confirmed", "tentative" (a pencilled-in date) or "cancelled".
type Booking struct {
	Title       string `json:"title"`
	Kind        string `json:"kind"`
	StartsOn    string `json:"starts_on"`
	StartTime   string `json:"start_time"`
	EndsOn      string `json:"ends_on"`
	EndTime     string `json:"end_time"`
	Rooms       []Room `json:"rooms"`
	Visibility  string `json:"visibility"`
	Description string `json:"description"`
	URL         string `json:"url"`
	Status      string `json:"status"`
	// Replaces is a hand-made calendar booking this one takes over
	// (Make this an event), 0 for none.
	Replaces int64 `json:"replaces,omitempty"`
}

// Block is a booking as the calendar shows it to us: in full, or only its
// rooms and times when Busy (another app's private or locked booking).
type Block struct {
	ID        int64    `json:"id"`
	Source    string   `json:"source"`
	SourceID  string   `json:"source_id"`
	Title     string   `json:"title"`
	StartsOn  string   `json:"starts_on"`
	StartTime string   `json:"start_time"`
	EndsOn    string   `json:"ends_on"`
	EndTime   string   `json:"end_time"`
	Rooms     []string `json:"rooms"`
	Busy      bool     `json:"busy"`
	// Status is "confirmed", "tentative" (pencilled in) or "pending" (a
	// request waiting for approval); all of them hold the rooms. It may be
	// missing on busy blocks.
	Status string `json:"status"`
}

// Ours reports whether the block is one of this app's events.
func (b Block) Ours() bool { return b.Source == Source }

// Result is the calendar's answer to a PUT: the booking's ID there and any
// bookings it overlaps in the same rooms. The calendar never refuses one.
type Result struct {
	ID        int64   `json:"id"`
	Conflicts []Block `json:"conflicts"`
}

// Calls -----------------------------------------------------------------------

// Error is the calendar answering with something other than success.
type Error struct {
	Status  int
	Message string
}

func (e *Error) Error() string {
	switch e.Status {
	case http.StatusUnauthorized:
		return "the calendar turned down the token; make a new source token on its API tokens page"
	case http.StatusForbidden:
		return "the calendar's token isn't for the event planner (source \"events\")"
	}
	if e.Message != "" {
		return fmt.Sprintf("the calendar said: %s (HTTP %d)", e.Message, e.Status)
	}
	return fmt.Sprintf("the calendar answered HTTP %d", e.Status)
}

func (c *Client) do(ctx context.Context, method, path string, q url.Values, body, out any) error {
	if !c.Configured() {
		return ErrNotConfigured
	}
	u := c.InternalURL + "/api/v1" + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("couldn't reach the calendar: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		var e struct {
			Error string `json:"error"`
		}
		json.Unmarshal(raw, &e)
		return &Error{Status: resp.StatusCode, Message: e.Error}
	}
	if out == nil || resp.StatusCode == http.StatusNoContent {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("the calendar sent something unexpected (HTTP %d)", resp.StatusCode)
	}
	return nil
}

func bookingPath(eventID int64) string {
	return "/bookings/" + Source + "/" + strconv.FormatInt(eventID, 10)
}

// PutBooking creates or replaces the booking for an event.
func (c *Client) PutBooking(ctx context.Context, eventID int64, b Booking) (*Result, error) {
	if b.Rooms == nil {
		b.Rooms = []Room{}
	}
	var out Result
	if err := c.do(ctx, http.MethodPut, bookingPath(eventID), nil, b, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// DeleteBooking removes an event's booking. Already gone is fine.
func (c *Client) DeleteBooking(ctx context.Context, eventID int64) error {
	err := c.do(ctx, http.MethodDelete, bookingPath(eventID), nil, nil, nil)
	var e *Error
	if errors.As(err, &e) && e.Status == http.StatusNotFound {
		return nil
	}
	return err
}

// Availability is the busy blocks in rooms (by name) between from and to
// (YYYY-MM-DD, inclusive), from every app.
func (c *Client) Availability(ctx context.Context, rooms []string, from, to string) ([]Block, error) {
	q := url.Values{"rooms": {strings.Join(rooms, ",")}, "from": {from}, "to": {to}}
	var out struct {
		Busy []Block `json:"busy"`
	}
	err := c.do(ctx, http.MethodGet, "/availability", q, nil, &out)
	return out.Busy, err
}

// Check sees that the address and token work.
func (c *Client) Check(ctx context.Context) error {
	var out json.RawMessage
	return c.do(ctx, http.MethodGet, "/rooms", nil, nil, &out)
}
