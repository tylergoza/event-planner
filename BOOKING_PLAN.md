# Event Planner: events not scheduled yet, and pencilled-in dates

The cross-app design is in
`../.claude/plans/bookable-rooms-and-pending-bookings.md`; this is the
event planner's part. All of it works without the calendar. With the
calendar connected, pencilled-in dates hold rooms there as `tentative`.

Status: not started (planned 2026-10-13).

## The idea

An event goes through three stages before it happens:

| Stage | Date | On the calendar |
|-------|------|-----------------|
| **Not scheduled yet** | None (`starts_on = ''`) | Not sent. If it had been sent before, it's removed |
| **Pencilled in** | A date we're holding but not sure of (`date_tentative = 1`) | Sent with status `tentative`: blocks the rooms, shown as "Pencilled in" |
| **Scheduled** | Confirmed date | Sent as `confirmed`, as today |

This is separate from the existing `status` (`planning`, `ready`, `done`,
`cancelled`), which tracks the work. A `planning` event can be at any
stage. A `ready` event must have a date; it may still be pencilled in, and
the dashboard warns about that.

**Rooms**: the room picker keeps listing every tracker building, room and
area. It must **not** filter on the tracker's new `bookable` flag, which is
only for the calendar's public request form. An unscheduled event can
still have rooms picked; they're simply not held anywhere until there's a
date.

## Steps

### 1. Events with no date yet
- [ ] Migration `007_unscheduled.sql`: add `date_tentative INTEGER NOT NULL DEFAULT 0`. `starts_on` stays `NOT NULL`; `''` means "not scheduled yet" (no table rebuild). Drop and recreate the `004` outbox update trigger so it also watches `date_tentative`
- [ ] `readEvent` (`internal/server/handlers_events.go`): a **Not scheduled yet** checkbox clears the date fields and skips "Pick the day it starts." Refused for `ready` and for repeating events (a series needs a first date)
- [ ] `store.Event.Scheduled()` (`StartsOn != ""`); `.When` says "Not scheduled yet"; `relDays` returns "" for an empty date. The new-event form still pre-fills today, with the checkbox off
- [ ] Dashboard and `/events`: a **Not scheduled yet** group above the dated ones; `sortSummaries` puts undated events in it, not first by empty string
- [ ] Undated events stay out of everything that's laid out by date: the built-in `/calendar` (already skipped by the `starts_on` range), `.ics` and `/api/v1/events` (filter `starts_on <> ''`), room clash checks, schedule "Upcoming"
- [ ] Calendar push: `OutboxEvent` / `calendarBooking` send a **delete** for an undated event (it may have had a date before), not a put
- [ ] Tests: create/edit undated, `ready` refused without a date, lists and sort, API/ics leave it out, outbox sends delete

### 2. Pencilled-in dates
- [ ] Event form: **Date pencilled in (not confirmed yet)** checkbox next to the date. Clearing it is "Confirm the date". On the event page, a one-click **Confirm date** button
- [ ] `calendarBooking` (`internal/server/calendar_sync.go`): status `tentative` when `date_tentative`, still `cancelled` when the event is cancelled, else `confirmed`. Update the comment at `internal/calendar/calendar.go` (status values)
- [ ] Repeats: the checkbox applies per occurrence; "this and later" edits copy it
- [ ] Built-in `/calendar` (running alone): pencilled-in entries get the same hatched style and "Pencilled in" label as the calendar app (copy the CSS class from `../calendar` once its step 2 is built). `.ics` sends `STATUS:TENTATIVE`; `/api/v1/events` adds `"status": "tentative"`
- [ ] Dashboard: a `ready` event whose date is still pencilled in gets a "Date not confirmed" warning
- [ ] Room clash warnings treat other apps' `tentative` and `pending` bookings as busy (they already arrive from `GET /api/v1/availability` as busy blocks; show their label)
- [ ] Tests: push status for each combination, `.ics` status, confirm button

### 3. Docs
- [ ] README: the three stages; `CALENDAR_PLAN.md`: the status rule now has `tentative`
- [ ] `.claude/CLAUDE.md`: "`starts_on = ''` means not scheduled yet; never sent to the calendar"
