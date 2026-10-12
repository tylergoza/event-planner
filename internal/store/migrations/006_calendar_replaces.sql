-- The calendar booking an event was made from ("Make this an event" on the
-- church calendar). Sent as `replaces` with the event's first push so the
-- calendar swaps its hand-made booking for this event, then cleared. Not in
-- the 004 update trigger's column list, so clearing it queues nothing.
ALTER TABLE events ADD COLUMN calendar_replaces INTEGER;
