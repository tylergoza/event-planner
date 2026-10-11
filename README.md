# Church Event Planner

Plan the church's events: fellowship dinners, work days, meetings and
outreach. Set the dates and timeline, make to-do lists with who's doing each
task, pick the rooms (from the [maintenance tracker](../maintenance_tracking))
and how each is set up, list the items and supplies needed, sign up helpers,
and see who's doing what.

Built the same way as the maintenance tracker:

- **Backend:** Go, one static binary with its templates, JS and CSS embedded
- **Database:** SQLite (one file, pure-Go driver, no CGO)
- **Frontend:** server-rendered HTML + [Stimulus](https://stimulus.hotwired.dev), vendored, with the
  same no-build autoloader. No Node, no bundler.

## Quick start

Needs Go 1.27 or newer.

```sh
make dev                     # http://127.0.0.1:8110, edits to web/ show on refresh
go run . seed-demo           # optional: a sample Fall Fellowship Dinner
```

`/` is the public calendar until someone signs in. On a new install, **Sign
in** goes to `/setup` to create the admin account (local login only; with
single sign-on on, `/setup` is off). It runs on port 8110, so the
maintenance tracker (8080), the production planner (8090) and User
Management (8100) can run alongside it.

## Features

| Area | What it does |
|------|--------------|
| Dashboard | Upcoming events with task and needs progress. Then **My tasks** and **My helper jobs**. Signed-in people land here at `/`, with a **Calendar** link. |
| Repeating events | Make an event repeat: every N days, every N weeks on chosen weekdays, or every N months on a date (the 15th) or a weekday (the 2nd Sunday, the last Friday). It ends never, on a date, or after N times. Each date is a real event with its own copy of the tasks, rooms, helper jobs, needs and timeline, made up to 6 months ahead and topped up every 6 hours. A new series starts on the pattern's first date on or after the start you pick. On any date: edit **just this date** or **this and later dates** (only the fields you changed carry forward), move it to another day, or **Skip this date** (removes only that one; restore it from the series page). **Make later dates like this one** copies chosen parts (rooms, timeline, tasks, helper jobs, needs) onto the later dates, and **End the series from here** or **Change the repeat pattern** from a date leave earlier dates alone. Each shows what would be lost first. Changes that would reach a later date locked away from you are refused. Planners manage series. |
| Calendar | The public calendar at `/calendar`: a month grid on wide screens and an agenda list on phones. Filter by room. Each event has a page at `/calendar/{id}`. **Subscribe** (webcal) to `/calendar.ics`. See [Public and private events](#public-and-private-events). |
| API | Read-only JSON and iCalendar feeds for other sites and apps, behind tokens an admin makes. See [The events API](#the-events-api). |
| Events | Title, kind, dates and times, coordinator, expected headcount, a status (Planning, Ready, Done, Cancelled) and whether it's public or private. Done and cancelled ones leave the dashboard but are kept. **Copy** an event for the next one: pick a new date and its timeline, tasks and rooms move with it, statuses start over, and you choose whether to keep who's on each task and the helpers signed up. Planners make and copy events. |
| Schedule | The timeline, or run of show. Each item has a time and notes, and can be in one of the event's rooms. |
| Tasks | Lists such as Before, Kitchen and Clean-up. Add one at a time, or paste many. Assign a user or any name, set due dates (overdue ones are flagged), and pick a room. Change status right from the list: To do, Doing, Needs help, Done. Filters: All, Mine, Open. Prints as a checklist. |
| Rooms | Search the tracker's rooms, or type any place. Each room has a purpose and set-up notes, and shows what's pointed at it. Warns when another event uses the same tracker room on overlapping dates. Prints as a set-up sheet. |
| Needs | Like the planner's needs: products and supplies from the tracker with how many are on hand (low and out flags) and **short by** when there aren't enough. Anything else is something to buy or borrow. Each need can go to a room. Status: Needed, Gathered, Put back. |
| Helpers | Jobs with slots, when and room. Sign up a user, or a typed name with a phone number. **Sign me up** for yourself. Each job shows full, short or over. |
| People | "Who's doing what": each person's tasks and helper jobs, plus tasks nobody has and jobs still short. Prints. |
| Offline | Installable as an app. The dashboard and an event's overview open from the last copy when there's no signal. |
| Users and access | Three roles: Helper, Planner and Admin (see [Roles](#roles)). Admins manage users, settings, backups and API tokens, and can lock an event to a list of people. Locked events are busy-only to everyone else. Admins see every event. |

## Roles

Everyone who works in the app has one of three roles. Each role includes the
ones before it.

| Can | Helper | Planner | Admin |
|-----|:------:|:-------:|:-----:|
| See events, tasks, needs, the schedule, rooms, helper jobs and People (read-only for the schedule, rooms and helper jobs) | ✓ | ✓ | ✓ |
| Add, edit and tick off tasks and task lists; add needs | ✓ | ✓ | ✓ |
| Sign up for helper jobs (for self, or a typed name) | ✓ | ✓ | ✓ |
| Create, edit, copy and delete events | | ✓ | ✓ |
| Manage the schedule, rooms and helper jobs | | ✓ | ✓ |
| Manage users, settings, backups and API tokens | | | ✓ |
| Lock an event to a list of people, and see every locked event | | | ✓ |

With single sign-on the role comes from User Management: `admin`, `planner`
or `helper`. The old `user` role counts as helper. Local login: the Users page
has a role select, and the `create-user` command takes `--role` (see
[Commands](#commands)).

## Public and private events

Every event has a visibility, set on the event form. New events are
**private** unless someone picks **Public**. A public event can also have a
**public description**, which is the only text shown outside the app.

| | Public | Private |
|--|--------|---------|
| Public calendar and `/calendar/{id}` | Full: title, kind, times, rooms and public description | Only its rooms as busy, e.g. "Lifecenter unavailable · 2:00–4:00 pm" |
| API tokens without private access | Full | Busy |
| API tokens with private access | Full | Full |
| Signed-in people | Full | Full (except a locked event they aren't on, which is busy) |

Locked events are always busy outside the app, even when public. Notes,
tasks, helpers and needs are never shown outside the app, whatever the
visibility.

## Public calendar

Anyone can open `/calendar`. `/calendar?month=YYYY-MM&room=Name` picks the
month and a room. Signed-in people see the same pages with the details
described above and links to each event's internal page.

- `/` shows the calendar to visitors who aren't signed in.
- **Subscribe** on the calendar page gives `webcal://…/calendar.ics`. The
  feed is public and covers 90 days from today by default. `from` and `to`
  work as they do in the [API](#the-events-api). It's cached for 5 minutes.

## The events API

Other sites and apps can read the calendar through `/api/v1/`. Admins make
the tokens.

1. An admin opens **API tokens** (`/admin/tokens`), names what will use the
   token (e.g. "Church website") and saves. The token starts with `ep_` and
   is shown **once**. Only a hash is kept; the page then shows its first
   characters.
2. **Can see private events** is off by default. Ticking it lets that token
   see private events' details. It never shows a locked event's details.
   Admins can change this later, or revoke the token (anything using it
   stops working at once).

| Endpoint | Returns |
|----------|---------|
| `GET /api/v1/events?from=YYYY-MM-DD&to=YYYY-MM-DD` | `{"events": [...]}`, ordered by date and time |
| `GET /api/v1/events/{id}` | One event in full. 404 when the token can't see it in full, so the answer doesn't show it exists. |
| `GET /api/v1/events.ics` | The same range as iCalendar. Takes the token as `?token=` too, for calendar apps that subscribe by URL. |
| `GET /calendar.ics` | The public feed, no token |

`from` defaults to today and `to` to 90 days after `from`. `to` can't be
before `from`, and the range can be at most 400 days. Cancelled events are
left out. Send the token as a header:

```sh
curl -H "Authorization: Bearer ep_…" "https://events.example.org/api/v1/events?from=2026-11-01&to=2026-11-30"
```

Each entry has these fields. A full entry:

```json
{
  "id": 12,
  "busy": false,
  "title": "Fall Fellowship Dinner",
  "kind": "Fellowship dinner",
  "status": "ready",
  "starts_on": "2026-11-07",
  "start_time": "17:30",
  "ends_on": "",
  "end_time": "20:00",
  "rooms": ["Lifecenter", "Kitchen"],
  "description": "Potluck dinner for everyone. Bring a dish to share.",
  "public": true,
  "url": "https://events.example.org/calendar/12"
}
```

A busy entry has no `id`, `title`, `kind`, `status`, `description` or `url`:

```json
{
  "busy": true,
  "starts_on": "2026-11-14",
  "start_time": "14:00",
  "ends_on": "",
  "end_time": "16:00",
  "rooms": ["Lifecenter"],
  "public": false
}
```

`ends_on` is `""` for a one-day event and `start_time` is `""` for an all-day
one. `rooms` is `[]` when there are none. `description` is the public
description.

**CORS is open for GET**, so a page on any site can call the API. Anyone can
read a token out of a page's code, so in a browser on another site only use a
token without private access. Fetch private events from your server, and
keep that token secret.

## The maintenance tracker

The event planner only **reads** from the tracker, through its API (`/api/v1/`):
products, items, supplies and places.

1. In the tracker, an admin opens **API**, makes a token named
   "Event planner" and copies it (`mt_…`).
2. In the event planner, an admin opens **Settings**, enters the tracker's
   address and the token, and saves. Saving checks the connection.

Answers are cached for a minute. If the tracker can't be reached, pages
still work and say they couldn't check it. Needs and rooms remember what
they were linked to by the tracker's IDs.

Not done yet: the tracker's API can't be written to, so the planner can't
reserve items or ask for more of a supply there.

## Configuration

Set with a flag or an environment variable. Flags go **before** any command.

| Env var | Flag | Default | |
|---|---|---|---|
| `ADDR` | `-addr` | `:8110` | Listen address |
| `DB_PATH` | `-db` | `data/events.db` | SQLite file. Created, with migrations applied, on start. |
| `TZ` | | system | Church time zone: decides what "today", "upcoming" and "overdue" mean |
| `TRUST_PROXY=1` | `-trust-proxy` | off | Trust `X-Forwarded-For/Proto` from a reverse proxy. Only when a proxy is the only way in. |
| `DEV=1` | `-dev` | off | Load templates/static from `./web` on disk |
| `TRACKER_URL`, `TRACKER_TOKEN` | | | The maintenance tracker connection. When set, they override Settings and can't be changed there. |
| `SSO_URL` | | | User Management's public address (e.g. `https://accounts.example.org`). Set it to sign in there instead of with local passwords; empty keeps the local login. |
| `SSO_INTERNAL_URL` | | `SSO_URL` | User Management for server-to-server calls, e.g. `http://127.0.0.1:8100` on the shared droplet |
| `SSO_CLIENT_ID`, `SSO_CLIENT_SECRET` | | | From registering this app in User Management (shown once). Required when `SSO_URL` is set; the app won't start without them. |
| `BASE_URL` | | from the request | This app's public address, e.g. `https://events.example.org`. The sign-in redirect URI is `BASE_URL/auth/callback` and must be registered exactly like that in User Management. |

## Commands

```sh
event-planner create-user alice [--role helper|planner|admin]   # default helper; --admin is --role admin; prompts for a password (10+ characters)
event-planner reset-password alice           # also gives SSO users a local password, and turns them back on
event-planner local-login on|off             # break-glass, see below
event-planner backup /path/backup.db        # safe while the app is running
event-planner seed-demo                     # a sample event
```

The first user created is always an admin.

## Single sign-on

With `SSO_URL` set, people sign in with their church account in User
Management (the `user_management` repo) instead of a password here:

1. `/login` sends the browser to User Management's `/authorize`
   (authorization code + PKCE). The state and PKCE verifier wait in a
   10-minute `ep_sso` cookie.
2. User Management sends it back to `/auth/callback` with a code. The app
   checks the state, swaps the code for a grant (server to server), and
   adds or updates the person's local user row by their User Management ID.
   An existing local user with the same username is linked, so their
   history stays theirs. The role there maps to the same role here:
   `admin`, `planner` or `helper`. The old `user` counts as helper.
3. Every 5 minutes at most, a session's grant is checked again, which picks
   up name and role changes. If User Management says the sign-in has
   ended (signed out, turned off, access removed), the session ends here
   too. If it can't be reached, sessions keep working for an hour past
   their last good check, so restarting it signs no one out.
4. Signing out here also signs out of User Management, and so of the
   other apps within 5 minutes.

`/setup` is gone and `/account` links to User Management's account page.
Event members and other permissions stay in this app.

**Roles in User Management.** The event planner's roles there are `helper`,
`planner` and `admin`. Registering the app with
`make register-app APP=events DRY_RUN=false` in `user_management` sets them
and moves anyone with `user` to `helper`. After that, a User Management
user admin picks Planner or Helper per person on their page in User
Management. An app admin can do the same on this app's Users page.

**Users page.** With SSO on, Admin › Users (`/admin/users`) is the app-admin
page for this app's people, read from and changed through User
Management's API. It lists everyone with access: username, name, role,
whether they're active, suspended or turned off, and who last changed it.
An admin can change someone's role or suspend and restore their access.
Suspending signs them out here straight away. User Management checks every
change against its own records, as the signed-in admin, and the page shows
its answer if it says no (e.g. "make someone else an admin first" for the
last admin). There's no adding people, passwords, renames or deleting here:
do those in User Management. An admin signed in with the break-glass local
login sees the list read-only.

Opening the Users page or a people picker pulls the list from User
Management (reused for a minute) and updates the local users, so people show
up before they've ever signed in here. Anyone whose access is gone keeps
their local row, so their history stays theirs, but is marked inactive.
They stay on events they were already on, marked "(no access)", and can't be
newly added. If User Management can't be reached, the pickers use the last
list.

**Break-glass.** If User Management is broken, an admin can bring the
password form back over SSH, without a restart:

```sh
sudo -u events event-planner -db /var/lib/event-planner/events.db local-login on
sudo -u events event-planner -db /var/lib/event-planner/events.db reset-password alice
```

People who only ever signed in through SSO have no password here, so give
the admin one with `reset-password` (which also turns their account here
back on, in case their access was removed). `/login` then shows the password
form, with a link to sign in through User Management as well. Run
`local-login off` once User Management is back.

## Registering with User Management

Before turning SSO on, register this app from the `user_management` repo.
Secrets live in the macOS Keychain and reach `make` through the `with-*`
helpers in `~/.zshrc`: `with-do` (DigitalOcean token, service
`digitalocean`), `with-um` (the User Management client secrets, services
`um-planner`, `um-tracker` and `um-events`) and `with-ep` (this app's
tracker token, service `event-planner`).

```sh
# in ../user_management
with-do make register-app APP=events                             # dry run: shows what would change
with-do make register-app APP=events DRY_RUN=false               # registers; saves the secret in the Keychain as um-events
with-do make register-app APP=events DRY_RUN=false ROTATE=true   # lost or leaked secret: a new one, saved over the old
```

Registration can optionally copy people's access over from the production
planner. A new secret stops the old one working at once, so deploy here
straight after rotating.

## Deploying

The app is one binary plus one `.db` file, behind [Caddy](https://caddyserver.com)
for HTTPS. `deploy/` has the systemd unit (`deploy/event-planner.service`)
and a Caddyfile for doing it by hand.

### DigitalOcean with Ansible

`deploy/ansible/` works like the maintenance tracker's, and by default puts
the event planner **on the tracker's droplet**. It finds the droplet by the
tracker's tag (`maintenance-tracker`) and adds itself alongside, on port
8110, with its own service user (`events`), data folder and Caddy site.
Provision adds the DNS record for `app_domain` (pointing at the same droplet)
and the service user; with no tagged droplet it would create one. Deploy
runs the tests, builds the Linux binary here, backs up the database when the
binary changes (keeping 10), installs, and checks `/healthz`.

Caddy's config is split so both apps can deploy without overwriting each
other: `/etc/caddy/Caddyfile` only imports `/etc/caddy/sites/*.caddy`, and
each app writes its own site there (`event-planner.caddy`,
`maintenance-tracker.caddy`). If the droplet still has the Caddyfile from an
older tracker deploy, the planner's deploy stops and asks you to deploy the
updated tracker first, rather than taking the tracker offline.

For a droplet of its own instead, set `droplet_name` and `do_tag` to
`event-planner` in `vars.yml`.

```sh
cp deploy/ansible/vars.example.yml deploy/ansible/vars.yml   # set app_domain, dns_zone, sso_url, base_url
with-do with-um make provision         # first time (ends with a deploy, so it needs with-um too)
with-do with-um make deploy            # every update after that
```

On the first deploy the admin user is created before the app starts, so
`/setup` is never exposed. Set `EP_ADMIN_PASSWORD` to choose its password, or
a random one is printed at the end. With `sso_url` set there's no local admin
to create: people come from User Management.

To fix the tracker connection at deploy time instead of in Settings, set
`tracker_url` in `vars.yml`, make an API token in the tracker named "Event
planner", save it in the Keychain as `event-planner`, and add `with-ep`:
`with-do with-um with-ep make deploy`.

#### Turning on single sign-on

SSO is the expected setup, since User Management is already live. Set
`sso_url` and `base_url` in `deploy/ansible/vars.yml`, register the app
(above), and deploy with `with-um` so `UM_EVENTS_SECRET` is set:

```sh
# in deploy/ansible/vars.yml:
#   sso_url: https://accounts.example.org
#   base_url: https://events.example.org   # sign-in returns to base_url/auth/callback
with-do with-um make deploy
```

The deploy stops before building if `sso_url` is set without the secret or
`base_url`, so deploy through `with-um` every time while SSO is on. The secret goes only into the root-only systemd drop-in, with
nothing logged. To turn SSO back off, remove `sso_url` and deploy again. If
User Management is down, use the `local-login on` break-glass above instead.

With a hardware SSH key, use a deploy key as described in the tracker's
README (`ssh_private_key_file` in `vars.yml`; the same key works for both).

### Moving between hosts

**Settings → Backup** (or the `backup` command), stop the app on the new host,
copy the file to its database path (`/var/lib/event-planner/events.db`, owned
by `events`), and start it. Droplet backups cover both apps when they share
a droplet.

## Layout

```
main.go, seed.go            entrypoint, CLI commands, demo data
internal/store/             SQLite access, migrations, tasks, rooms, needs, helpers, calendar (access levels), API tokens, repeating series
internal/recur/             repeat patterns: which dates a daily, weekly or monthly rule gives
internal/tracker/           read-only client for the maintenance tracker's API
internal/sso/               User Management client: sign-in, grants, app users
internal/server/            routes, middleware, handlers, PWA endpoints, SSO, events API (api.go), calendar pages
web/templates/              layout, partials, pages (events, schedule, tasks, rooms, needs, helpers, people, users, calendar, tokens), sw.js
web/static/                 css, js (application, autoloader, controllers, vendor), icons
deploy/                     systemd unit, Caddyfile, ansible/ (DigitalOcean provision + deploy)
```

## Tests

```sh
make test
```

They cover repeat patterns and series (top-up, skip and restore, this and later, ending, changing the pattern, locked later dates), tasks (lists, ordering, overdue, status and done dates, pasted
lists), rooms and room conflicts, helpers (slots, sign-ups, locked events),
schedule and needs, copying an event (dates shift, rooms remap), who's
doing what, single sign-on (sign-in, grant checks, the Users page,
break-glass), the tracker client, and an end-to-end run against a fake
tracker API that renders every page, including when the tracker is down.
Newer ones cover roles and what each may do (`internal/server/roles_test.go`,
`internal/store/roles_test.go`), the calendar's access levels and the public
calendar pages (`calendar_test.go` in both packages), the events API and the
.ics feeds (JSON, CORS, range limits, escaping), and API tokens (`tokens_test.go`,
`api_test.go`).

## License

MIT. See [LICENSE](LICENSE).
