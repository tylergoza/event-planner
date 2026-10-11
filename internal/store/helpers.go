package store

import (
	"errors"
	"regexp"
	"strconv"
	"strings"
)

// ErrAlreadySignedUp is returned when a user is put on a job twice.
var ErrAlreadySignedUp = errors.New("already signed up for that job")

// Helper is one person signed up for a job. UserID is 0 for someone
// without an account (or whose account was deleted); Name is always set.
type Helper struct {
	ID     int64
	RoleID int64
	UserID int64
	Name   string
	Phone  string
	Notes  string
}

// HelperRole is a job ("Greeters") that needs Slots people.
type HelperRole struct {
	ID       int64
	EventID  int64
	Position int
	Name     string
	Slots    int
	WhenText string
	RoomID   int64 // 0: no particular room
	RoomName string
	Notes    string
	Helpers  []Helper // filled in by ListHelperRoles and GetHelperRole
}

// Filled is how many slots have someone, never more than Slots.
func (r HelperRole) Filled() int { return min(len(r.Helpers), r.Slots) }

// Short is how many more people the job needs (0 when full).
func (r HelperRole) Short() int { return max(r.Slots-len(r.Helpers), 0) }

// Over is how many people are signed up beyond the slots (0 if none).
func (r HelperRole) Over() int { return max(len(r.Helpers)-r.Slots, 0) }

// HasUser reports whether the user is on the job.
func (r HelperRole) HasUser(userID int64) bool {
	for _, h := range r.Helpers {
		if userID != 0 && h.UserID == userID {
			return true
		}
	}
	return false
}

// HelperTally adds up an event's jobs.
type HelperTally struct{ Slots, Filled, ShortJobs int }

// TallyHelperRoles adds up slots, filled slots and jobs still short.
func TallyHelperRoles(roles []HelperRole) HelperTally {
	var t HelperTally
	for _, r := range roles {
		t.Slots += r.Slots
		t.Filled += r.Filled()
		if r.Short() > 0 {
			t.ShortJobs++
		}
	}
	return t
}

const helperRoleCols = `r.id, r.event_id, r.position, r.name, r.slots, r.when_text, COALESCE(r.room_id, 0), COALESCE(er.place_name, ''), r.notes`
const helperRoleFrom = ` FROM helper_roles r LEFT JOIN event_rooms er ON er.id = r.room_id`

func scanHelperRole(sc interface{ Scan(...any) error }) (HelperRole, error) {
	var r HelperRole
	err := sc.Scan(&r.ID, &r.EventID, &r.Position, &r.Name, &r.Slots, &r.WhenText, &r.RoomID, &r.RoomName, &r.Notes)
	return r, err
}

// ListHelperRoles lists an event's jobs in order, each with its Helpers
// (in the order they signed up).
func (s *Store) ListHelperRoles(eventID int64) ([]HelperRole, error) {
	rows, err := s.DB.Query(`SELECT `+helperRoleCols+helperRoleFrom+` WHERE r.event_id = ? ORDER BY r.position, r.id`, eventID)
	if err != nil {
		return nil, err
	}
	var out []HelperRole
	idx := map[int64]int{}
	for rows.Next() {
		r, err := scanHelperRole(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		idx[r.ID] = len(out)
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	hrows, err := s.DB.Query(`SELECT h.id, h.role_id, COALESCE(h.user_id, 0), h.name, h.phone, h.notes
		FROM helpers h JOIN helper_roles r ON r.id = h.role_id WHERE r.event_id = ? ORDER BY h.id`, eventID)
	if err != nil {
		return nil, err
	}
	defer hrows.Close()
	for hrows.Next() {
		var h Helper
		if err := hrows.Scan(&h.ID, &h.RoleID, &h.UserID, &h.Name, &h.Phone, &h.Notes); err != nil {
			return nil, err
		}
		if i, ok := idx[h.RoleID]; ok {
			out[i].Helpers = append(out[i].Helpers, h)
		}
	}
	return out, hrows.Err()
}

// GetHelperRole loads one job with its helpers, or ErrNotFound.
func (s *Store) GetHelperRole(id int64) (*HelperRole, error) {
	r, err := scanHelperRole(s.DB.QueryRow(`SELECT `+helperRoleCols+helperRoleFrom+` WHERE r.id = ?`, id))
	if err != nil {
		return nil, notFound(err)
	}
	all, err := s.ListHelperRoles(r.EventID)
	if err != nil {
		return nil, err
	}
	for _, x := range all {
		if x.ID == id {
			return &x, nil
		}
	}
	return nil, ErrNotFound
}

func (r *HelperRole) normalize() {
	r.Name, r.WhenText, r.Notes = strings.TrimSpace(r.Name), strings.TrimSpace(r.WhenText), strings.TrimSpace(r.Notes)
	if r.Slots < 1 {
		r.Slots = 1
	}
}

// CreateHelperRole adds a job at the end of the event's list.
func (s *Store) CreateHelperRole(r *HelperRole) (int64, error) {
	r.normalize()
	pos, err := s.nextPosition("helper_roles", r.EventID)
	if err != nil {
		return 0, err
	}
	res, err := s.DB.Exec(`INSERT INTO helper_roles (event_id, position, name, slots, when_text, room_id, notes) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		r.EventID, pos, r.Name, r.Slots, r.WhenText, nullInt(r.RoomID), r.Notes)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) UpdateHelperRole(r *HelperRole) error {
	r.normalize()
	_, err := s.DB.Exec(`UPDATE helper_roles SET name = ?, slots = ?, when_text = ?, room_id = ?, notes = ? WHERE id = ?`,
		r.Name, r.Slots, r.WhenText, nullInt(r.RoomID), r.Notes, r.ID)
	return err
}

// DeleteHelperRole removes a job and everyone signed up for it.
func (s *Store) DeleteHelperRole(id int64) error {
	_, err := s.DB.Exec(`DELETE FROM helper_roles WHERE id = ?`, id)
	return err
}

var slotsSuffix = regexp.MustCompile(`(?i)^(.+?)(?:\s+x\s*|\s+-\s*)(\d{1,3})$`)

// ParseHelperRoles reads one job per line: "Greeters", "Greeters x3" or
// "Greeters - 3" (the number is the slots; default 1). Blank lines are
// skipped.
func ParseHelperRoles(text string) []HelperRole {
	var out []HelperRole
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		r := HelperRole{Name: line, Slots: 1}
		if m := slotsSuffix.FindStringSubmatch(line); m != nil {
			if n, _ := strconv.Atoi(m[2]); n >= 1 {
				r.Name, r.Slots = strings.TrimSpace(m[1]), n
			}
		}
		out = append(out, r)
	}
	return out
}

// AddHelperRoles adds the jobs in text (see ParseHelperRoles) and returns
// how many it added.
func (s *Store) AddHelperRoles(eventID int64, text string) (int, error) {
	n := 0
	for _, r := range ParseHelperRoles(text) {
		r.EventID = eventID
		if _, err := s.CreateHelperRole(&r); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// SignUpUser puts a user on a job, copying their display name (or
// username). It returns ErrAlreadySignedUp if they're already on it.
func (s *Store) SignUpUser(roleID, userID int64) (int64, error) {
	u, err := s.GetUser(userID)
	if err != nil {
		return 0, err
	}
	var n int
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM helpers WHERE role_id = ? AND user_id = ?`, roleID, userID).Scan(&n); err != nil {
		return 0, err
	}
	if n > 0 {
		return 0, ErrAlreadySignedUp
	}
	res, err := s.DB.Exec(`INSERT INTO helpers (role_id, user_id, name, phone) VALUES (?, ?, ?, '')`, roleID, userID, u.Name())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// SignUpName puts someone without an account on a job.
func (s *Store) SignUpName(roleID int64, name, phone string) (int64, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return 0, errors.New("a helper needs a name")
	}
	res, err := s.DB.Exec(`INSERT INTO helpers (role_id, name, phone) VALUES (?, ?, ?)`, roleID, name, strings.TrimSpace(phone))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) GetHelper(id int64) (*Helper, error) {
	var h Helper
	err := s.DB.QueryRow(`SELECT id, role_id, COALESCE(user_id, 0), name, phone, notes FROM helpers WHERE id = ?`, id).
		Scan(&h.ID, &h.RoleID, &h.UserID, &h.Name, &h.Phone, &h.Notes)
	if err != nil {
		return nil, notFound(err)
	}
	return &h, nil
}

// HelperEvent is the event a helper's job is in.
func (s *Store) HelperEvent(id int64) (int64, error) {
	var ev int64
	err := s.DB.QueryRow(`SELECT r.event_id FROM helpers h JOIN helper_roles r ON r.id = h.role_id WHERE h.id = ?`, id).Scan(&ev)
	return ev, notFound(err)
}

// UpdateHelper saves phone and notes, and the name unless the row is
// linked to a user (their name follows their account).
func (s *Store) UpdateHelper(h *Helper) error {
	name := strings.TrimSpace(h.Name)
	if h.UserID == 0 && name == "" {
		return errors.New("a helper needs a name")
	}
	_, err := s.DB.Exec(`UPDATE helpers SET name = CASE WHEN user_id IS NULL THEN ? ELSE name END, phone = ?, notes = ? WHERE id = ?`,
		name, strings.TrimSpace(h.Phone), strings.TrimSpace(h.Notes), h.ID)
	return err
}

func (s *Store) DeleteHelper(id int64) error {
	_, err := s.DB.Exec(`DELETE FROM helpers WHERE id = ?`, id)
	return err
}

// RemoveUserFromRole takes a user off a job ("take me off").
func (s *Store) RemoveUserFromRole(roleID, userID int64) error {
	_, err := s.DB.Exec(`DELETE FROM helpers WHERE role_id = ? AND user_id = ?`, roleID, userID)
	return err
}

// HelperAssignment is a job a user is signed up for, with its event.
type HelperAssignment struct {
	EventID    int64
	EventTitle string
	StartsOn   string
	RoleID     int64
	RoleName   string
	WhenText   string
}

// HelperAssignmentsForUser lists the jobs the user is signed up for in
// events that aren't done or cancelled, soonest event first. It does not
// check event visibility: callers showing another person's list must.
func (s *Store) HelperAssignmentsForUser(userID int64) ([]HelperAssignment, error) {
	rows, err := s.DB.Query(`SELECT e.id, e.title, e.starts_on, r.id, r.name, r.when_text
		FROM helpers h JOIN helper_roles r ON r.id = h.role_id JOIN events e ON e.id = r.event_id
		WHERE h.user_id = ? AND e.status NOT IN ('done', 'cancelled')
		ORDER BY e.starts_on, e.id, r.position, r.id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []HelperAssignment
	for rows.Next() {
		var a HelperAssignment
		if err := rows.Scan(&a.EventID, &a.EventTitle, &a.StartsOn, &a.RoleID, &a.RoleName, &a.WhenText); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
