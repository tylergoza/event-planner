package store

import (
	"errors"
	"strings"
	"testing"
)

func TestTokens(t *testing.T) {
	st := openTest(t)
	u := calUser(t, st, "ann", "admin")
	tok, err := st.CreateToken("Church website", false, u.ID)
	if err != nil || !strings.HasPrefix(tok, "ep_") {
		t.Fatalf("%q %v", tok, err)
	}
	var hash string
	st.DB.QueryRow(`SELECT token_hash FROM api_tokens`).Scan(&hash)
	if hash == "" || strings.Contains(hash, tok) || hash != hashToken(tok) {
		t.Errorf("only the hash should be stored: %q", hash)
	}

	got, err := st.AuthenticateToken(tok)
	if err != nil || got.Name != "Church website" || got.IncludePrivate || got.Access() != CalendarPublic {
		t.Fatalf("%+v %v", got, err)
	}
	for _, bad := range []string{"", "ep_nope", "mt_" + tok[3:], tok + "x"} {
		if _, err := st.AuthenticateToken(bad); !errors.Is(err, ErrNotFound) {
			t.Errorf("%q: %v", bad, err)
		}
	}

	list, _ := st.ListTokens()
	if len(list) != 1 || list[0].CreatedBy != "ann" || list[0].LastUsedAt == "" || list[0].Prefix != tok[:9] {
		t.Fatalf("%+v", list)
	}

	if err := st.SetTokenPrivate(got.ID, true); err != nil {
		t.Fatal(err)
	}
	got, _ = st.AuthenticateToken(tok)
	if !got.IncludePrivate || got.Access() != CalendarPrivate {
		t.Errorf("%+v", got)
	}

	if err := st.DeleteToken(got.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AuthenticateToken(tok); !errors.Is(err, ErrNotFound) {
		t.Errorf("deleted token still works: %v", err)
	}
}
