-- What the church calendar said clashed with an event the last time it was
-- sent (CALENDAR_PLAN.md step 4). The push is asynchronous, so the worker
-- keeps the answer here for the event's page. conflicts is JSON, opaque to
-- the store; no row means no clashes (or nothing sent yet).
CREATE TABLE calendar_conflicts (
    event_id   INTEGER PRIMARY KEY REFERENCES events(id) ON DELETE CASCADE,
    conflicts  TEXT NOT NULL,
    checked_at TEXT NOT NULL DEFAULT (datetime('now'))
);
