# Connections as admin settings

Every link to another app (maintenance tracker, User Management, the
church calendar) is set by an admin on a **Connections** page. An
environment variable, when set, **overrides** what's saved there. So a
deploy can make the first connection from Ansible, and the admin can then
take it over in the app and drop it from the server.

This file holds the design for all the apps. The event planner builds it
first; the others copy it. Their checklists are in their own
`CONNECTIONS_PLAN.md` (and in `../calendar/PLAN.md`).

Status: planned, nothing built yet (2026-10-11).

## What's already there

The tracker connection in the event planner and the production planner
already works this way (`trackerSettings` in `internal/server/server.go`,
`handleSettingsUpdate` in `handlers_admin.go`):

- `TRACKER_URL` / `TRACKER_TOKEN` win over the `tracker_url` /
  `tracker_token` settings; the Settings page shows them as "set on the
  server" and won't change them.
- The token is write-only on the page (only "set" or "not set").
- Saving rebuilds the client (`atomic.Pointer`) without a restart and
  checks the connection.

What's missing: SSO and the calendar are environment-only and read once at
start; the override is per field, which can mix a server URL with a
saved token; secrets sit in SQLite as plain text, so the **Download
backup** file carries them; and there's no way to hand an env value over to
the settings.

## The rules

1. **Order: environment, then settings, then off.**
2. **Whole connection, not per field.** If any of a connection's variables
   is set in the environment, the whole connection comes from the
   environment and the settings for it are ignored (and shown greyed out).
   This replaces today's per-field override for the tracker. Mixing a URL
   from one place with a token from another is how you end up debugging the
   wrong thing.
3. **Only links to other apps move.** These stay environment-only because
   they're needed before the database opens or belong to the server:
   `ADDR`, `DB_PATH`, `DEV`, `TRUST_PROXY`, `TZ`.
4. **Secrets are write-only.** The page shows "set, ends …a1b2", never the
   value. A blank field on save keeps the current one; **Remove** clears it.
5. **Every change applies without a restart** and is tested straight away.

## Connections per app

| App | Connections |
|-----|-------------|
| Event planner | Tracker, Single sign-on, Calendar |
| Production planner | Tracker, Single sign-on, Calendar |
| Maintenance tracker | Single sign-on, Calendar (optional, read) |
| Calendar | Tracker, Single sign-on, Event planner address (no token) |
| User Management | None: it's the hub. Its Apps page is where the other apps get their client ID and secret |

| Connection | Fields | Env vars | Settings keys |
|------------|--------|----------|---------------|
| Tracker | Address, API token (`mt_…`) | `TRACKER_URL`, `TRACKER_TOKEN` | `tracker_url`, `tracker_token` |
| Single sign-on | User Management address, internal address (optional), client ID, client secret, this app's address | `SSO_URL`, `SSO_INTERNAL_URL`, `SSO_CLIENT_ID`, `SSO_CLIENT_SECRET`, `BASE_URL` | `sso_url`, `sso_internal_url`, `sso_client_id`, `sso_client_secret`, `base_url` |
| Calendar | Address, internal address (optional), token (`cal_…`) | `CALENDAR_URL`, `CALENDAR_INTERNAL_URL`, `CALENDAR_TOKEN` | `calendar_url`, `calendar_internal_url`, `calendar_token` |

## Taking a connection over from the server

The "initial connection, then switch to the admin setting" path:

1. Deploy with the values in `vars.yml` (the Keychain helpers, as now). The
   app starts connected.
2. On **Connections**, the row says *Set on the server*. The admin presses
   **Keep in settings**: the app copies the environment values into its
   settings (secrets encrypted, below). The row now says *Set on the server;
   settings match, so it's safe to remove from the server*.
3. Blank those values in `vars.yml` and redeploy. The Ansible templates
   already leave out empty variables (`{% if %}`), so they drop out of
   the systemd override. The row says *Set here*. Nothing disconnected.

If settings and environment ever **differ**, the row says so, so a stale
server value doesn't silently win. Going back to server control is just
setting the values in `vars.yml` again.

## Secrets at rest

Settings secrets are stored encrypted with AES-256-GCM (Go standard
library, no new dependencies):

- A 32-byte key in `secrets.key` **next to the database** (same folder,
  mode 0600), made on first start if missing.
- Stored as `enc:v1:<base64 nonce+ciphertext>`. A plain value (today's
  `tracker_token`) is encrypted the first time the app starts with this
  change.
- **Download backup** stays useful but its secrets can't be read without
  the key. Restoring onto a new server without the key: those connections
  show *Can't read the saved secret: enter it again*. Nothing else breaks.
- Be honest about what it protects: a downloaded or copied database file.
  Someone with the whole server has the key too. That's fine; the
  environment values have the same exposure today.
- The server's own backups should include `secrets.key` along with the
  database (check the Ansible backup task).

## The Connections page

`/admin/connections`, admins only. The tracker part moves here from
**Settings** (Settings keeps site name and backup; its old tracker form
redirects). One section per connection:

- Where it comes from: *Set on the server*, *Set here*, or *Not connected*.
  Last changed by whom and when.
- The fields (read-only when set on the server), with **Keep in settings**
  when they are.
- **Test**: a live check (the client's `Check`) with the answer in plain
  words ("Connected", "The token was refused: make a new one in the
  tracker under API", "Can't reach https://…").
- Save tests too, and saves even if the test fails (except SSO, below), so
  you can set up an app that isn't running yet.
- **Calendar section** also holds the backfill: waiting count, last error,
  last successful send and **Send all events to the calendar**
  (`CALENDAR_PLAN.md` step 3).

Changes are logged (`slog`: who, which connection, which fields; never
values).

## Single sign-on needs care

A wrong SSO setting can lock every admin out, so it doesn't switch on just
by saving:

- **Turning on or changing it**: saving stores the new values as
  *pending*. The admin then presses **Test sign-in**, goes through User
  Management and comes back. Only if that works **and** the person who
  came back is an `admin` of this app in User Management does the pending
  set become live. Otherwise the old setting stays and the page says why.
  Their local account links to their church account the same way the first
  SSO rollout did (`sso_users`).
- **Turning it off** from the page: allowed after a confirm that says local
  passwords will be needed (and `reset-password` exists).
- **Server checks** while saving: the client ID and secret are checked
  server-to-server before the sign-in test. If User Management has no
  endpoint that fits, add one (see `../user_management/CONNECTIONS_PLAN.md`).
- **Break-glass**: `local-login on` stays. Add
  `<app> connection clear sso|tracker|calendar` for when a bad saved
  setting is the problem and nobody can sign in.
- Environment SSO values that are wrong still stop the app at start (as
  now: that's a deploy mistake and should be loud). A saved setting that's
  wrong never stops the app from starting: it logs, shows a banner to
  admins, and falls back to local login.
- The SSO client moves to an `atomic.Pointer` like the tracker's; the CSP's
  `form-action` already asks `ssoOrigin()` per request.

## Steps (event planner: the reference build)

### 1. Settings plumbing
- [ ] `internal/secrets`: key file load-or-create, `Seal`/`Open`, `enc:v1:` format; tests (round trip, wrong key, plain value passes through)
- [ ] `store.Setting` / `SetSetting` gain `SecretSetting` / `SetSecretSetting`; plain `tracker_token` encrypted on start
- [ ] A small connection type: fields, env names, setting keys, `Source()` (env / settings / off), `Resolve()`; one table for all three connections
- [ ] Tests: whole-connection rule (one env var set → all from env), blank secret keeps the old one, **Remove** clears it

### 2. Connections page with the tracker
- [ ] `/admin/connections` with the tracker section (moved from Settings; old form redirects)
- [ ] **Test**, **Keep in settings**, "settings differ" warning, last changed by/when
- [ ] README: a **Connections** section describing environment vs settings and the hand-over steps

### 3. Single sign-on
- [ ] SSO client behind `atomic.Pointer`; rebuilt on change
- [ ] Pending values, **Test sign-in** round trip, admin-role check, then live
- [ ] Turn off with confirm
- [ ] Saved-but-broken SSO falls back to local login with an admin banner; env-broken still fails at start
- [ ] `event-planner connection clear sso|tracker|calendar`
- [ ] Tests: pending doesn't change sign-in; test as a non-admin is refused; clear command works with SSO on

### 4. Calendar
- [ ] Calendar connection in the same table (when `CALENDAR_PLAN.md` step 1 is built, build it this way instead of env-only)
- [ ] Backfill status and button in its section

### 5. Ansible
- [ ] Every connection variable in `vars.yml` optional, documented as "leave blank to manage in the app"
- [ ] Backup task includes `secrets.key`
- [ ] README deploy section: first deploy with vars, **Keep in settings**, blank vars, redeploy
