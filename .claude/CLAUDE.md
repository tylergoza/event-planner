# Event Planner: project notes

Generic church event planning (dinners, work days, fellowships, meetings).
Sibling of `../production_planning` (it started as a copy of it),
`../maintenance_tracking` and `../user_management`. Same stack and
conventions: Go + SQLite (modernc, no CGO) + server-rendered templates +
vendored Stimulus with the no-build autoloader. No Node, no bundler, no new
dependencies. When changing shared patterns (auth, CSRF, layout, SSO,
Ansible), check whether the siblings should get the same change.

- Schema is `internal/store/migrations/001_initial.sql`. It is the contract
  between features; add a new numbered migration rather than editing 001
  once the app has been deployed.
- Reads from the tracker only through its read-only API (`internal/tracker`):
  products, items, supplies and places (rooms). Rows keep the tracker's IDs
  plus a cached name/path so pages work when the tracker is down.
- Cookies are `ep_*`; local port 8110 (tracker 8080, planner 8090, User
  Management 8100).
- SSO client ID in User Management is `events`; its secret comes from
  `UM_EVENTS_SECRET` at deploy time. Local login when `SSO_URL` is unset.
- Secrets never go in files or `export` lines in docs: the user keeps them
  in the macOS Keychain and runs make through `~/.zshrc` helpers:
  `with-do` (DIGITALOCEAN_TOKEN, service `digitalocean`), `with-um`
  (UM_PLANNER/TRACKER/EVENTS_SECRET from `um-planner`/`um-tracker`/
  `um-events`), `with-ep` (EP_TRACKER_TOKEN from `event-planner`), `with-pp`
  (planner's PP_TRACKER_TOKEN). Deploy is `with-do with-um make deploy`.
- Roles are `users.role`: helper (works on events), planner (also
  creates/runs events), admin (also users, settings, tokens, locks). Gate
  routes with `requirePlanner`/`requireAdmin`; hide controls with
  `.User.CanPlan`. SSO maps User Management's role via `RoleFromSSO`.
- Public paths (`isPublicPath` in routes.go): `/` (calendar when signed
  out), `/calendar…`, `/calendar.ics`, `/api/`. `/api/` and `.ics` skip
  session and CSRF (`isCookieFree`). What outsiders see comes only from
  `store.Calendar`/`CalendarEvent`: private and locked events are busy
  blocks (rooms + times). Never expose notes, tasks, helpers or needs.
- CSP is `style-src 'self'`: no inline `style=` attributes in templates.
- Each page template is parsed with only `layout.html` + `partials/*.html`,
  so anything shared between pages goes in a partial.
- Deploys onto the maintenance tracker's droplet (tag `maintenance-tracker`)
  as `events.<dns_zone>`, service user `events`, data in
  `/var/lib/event-planner`, Caddy site `/etc/caddy/sites/event-planner.caddy`.
  The main Caddyfile text must stay identical in every repo's deploy.yml.
  `deploy/ansible/vars.yml` is gitignored.
- The user runs all `git commit`s themselves (YubiKey signing). Don't commit.
- `CALENDAR_PLAN.md` is this app's part of joining `../calendar` (8120);
  tick its checkboxes as steps are done. Everything there is optional:
  this app must keep working when `CALENDAR_URL` is unset.
- `CONNECTIONS_PLAN.md` (this app is the reference build): links to
  other apps are admin settings on `/admin/connections`; env vars override
  them per whole connection. Secrets encrypted with `secrets.key`.
- Every app must run alone. Links to other apps are optional and off
  unless their URL and token are set (the `Configured()` pattern); when
  off, the app acts as if the other app doesn't exist. Each app owns its
  own data; others keep only a thin copy or a reference. No hub app that
  everything depends on.
- Shared kinds of data (places/rooms, items, events, users) keep the same
  shape in every app, matching the owning app's API. A thin local version
  is a subset of the full one, so when the owning app is connected the
  local rows start referencing it (its ID plus a cached name) instead of
  being rebuilt.
- `BOOKING_PLAN.md`: this app's part of bookable rooms / pending bookings / unscheduled events (design in `../.claude/plans/bookable-rooms-and-pending-bookings.md`). Tick its checkboxes as steps are done.
