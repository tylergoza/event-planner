# Event Planner: meetings (agenda, minutes, events discussed)

A meeting is an event with a different type. It keeps everything an event
has (date, rooms, visibility, lock, members, tasks, repeats) and adds an
agenda, minutes, and a list of the events it discussed. Any meeting can
lead to events being planned, so every meeting gets the events list, not
only the event planning committee's.

Everything here stays inside the event planner. The calendar sees a
meeting like any other event (times, title, rooms, status, visibility).
The agenda, minutes and discussion notes are internal, like `notes`, and
never reach the calendar, `/api/v1/events`, `.ics` or the public calendar.

Status: planned (2026-10-13), nothing built yet.

## The idea

| Part | What it is | Where it lives |
|------|------------|----------------|
| **Type** | `event` or `meeting`. `kind` stays the free-text label ("Event planning committee", "Elders meeting") | `events.type` |
| **Agenda** | Ordered items, each a title and optional details. Written before the meeting | `agenda_items` |
| **Events discussed** | Events the meeting talks about: planned, half-planned, or only an idea. Each with a short note on what was said | `meeting_events` |
| **Minutes** | One text field, written during or after the meeting | `events.minutes` |
| **Action items** | The existing tasks, on the meeting itself or on one of the events discussed | `tasks` (no change) |

**Ideas are events.** An idea that came up at a meeting becomes a new
`planning` event that isn't scheduled yet (`starts_on = ''`, from
`BOOKING_PLAN.md`), linked to the meeting. It's only a title until someone
plans it, and it's never sent to the calendar until it has a date. There is
no separate "idea" table.

**Standing agenda.** A repeating meeting's template event holds its usual
agenda, so each occurrence starts with it (series occurrences are already
copied from the template).

**Carrying things over.** A **Bring over from last meeting** button copies
the previous meeting's events that are still open (not `done` or
`cancelled`), plus any agenda items marked *carry over*. The previous
meeting is the one before it in the same series, or else the latest
earlier meeting with the same `kind`. It's a button, not automatic,
because series occurrences are made ahead of time and would copy a stale
list.

## Steps

### 1. Schema (`008_meetings.sql`)
- [ ] `ALTER TABLE events ADD COLUMN type TEXT NOT NULL DEFAULT 'event' CHECK (type IN ('event', 'meeting'))`
- [ ] `ALTER TABLE events ADD COLUMN minutes TEXT NOT NULL DEFAULT ''`
- [ ] `agenda_items (id, event_id → events ON DELETE CASCADE, position, title NOT NULL CHECK (title <> ''), details TEXT DEFAULT '', carry_over INTEGER DEFAULT 0)`, index `(event_id, position)`
- [ ] `meeting_events (meeting_id → events ON DELETE CASCADE, event_id → events ON DELETE CASCADE, position, note TEXT DEFAULT '', PRIMARY KEY (meeting_id, event_id), CHECK (meeting_id <> event_id))`, index on `event_id` for the "Discussed at" back-links
- [ ] Leave the calendar outbox triggers alone: `type` and `minutes` are never sent

### 2. Meeting type on the event form and pages
- [ ] Event form: an **Event / Meeting** radio pair at the top (`/events/new?type=meeting` pre-selects it). `readEvent` validates it
- [ ] Meeting page (`pages/events/show.html`): sections in this order: Agenda, Events discussed, Minutes, Tasks (headed "Action items"), Rooms. Helpers, Needs and Timeline are hidden on meetings unless they already have rows, and can be added from a "More" menu
- [ ] `/events` and dashboard: a **Meetings** label on meeting rows, and an `Events | Meetings | All` filter (`?type=`) on `/events`
- [ ] Dashboard **New meeting** button next to **New event** (planners)
- [ ] Copy (`internal/store/copy.go`): copy `type` and agenda items; never copy `minutes` or `meeting_events`. Series occurrences get the template's agenda through the same copier
- [ ] Tests: create/edit a meeting, type filter, copy keeps the agenda and drops the minutes, series occurrence gets the standing agenda

### 3. Agenda
- [ ] Add, edit, delete and reorder items on the meeting page (reuse the task list's reorder controller and partial pattern). Planners and the meeting's members can edit
- [ ] Item fields: title, details, **Carry over to next meeting** checkbox
- [ ] Print-friendly agenda view (`/events/{id}/agenda`): title, date, rooms, agenda, events to discuss. Plain CSS `@media print`, no PDF dependency
- [ ] Tests: CRUD, reorder, permission (helpers who aren't members can't edit)

### 4. Events discussed
- [ ] **Add an event** picker: search by title over events that aren't meetings, newest first, with not-scheduled and `planning` events at the top. Already-linked ones are left out
- [ ] **New idea** quick-add: a title only. It creates a `planning` event with no date, the same `locked` and members as the meeting, and links it. Its page says "Came up at: *meeting title, date*"
- [ ] Each row shows the event's title, stage (Not scheduled yet / Pencilled in / date), status, open-task count, coordinator, and the discussion note (editable inline). The row can be reordered or removed (removing only unlinks)
- [ ] **Add task** on a row creates the task on *that event*, not the meeting, so follow-ups land where the work happens
- [ ] Event page: a **Discussed at** list of meetings that link it, with each note
- [ ] Visibility: a locked event the viewer can't see shows as "Private event" with no title or link. A locked meeting the viewer can't see is left out of "Discussed at"
- [ ] **Bring over from last meeting** (see *Carrying things over*): skips events already linked, keeps the old notes as "Last time: …" until replaced
- [ ] Tests: link/unlink, new idea is undated + inherits lock and members, task on a linked event, back-links, locked rows hidden, bring-over picks the right previous meeting and skips done/cancelled

### 5. Minutes and closing out
- [ ] Minutes textarea on the meeting page, always editable by planners and members. Before the meeting starts it's collapsed under "Minutes (after the meeting)"
- [ ] Close-out prompt for **every** event, not only meetings: once an event's end has passed and its status is `planning` or `ready`, the event page shows **Mark as done / Mark as cancelled**. For meetings, **Mark as done** opens the minutes if they're empty
- [ ] Dashboard: a **Needs closing out** group for past events still `planning`/`ready` (planners only)
- [ ] Tests: prompt shows only after the end time, dashboard group, minutes never in `/api/v1/events`, `.ics` or the calendar push payload

### 6. Docs
- [ ] README: meetings, agenda, events discussed, minutes, closing out
- [ ] `.claude/CLAUDE.md`: agenda, minutes and discussion notes are internal like `notes`; never sent to the calendar or API

## Open questions
- **Attendance**: track who came (an `attended` flag per member, or a small attendees list)? Left out until it's needed. "Done" plus minutes covers "the meeting happened."
- **Minutes approval**: do any committees approve last meeting's minutes? If so, add a `minutes_approved_at` later. It isn't worth it now.
