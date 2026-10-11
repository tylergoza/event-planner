package store

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// apiTokenPrefix starts every API token, so one pasted somewhere it
// shouldn't be is easy to spot.
const apiTokenPrefix = "ep_"

// APIToken lets another site or app read events. The token itself is only
// known when it's made; afterwards it's Prefix, its first characters.
type APIToken struct {
	ID             int64
	Name           string // what uses it, e.g. "Church website"
	Prefix         string
	IncludePrivate bool // may see private events' details (never locked ones)
	CreatedBy      string
	CreatedAt      string
	LastUsedAt     string // "" if never
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// CreateToken makes a token for the app named name and returns it. This is
// the only time the token can be seen.
func (s *Store) CreateToken(name string, includePrivate bool, createdBy int64) (string, error) {
	token := apiTokenPrefix + RandomToken()
	_, err := s.DB.Exec(`INSERT INTO api_tokens (name, token_hash, prefix, include_private, created_by) VALUES (?, ?, ?, ?, ?)`,
		strings.TrimSpace(name), hashToken(token), token[:len(apiTokenPrefix)+6], includePrivate, nullInt(createdBy))
	return token, err
}

func (s *Store) ListTokens() ([]APIToken, error) {
	rows, err := s.DB.Query(`
		SELECT t.id, t.name, t.prefix, t.include_private, COALESCE(NULLIF(u.display_name, ''), u.username, ''),
		       t.created_at, COALESCE(t.last_used_at, '')
		FROM api_tokens t LEFT JOIN users u ON u.id = t.created_by ORDER BY t.name COLLATE NOCASE, t.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []APIToken
	for rows.Next() {
		var t APIToken
		if err := rows.Scan(&t.ID, &t.Name, &t.Prefix, &t.IncludePrivate, &t.CreatedBy, &t.CreatedAt, &t.LastUsedAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) DeleteToken(id int64) error {
	_, err := s.DB.Exec(`DELETE FROM api_tokens WHERE id = ?`, id)
	return err
}

// SetTokenPrivate turns a token's access to private events on or off.
func (s *Store) SetTokenPrivate(id int64, private bool) error {
	_, err := s.DB.Exec(`UPDATE api_tokens SET include_private = ? WHERE id = ?`, private, id)
	return err
}

// AuthenticateToken finds the token and notes that it was used (at most
// once a minute, to spare the database a write on every request). Unknown
// tokens give ErrNotFound.
func (s *Store) AuthenticateToken(raw string) (*APIToken, error) {
	if !strings.HasPrefix(raw, apiTokenPrefix) {
		return nil, ErrNotFound
	}
	var t APIToken
	if err := s.DB.QueryRow(`SELECT id, name, prefix, include_private FROM api_tokens WHERE token_hash = ?`, hashToken(raw)).
		Scan(&t.ID, &t.Name, &t.Prefix, &t.IncludePrivate); err != nil {
		return nil, notFound(err)
	}
	_, err := s.DB.Exec(`UPDATE api_tokens SET last_used_at = datetime('now')
		WHERE id = ? AND (last_used_at IS NULL OR last_used_at < datetime('now', '-1 minute'))`, t.ID)
	return &t, err
}

// Access is the calendar access this token grants.
func (t *APIToken) Access() CalendarAccess {
	if t.IncludePrivate {
		return CalendarPrivate
	}
	return CalendarPublic
}
