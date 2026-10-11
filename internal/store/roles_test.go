package store

import (
	"path/filepath"
	"testing"
)

func TestRoleDefaultsAndCanPlan(t *testing.T) {
	st := openTest(t)
	id, err := st.CreateUser("ann", "", "a long password", "")
	if err != nil {
		t.Fatal(err)
	}
	u, _ := st.GetUser(id)
	if u.Role != RoleHelper || u.CanPlan() || u.IsAdmin() {
		t.Errorf("a new user is a helper: %+v", u)
	}
	if _, err := st.CreateUser("bob", "", "a long password", "wizard"); err != nil {
		t.Fatal(err)
	}
	for role, want := range map[string][2]bool{RoleHelper: {false, false}, RolePlanner: {true, false}, RoleAdmin: {true, true}} {
		u := &User{Role: role}
		if u.CanPlan() != want[0] || u.IsAdmin() != want[1] {
			t.Errorf("%s: CanPlan %v IsAdmin %v", role, u.CanPlan(), u.IsAdmin())
		}
	}
	if RoleFromSSO("user") != RoleHelper || RoleFromSSO("planner") != RolePlanner || RoleFromSSO("admin") != RoleAdmin || RoleFromSSO("zzz") != RoleHelper || RoleFromSSO("") != RoleHelper {
		t.Error("RoleFromSSO mapping is off")
	}
	if err := st.UpdateUser(id, "Ann", RolePlanner); err != nil {
		t.Fatal(err)
	}
	if u, _ := st.GetUser(id); u.Role != RolePlanner || u.DisplayName != "Ann" {
		t.Errorf("after update: %+v", u)
	}
}

func TestMigration002MapsIsAdmin(t *testing.T) {
	st, err := openAt(filepath.Join(t.TempDir(), "m.db"), 1)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	for _, u := range []struct {
		name  string
		admin int
	}{{"boss", 1}, {"ann", 0}} {
		if _, err := st.DB.Exec(`INSERT INTO users (username, is_admin) VALUES (?, ?)`, u.name, u.admin); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.migrate(2); err != nil {
		t.Fatal(err)
	}
	roles := map[string]string{}
	rows, _ := st.DB.Query(`SELECT username, role FROM users`)
	for rows.Next() {
		var n, r string
		rows.Scan(&n, &r)
		roles[n] = r
	}
	rows.Close()
	if roles["boss"] != "admin" || roles["ann"] != "helper" {
		t.Errorf("roles after migration: %v", roles)
	}
}

func TestLastAdminCount(t *testing.T) {
	st := openTest(t)
	boss, _ := st.CreateUser("boss", "", "a long password", RoleAdmin)
	st.CreateUser("pat", "", "a long password", RolePlanner)
	if n, _ := st.CountAdmins(); n != 1 {
		t.Fatalf("%d admins, want 1 (planners don't count)", n)
	}
	st.UpdateUser(boss, "", RolePlanner)
	if n, _ := st.CountAdmins(); n != 0 {
		t.Fatalf("%d admins after demotion, want 0", n)
	}
}

func TestEventVisibilityStoredAndCopied(t *testing.T) {
	st := openTest(t)
	id, err := st.CreateEvent(&Event{Title: "Dinner", StartsOn: "2026-11-01"})
	if err != nil {
		t.Fatal(err)
	}
	if e, _ := st.GetEvent(id); e.Visibility != "private" || e.PublicDescription != "" {
		t.Errorf("default visibility: %q", e.Visibility)
	}
	e, _ := st.GetEvent(id)
	e.Visibility, e.PublicDescription = "public", "Join us"
	if err := st.UpdateEvent(e); err != nil {
		t.Fatal(err)
	}
	cp, err := st.CopyEvent(id, CopyOptions{StartsOn: "2026-12-01"})
	if err != nil {
		t.Fatal(err)
	}
	if c, _ := st.GetEvent(cp); c.Visibility != "public" || c.PublicDescription != "Join us" {
		t.Errorf("copy: %q %q", c.Visibility, c.PublicDescription)
	}
	e.Visibility = "bogus"
	st.UpdateEvent(e)
	if e, _ := st.GetEvent(id); e.Visibility != "private" {
		t.Errorf("unknown visibility should be private, got %q", e.Visibility)
	}
}
