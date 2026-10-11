-- People. With single sign-on this is a local cache of User Management's
-- users (keyed by sso_subject), so every users(id) reference keeps working.
CREATE TABLE users (
    id            INTEGER PRIMARY KEY,
    username      TEXT NOT NULL UNIQUE COLLATE NOCASE,
    display_name  TEXT NOT NULL DEFAULT '',
    password_hash TEXT,                        -- NULL: can't sign in locally
    is_admin      INTEGER NOT NULL DEFAULT 0,
    sso_subject   TEXT UNIQUE,                 -- User Management's user ID
    active        INTEGER NOT NULL DEFAULT 1,  -- 0: access removed, row kept for history
    created_at    TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE sessions (
    token            TEXT PRIMARY KEY,
    user_id          INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    csrf_token       TEXT NOT NULL,
    expires_at       TEXT NOT NULL,
    sso_grant        TEXT,   -- the grant behind an SSO session; NULL for local login
    grant_checked_at TEXT,
    created_at       TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX sessions_user ON sessions(user_id);

CREATE TABLE settings (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

-- A dinner, work day, fellowship, meeting, outreach... Dates are
-- YYYY-MM-DD, times HH:MM ('' for all day). ends_on '' means one day.
CREATE TABLE events (
    id             INTEGER PRIMARY KEY,
    title          TEXT NOT NULL,
    kind           TEXT NOT NULL DEFAULT '',  -- "Fellowship dinner", "Work day"
    status         TEXT NOT NULL DEFAULT 'planning'
                   CHECK (status IN ('planning', 'ready', 'done', 'cancelled')),
    starts_on      TEXT NOT NULL,
    start_time     TEXT NOT NULL DEFAULT '',
    ends_on        TEXT NOT NULL DEFAULT '',
    end_time       TEXT NOT NULL DEFAULT '',
    coordinator    TEXT NOT NULL DEFAULT '',
    expected_count INTEGER CHECK (expected_count IS NULL OR expected_count >= 0),  -- headcount
    description    TEXT NOT NULL DEFAULT '',
    notes          TEXT NOT NULL DEFAULT '',
    locked         INTEGER NOT NULL DEFAULT 0,  -- 1: only members (and admins) see it
    copied_from    INTEGER REFERENCES events(id) ON DELETE SET NULL,
    created_at     TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at     TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX events_starts ON events(starts_on);

-- Who may see a locked event. Kept while unlocked, so locking again
-- brings the same people back.
CREATE TABLE event_members (
    event_id INTEGER NOT NULL REFERENCES events(id) ON DELETE CASCADE,
    user_id  INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    PRIMARY KEY (event_id, user_id)
);
CREATE INDEX event_members_user ON event_members(user_id);

-- Rooms (places in the maintenance tracker, or anywhere else) the event
-- uses, and how each should be set up. place_id is the tracker's place ID;
-- place_name keeps its path ("Main › Fellowship Hall") so pages work when
-- the tracker can't be reached.
CREATE TABLE event_rooms (
    id         INTEGER PRIMARY KEY,
    event_id   INTEGER NOT NULL REFERENCES events(id) ON DELETE CASCADE,
    position   INTEGER NOT NULL,
    place_id   INTEGER,
    place_name TEXT NOT NULL,
    purpose    TEXT NOT NULL DEFAULT '',  -- "Dinner seating", "Kids' crafts"
    setup      TEXT NOT NULL DEFAULT '',  -- "8 round tables, 64 chairs, stage left clear"
    notes      TEXT NOT NULL DEFAULT ''
);
CREATE INDEX event_rooms_event ON event_rooms(event_id, position);
CREATE INDEX event_rooms_place ON event_rooms(place_id);

-- The timeline / run of show: set-up, doors open, meal, clean-up.
CREATE TABLE schedule_items (
    id       INTEGER PRIMARY KEY,
    event_id INTEGER NOT NULL REFERENCES events(id) ON DELETE CASCADE,
    date     TEXT NOT NULL,
    time     TEXT NOT NULL DEFAULT '',
    end_time TEXT NOT NULL DEFAULT '',
    label    TEXT NOT NULL,
    room_id  INTEGER REFERENCES event_rooms(id) ON DELETE SET NULL,
    notes    TEXT NOT NULL DEFAULT ''
);
CREATE INDEX schedule_event ON schedule_items(event_id, date, time);

-- To-do lists ("Before", "Kitchen", "Clean-up"). Tasks with no list show
-- under "Other".
CREATE TABLE task_lists (
    id       INTEGER PRIMARY KEY,
    event_id INTEGER NOT NULL REFERENCES events(id) ON DELETE CASCADE,
    position INTEGER NOT NULL,
    name     TEXT NOT NULL
);
CREATE INDEX task_lists_event ON task_lists(event_id, position);

-- A task is someone's: a user here (assignee_id) or anyone else by name
-- (assignee_name, e.g. "Youth group"). Not both.
CREATE TABLE tasks (
    id            INTEGER PRIMARY KEY,
    event_id      INTEGER NOT NULL REFERENCES events(id) ON DELETE CASCADE,
    list_id       INTEGER REFERENCES task_lists(id) ON DELETE SET NULL,
    position      INTEGER NOT NULL,
    title         TEXT NOT NULL,
    status        TEXT NOT NULL DEFAULT 'todo'
                  CHECK (status IN ('todo', 'in_progress', 'blocked', 'done')),
    assignee_id   INTEGER REFERENCES users(id) ON DELETE SET NULL,
    assignee_name TEXT NOT NULL DEFAULT '',
    due_on        TEXT NOT NULL DEFAULT '',
    room_id       INTEGER REFERENCES event_rooms(id) ON DELETE SET NULL,
    notes         TEXT NOT NULL DEFAULT '',
    done_at       TEXT NOT NULL DEFAULT '',
    updated_by    TEXT NOT NULL DEFAULT '',
    updated_at    TEXT NOT NULL DEFAULT (datetime('now')),
    CHECK (assignee_id IS NULL OR assignee_name = '')
);
CREATE INDEX tasks_event ON tasks(event_id, list_id, position);
CREATE INDEX tasks_assignee ON tasks(assignee_id);

-- Items and supplies wanted from the maintenance tracker (a product or a
-- supply there) or from anywhere else, and the room they go to.
CREATE TABLE needs (
    id           INTEGER PRIMARY KEY,
    event_id     INTEGER NOT NULL REFERENCES events(id) ON DELETE CASCADE,
    name         TEXT NOT NULL,
    quantity     INTEGER NOT NULL DEFAULT 1 CHECK (quantity > 0),
    room_id      INTEGER REFERENCES event_rooms(id) ON DELETE SET NULL,
    tracker_kind TEXT NOT NULL DEFAULT '' CHECK (tracker_kind IN ('', 'product', 'supply')),
    tracker_id   INTEGER,
    status       TEXT NOT NULL DEFAULT 'needed' CHECK (status IN ('needed', 'gathered', 'returned')),
    notes        TEXT NOT NULL DEFAULT '',
    updated_by   TEXT NOT NULL DEFAULT '',
    updated_at   TEXT NOT NULL DEFAULT (datetime('now')),
    CHECK ((tracker_kind = '') = (tracker_id IS NULL))
);
CREATE INDEX needs_event ON needs(event_id);

-- Helper sign-ups: a job ("Greeters", "Dishwashing"), how many people it
-- needs, and who's on it.
CREATE TABLE helper_roles (
    id       INTEGER PRIMARY KEY,
    event_id INTEGER NOT NULL REFERENCES events(id) ON DELETE CASCADE,
    position INTEGER NOT NULL,
    name     TEXT NOT NULL,
    slots    INTEGER NOT NULL DEFAULT 1 CHECK (slots > 0),
    when_text TEXT NOT NULL DEFAULT '',  -- "5:00–6:30"
    room_id  INTEGER REFERENCES event_rooms(id) ON DELETE SET NULL,
    notes    TEXT NOT NULL DEFAULT ''
);
CREATE INDEX helper_roles_event ON helper_roles(event_id, position);

CREATE TABLE helpers (
    id       INTEGER PRIMARY KEY,
    role_id  INTEGER NOT NULL REFERENCES helper_roles(id) ON DELETE CASCADE,
    user_id  INTEGER REFERENCES users(id) ON DELETE SET NULL,  -- NULL: someone without an account
    name     TEXT NOT NULL CHECK (name <> ''),  -- their name; a user's display name when signed up
    phone    TEXT NOT NULL DEFAULT '',
    notes    TEXT NOT NULL DEFAULT ''
);
CREATE INDEX helpers_role ON helpers(role_id);
CREATE INDEX helpers_user ON helpers(user_id);
