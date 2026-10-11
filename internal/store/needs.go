package store

import "strings"

// Need statuses: still to get, gathered for the event, and put back
// afterwards.
var NeedStatuses = []string{"needed", "gathered", "returned"}

var needStatusLabels = map[string]string{"needed": "Needed", "gathered": "Gathered", "returned": "Put back"}

func NeedStatusLabel(s string) string { return needStatusLabels[s] }

func ValidNeedStatus(s string) bool { _, ok := needStatusLabels[s]; return ok }

// Need is an item or supply the event needs. TrackerKind and
// TrackerID point at a product or supply in the maintenance tracker;
// without them it's something to buy or borrow.
type Need struct {
	ID          int64
	EventID     int64
	Name        string
	Quantity    int
	RoomID      int64 // where it goes; 0 for no particular room
	RoomName    string
	TrackerKind string // "", "product" or "supply"
	TrackerID   int64
	Status      string
	Notes       string
	UpdatedBy   string
	UpdatedAt   string
}

func (n Need) FromTracker() bool { return n.TrackerKind != "" }

const needCols = `n.id, n.event_id, n.name, n.quantity, COALESCE(n.room_id, 0), COALESCE(r.place_name, ''), n.tracker_kind,
	COALESCE(n.tracker_id, 0), n.status, n.notes, n.updated_by, n.updated_at`

const needFrom = ` FROM needs n LEFT JOIN event_rooms r ON r.id = n.room_id`

func scanNeed(sc interface{ Scan(...any) error }) (Need, error) {
	var n Need
	err := sc.Scan(&n.ID, &n.EventID, &n.Name, &n.Quantity, &n.RoomID, &n.RoomName, &n.TrackerKind, &n.TrackerID, &n.Status, &n.Notes, &n.UpdatedBy, &n.UpdatedAt)
	return n, err
}

func (s *Store) ListNeeds(eventID int64) ([]Need, error) {
	rows, err := s.DB.Query(`SELECT `+needCols+needFrom+` WHERE n.event_id = ?
		ORDER BY CASE n.status WHEN 'needed' THEN 0 WHEN 'gathered' THEN 1 ELSE 2 END, n.name COLLATE NOCASE`, eventID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Need
	for rows.Next() {
		n, err := scanNeed(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func (s *Store) GetNeed(id int64) (*Need, error) {
	n, err := scanNeed(s.DB.QueryRow(`SELECT `+needCols+needFrom+` WHERE n.id = ?`, id))
	if err != nil {
		return nil, notFound(err)
	}
	return &n, nil
}

func (s *Store) CreateNeed(n *Need) (int64, error) {
	if n.Status == "" {
		n.Status = "needed"
	}
	if n.Quantity < 1 {
		n.Quantity = 1
	}
	res, err := s.DB.Exec(`INSERT INTO needs (event_id, name, quantity, room_id, tracker_kind, tracker_id, status, notes, updated_by)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		n.EventID, strings.TrimSpace(n.Name), n.Quantity, nullInt(n.RoomID), n.TrackerKind, nullInt(n.TrackerID), n.Status, n.Notes, n.UpdatedBy)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// UpdateNeed saves the editable fields. What it links to in the tracker
// stays as it was.
func (s *Store) UpdateNeed(n *Need) error {
	_, err := s.DB.Exec(`UPDATE needs SET name = ?, quantity = ?, room_id = ?, status = ?, notes = ?, updated_by = ?, updated_at = datetime('now')
		WHERE id = ?`, strings.TrimSpace(n.Name), n.Quantity, nullInt(n.RoomID), n.Status, n.Notes, n.UpdatedBy, n.ID)
	return err
}

func (s *Store) SetNeedStatus(id int64, status, by string) error {
	_, err := s.DB.Exec(`UPDATE needs SET status = ?, updated_by = ?, updated_at = datetime('now') WHERE id = ?`, status, by, id)
	return err
}

func (s *Store) DeleteNeed(id int64) error {
	_, err := s.DB.Exec(`DELETE FROM needs WHERE id = ?`, id)
	return err
}
