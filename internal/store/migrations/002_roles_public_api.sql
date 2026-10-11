-- Three roles instead of is_admin:
--   helper   works on events (tasks, sign-ups, needs) but can't create them
--   planner  creates and runs events, but can't manage users or settings
--   admin    everything
-- With single sign-on the role comes from User Management.
ALTER TABLE users ADD COLUMN role TEXT NOT NULL DEFAULT 'helper'
    CHECK (role IN ('helper', 'planner', 'admin'));
UPDATE users SET role = 'admin' WHERE is_admin = 1;
ALTER TABLE users DROP COLUMN is_admin;

-- What people outside see. A public event shows on the public calendar
-- and to every API token with its title, kind, times, rooms and
-- public_description. A private one shows only as its rooms being busy
-- ("Lifecenter unavailable 2:00–4:00"), except to people signed in and to
-- API tokens allowed private events. Internal notes, tasks, helpers and
-- needs are never public.
ALTER TABLE events ADD COLUMN visibility TEXT NOT NULL DEFAULT 'private'
    CHECK (visibility IN ('public', 'private'));
ALTER TABLE events ADD COLUMN public_description TEXT NOT NULL DEFAULT '';

-- Tokens other sites and apps use to read events from /api/v1/. Only a
-- hash is kept: the token is shown once, when it's made, and its first
-- few characters (prefix) tell tokens apart afterwards. A token sees
-- public events and busy times; include_private lets it see private
-- events' details too (never a locked event's).
CREATE TABLE api_tokens (
    id              INTEGER PRIMARY KEY,
    name            TEXT    NOT NULL,
    token_hash      TEXT    NOT NULL UNIQUE,
    prefix          TEXT    NOT NULL,
    include_private INTEGER NOT NULL DEFAULT 0,
    created_by      INTEGER REFERENCES users(id) ON DELETE SET NULL,
    created_at      TEXT    NOT NULL DEFAULT (datetime('now')),
    last_used_at    TEXT
);
