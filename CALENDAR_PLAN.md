# Event Planner: joining the Church Calendar

The design is in `../calendar/PLAN.md`; read its "push API" section first.
This file is the event planner's part.

Status: built (2026-10-12). Left: press **Send all events to the calendar** after the first deploy with the calendar set.

## What changes

With the **Calendar** connection set (address and token, on `/admin/connections`, or `CALENDAR_URL` / `CALENDAR_TOKEN` on the server, which win; see `CONNECTIONS_PLAN.md`), the event planner sends every
event date and its rooms to the calendar. The calendar becomes the public
calendar and the place room clashes are checked across apps.

**Without it, nothing changes.** The event planner keeps its own
public calendar, `.ics`, API and same-app room warnings, so it still works
alone. Don't delete any of that.

## How sending works

- A `calendar_outbox` table, one row per event that needs sending, keyed by
  event ID (a newer change replaces an older waiting one):

  ```sql
  CREATE TABLE calendar_outbox (
      event_id    INTEGER PRIMARY KEY,   -- no FK: deleted events still need a DELETE sent
      op          TEXT NOT NULL CHECK (op IN ('put', 'delete')),
      attempts    INTEGER NOT NULL DEFAULT 0,
      next_try_at TEXT NOT NULL DEFAULT (datetime('now')),
      last_error  TEXT NOT NULL DEFAULT ''
  );
  ```

- Every store function that changes what the calendar shows (event fields,
  rooms, status, visibility, lock, series edits, skip, move, delete, the
  6-hourly series top-up) writes the outbox row **in the same
  transaction**. Saving an event never waits on the calendar.
- A worker sends waiting rows every 30 seconds, and straight after a save.
  Failures back off (1 min, 5, 30, then hourly) and show on the admin page.
- `source` is `events`, `source_id` is the event ID. Each repeating date is
  its own event already, so each is its own booking.
- What's sent: title, kind, dates and times, rooms (`place_id` + name),
  public description, `url` to `/events/{id}`, `visibility`
  (`locked` when locked, else `public`/`private`), `status`
  (`cancelled` when cancelled, else `confirmed`). For locked events send
  **no title, kind or description**. Never send notes, tasks, helpers or
  needs.
- Done events stay (they happened). Cancelled ones are sent as cancelled.

## Steps

### 1. Client and settings
- [x] `internal/calendar` client (shape like `internal/tracker`): `Configured()`, `PutBooking`, `DeleteBooking`, `Availability`, `Check`
- [x] **Calendar** connection (address, internal address, token) built the `CONNECTIONS_PLAN.md` way: on `/admin/connections`, with `CALENDAR_URL`, `CALENDAR_INTERNAL_URL`, `CALENDAR_TOKEN` overriding; README settings table (in the connection table and the README; works from the environment or saved settings, the page itself waits for CONNECTIONS_PLAN.md step 2)
- [x] Tests against an `httptest` fake calendar, like `tracker_test.go`

### 2. Outbox and worker
- [x] Migration `004_calendar_outbox.sql`
- [x] Outbox writes in every store function listed above, inside the existing transactions (done with triggers on `events` and `event_rooms` in migration 004, so every path is covered, including copies and series top-ups)
- [x] Worker with back-off; stops cleanly on shutdown
- [x] Tests: edit → one `put`; delete → `delete`; locked event sends no title; ten quick edits → one row

### 3. Send everything (the backfill)
The backfill is a button on the admin **Connections** page, not a command, so nobody needs a
shell on the server to connect the apps.

- [x] In the calendar section of `/admin/connections` (admins only): waiting rows, last error, last successful send (connected or not and **Test** come from the connection itself)
- [x] **Send all events to the calendar** button on that page: a POST with CSRF, behind a confirm that says how many events it will send ("Send 214 events from Oct 2025 on?")
- [x] It queues every event from one year ago on as `put` (cancelled ones as cancelled) in one transaction and returns straight away; the worker does the sending, and the page shows the waiting count going down (refresh, or a small Stimulus controller polling a JSON count)
- [x] Safe to press again (PUT is idempotent, outbox rows are keyed by event, so pressing twice doesn't queue twice)
- [x] Tests: the button queues the right events; pressing twice leaves one row per event; non-admins get 403

### 4. Room warnings across apps
- [x] When the calendar is set, the rooms picker asks `GET /api/v1/availability` and shows clashes from any app (busy blocks show as "Booked · 2:00–4:00 pm")
- [x] If the calendar can't be reached, fall back to today's same-app check and say so
- [x] After a save, show any `conflicts` the calendar returned as a warning (never block the save)

### 5. Hand over the public calendar
- [x] When the calendar is set: `/calendar`, `/calendar/{id}` and `/calendar.ics` redirect (302) to the calendar app; `/calendar/{id}` maps to the calendar's booking for `events/{id}` (`/calendar/by-source/events/{id}` on the calendar side)
- [x] Signed-out `/` redirects to the calendar too
- [x] `/api/v1/events…` keeps working (the website may still use it) until the website is switched; then it's left for standalone installs

### 6. Make this an event
- [x] `GET /events/new?from_calendar={id}&title=…&starts_on=…&start_time=…&end_time=…&rooms=…` fills the form
- [x] Hidden `from_calendar` is kept with the new event (column `calendar_replaces`, cleared after the first successful push) and sent as `replaces`

### 7. Deploy
- [x] Either set the connection on `/admin/connections`, or for the first deploy use Ansible vars `calendar_url` and the token from the Keychain (`with-ep` gets `EP_CALENDAR_TOKEN` too), then **Keep in settings** and blank them
- [ ] After the first deploy with the calendar set, press **Send all events to the calendar** on `/admin/connections`
