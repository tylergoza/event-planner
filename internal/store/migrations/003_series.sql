-- Repeating events. A series holds the rule; each occurrence is a real
-- event (its own tasks, rooms, helper jobs, needs and timeline), created
-- ahead of time by copying the series' template event.
CREATE TABLE series (
    id                INTEGER PRIMARY KEY,
    freq              TEXT    NOT NULL CHECK (freq IN ('daily', 'weekly', 'monthly')),
    every             INTEGER NOT NULL DEFAULT 1 CHECK (every > 0),  -- every N days/weeks/months
    weekdays          TEXT    NOT NULL DEFAULT '',  -- weekly: "0,3" (0 = Sunday); '' = starts_on's weekday
    month_day         INTEGER CHECK (month_day BETWEEN 1 AND 31),  -- monthly by date (short months use their last day)
    month_week        INTEGER CHECK (month_week IN (1, 2, 3, 4, -1)),  -- monthly by weekday: 1st..4th, -1 = last
    month_weekday     INTEGER CHECK (month_weekday BETWEEN 0 AND 6),
    starts_on         TEXT    NOT NULL,             -- YYYY-MM-DD, the rule's anchor
    until             TEXT    NOT NULL DEFAULT '',  -- last date it may fall on; '' = no end date
    max_count         INTEGER CHECK (max_count IS NULL OR max_count > 0),  -- or stop after this many, counted from starts_on
    -- The event new occurrences are copied from. NULL (it was deleted):
    -- the latest remaining occurrence is used instead.
    template_event_id INTEGER REFERENCES events(id) ON DELETE SET NULL,
    -- Occurrences exist up to this date; topped up as time passes.
    generated_through TEXT    NOT NULL DEFAULT '',
    created_at        TEXT    NOT NULL DEFAULT (datetime('now')),
    CHECK (freq <> 'monthly' OR (month_day IS NULL) <> (month_week IS NULL)),
    CHECK ((month_week IS NULL) = (month_weekday IS NULL))
);

-- An occurrence remembers the date the rule gave it, even if it was moved
-- to another day, so the rule never makes a second one for that date.
ALTER TABLE events ADD COLUMN series_id INTEGER REFERENCES series(id) ON DELETE SET NULL;
ALTER TABLE events ADD COLUMN occurrence_date TEXT NOT NULL DEFAULT '';
CREATE UNIQUE INDEX events_occurrence ON events(series_id, occurrence_date) WHERE series_id IS NOT NULL;

-- Dates skipped (their occurrence deleted on its own), so topping up
-- doesn't bring them back. Restoring one removes the row and recreates it.
CREATE TABLE series_skips (
    series_id INTEGER NOT NULL REFERENCES series(id) ON DELETE CASCADE,
    date      TEXT    NOT NULL,
    PRIMARY KEY (series_id, date)
);
