-- Events not scheduled yet, and pencilled-in dates (BOOKING_PLAN.md).
--
-- starts_on stays NOT NULL: '' means "not scheduled yet" (no table
-- rebuild). Such an event is left out of everything laid out by date and is
-- never sent to the calendar (a date it had before is deleted there).
--
-- date_tentative is 1 while the date is pencilled in: held, but not
-- certain. It's sent to the calendar as status 'tentative'.
ALTER TABLE events ADD COLUMN date_tentative INTEGER NOT NULL DEFAULT 0;

-- The 004 update trigger, now also watching date_tentative.
DROP TRIGGER calendar_event_update;

CREATE TRIGGER calendar_event_update
AFTER UPDATE OF title, kind, status, starts_on, start_time, ends_on, end_time, visibility, public_description, locked, date_tentative ON events
WHEN OLD.title IS NOT NEW.title OR OLD.kind IS NOT NEW.kind OR OLD.status IS NOT NEW.status
  OR OLD.starts_on IS NOT NEW.starts_on OR OLD.start_time IS NOT NEW.start_time
  OR OLD.ends_on IS NOT NEW.ends_on OR OLD.end_time IS NOT NEW.end_time
  OR OLD.visibility IS NOT NEW.visibility OR OLD.public_description IS NOT NEW.public_description
  OR OLD.locked IS NOT NEW.locked OR OLD.date_tentative IS NOT NEW.date_tentative
BEGIN
    INSERT OR REPLACE INTO calendar_outbox (event_id, op, seq)
    VALUES (NEW.id, 'put', (SELECT COALESCE(MAX(seq), 0) + 1 FROM calendar_outbox));
END;
