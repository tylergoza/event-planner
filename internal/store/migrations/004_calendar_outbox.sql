-- Events waiting to be sent to the church calendar (CALENDAR_PLAN.md).
-- One row per event: a newer change replaces an older waiting one. The
-- worker builds what to send from the event as it is when it sends, so a
-- row only says "this event changed". No foreign key: a deleted event
-- still needs its DELETE sent.
--
-- The triggers below queue a row in the same transaction as any change the
-- calendar shows (times, title, rooms, status, visibility, lock), from
-- whichever store function makes it: edits, copies, series top-ups, skips.
-- Rows queue even while no calendar is set up; there's at most one per
-- event, and connecting sends them.
CREATE TABLE calendar_outbox (
    event_id    INTEGER PRIMARY KEY,
    op          TEXT    NOT NULL CHECK (op IN ('put', 'delete')),
    -- Bumped on every queueing, so the worker only clears the row it sent
    -- and not a newer change made while it was sending.
    seq         INTEGER NOT NULL,
    attempts    INTEGER NOT NULL DEFAULT 0,
    next_try_at TEXT    NOT NULL DEFAULT (datetime('now')),
    last_error  TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX calendar_outbox_due ON calendar_outbox(next_try_at);

CREATE TRIGGER calendar_event_insert AFTER INSERT ON events BEGIN
    INSERT OR REPLACE INTO calendar_outbox (event_id, op, seq)
    VALUES (NEW.id, 'put', (SELECT COALESCE(MAX(seq), 0) + 1 FROM calendar_outbox));
END;

CREATE TRIGGER calendar_event_update
AFTER UPDATE OF title, kind, status, starts_on, start_time, ends_on, end_time, visibility, public_description, locked ON events
WHEN OLD.title IS NOT NEW.title OR OLD.kind IS NOT NEW.kind OR OLD.status IS NOT NEW.status
  OR OLD.starts_on IS NOT NEW.starts_on OR OLD.start_time IS NOT NEW.start_time
  OR OLD.ends_on IS NOT NEW.ends_on OR OLD.end_time IS NOT NEW.end_time
  OR OLD.visibility IS NOT NEW.visibility OR OLD.public_description IS NOT NEW.public_description
  OR OLD.locked IS NOT NEW.locked
BEGIN
    INSERT OR REPLACE INTO calendar_outbox (event_id, op, seq)
    VALUES (NEW.id, 'put', (SELECT COALESCE(MAX(seq), 0) + 1 FROM calendar_outbox));
END;

CREATE TRIGGER calendar_event_delete AFTER DELETE ON events BEGIN
    INSERT OR REPLACE INTO calendar_outbox (event_id, op, seq)
    VALUES (OLD.id, 'delete', (SELECT COALESCE(MAX(seq), 0) + 1 FROM calendar_outbox));
END;

-- Rooms. Deleting an event deletes its rooms too; those mustn't turn its
-- 'delete' back into a 'put', hence the checks that the event is still there.
CREATE TRIGGER calendar_room_insert AFTER INSERT ON event_rooms BEGIN
    INSERT OR REPLACE INTO calendar_outbox (event_id, op, seq)
    VALUES (NEW.event_id, 'put', (SELECT COALESCE(MAX(seq), 0) + 1 FROM calendar_outbox));
END;

CREATE TRIGGER calendar_room_update AFTER UPDATE OF place_id, place_name ON event_rooms
WHEN OLD.place_id IS NOT NEW.place_id OR OLD.place_name IS NOT NEW.place_name
BEGIN
    INSERT OR REPLACE INTO calendar_outbox (event_id, op, seq)
    VALUES (NEW.event_id, 'put', (SELECT COALESCE(MAX(seq), 0) + 1 FROM calendar_outbox));
END;

CREATE TRIGGER calendar_room_delete AFTER DELETE ON event_rooms
WHEN EXISTS (SELECT 1 FROM events WHERE id = OLD.event_id)
BEGIN
    INSERT OR REPLACE INTO calendar_outbox (event_id, op, seq)
    VALUES (OLD.event_id, 'put', (SELECT COALESCE(MAX(seq), 0) + 1 FROM calendar_outbox));
END;
