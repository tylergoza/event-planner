-- Meetings (MEETINGS_PLAN.md): an event with a type, an agenda, minutes and
-- a list of the events it discussed.
--
-- type is 'event' or 'meeting'; kind stays the free-text label. minutes is
-- internal text, like notes. Neither is sent to the calendar, so the
-- calendar outbox triggers are left as they are.
ALTER TABLE events ADD COLUMN type TEXT NOT NULL DEFAULT 'event' CHECK (type IN ('event', 'meeting'));
ALTER TABLE events ADD COLUMN minutes TEXT NOT NULL DEFAULT '';

-- The ordered things a meeting will cover. carry_over = 1 asks for the item
-- to be copied to the next meeting by "Bring over from last meeting".
CREATE TABLE agenda_items (
    id         INTEGER PRIMARY KEY,
    event_id   INTEGER NOT NULL REFERENCES events(id) ON DELETE CASCADE,
    position   INTEGER NOT NULL,
    title      TEXT NOT NULL CHECK (title <> ''),
    details    TEXT NOT NULL DEFAULT '',
    carry_over INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX agenda_items_event ON agenda_items(event_id, position);

-- The events a meeting discussed, each with a short note on what was said.
-- Removing a link never touches the event.
CREATE TABLE meeting_events (
    meeting_id INTEGER NOT NULL REFERENCES events(id) ON DELETE CASCADE,
    event_id   INTEGER NOT NULL REFERENCES events(id) ON DELETE CASCADE,
    position   INTEGER NOT NULL,
    note       TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (meeting_id, event_id),
    CHECK (meeting_id <> event_id)
);
-- For the "Discussed at" back-links on an event.
CREATE INDEX meeting_events_event ON meeting_events(event_id);
