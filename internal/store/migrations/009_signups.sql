-- Sign-up forms on events (SIGNUPS_PLAN.md steps 1-5). One form per event
-- with questions and choices; sign-ups hold people and answers. Answers
-- never leave this database except spots left and "who's bringing what".
--
-- Spots taken and the headcount are worked out when read, not stored.

-- One form per event. code is random and goes in the public URL, so
-- event IDs aren't guessable from it.
CREATE TABLE signup_forms (
    event_id      INTEGER PRIMARY KEY REFERENCES events(id) ON DELETE CASCADE,
    code          TEXT NOT NULL UNIQUE,
    label         TEXT NOT NULL DEFAULT '',  -- button text, '' = "Sign up"
    intro         TEXT NOT NULL DEFAULT '',
    thank_you     TEXT NOT NULL DEFAULT '',
    status        TEXT NOT NULL DEFAULT 'draft'
                  CHECK (status IN ('draft', 'open', 'closed')),
    audience      TEXT NOT NULL DEFAULT 'anyone'
                  CHECK (audience IN ('anyone', 'signed_in')),
    contact       TEXT NOT NULL DEFAULT 'phone'
                  CHECK (contact IN ('phone', 'email', 'phone_or_email', 'none')),
    people        TEXT NOT NULL DEFAULT 'self'
                  CHECK (people IN ('self', 'list')),  -- who's coming: just me / a list
    ask_guests    INTEGER NOT NULL DEFAULT 0,   -- 1: "and how many more guests?" (unnamed)
    ask_allergies INTEGER NOT NULL DEFAULT 0,   -- 1: allergies and dietary needs, per person
    closes_at     TEXT NOT NULL DEFAULT '',     -- YYYY-MM-DDTHH:MM, '' = when the event starts
    show_spots    INTEGER NOT NULL DEFAULT 1,   -- spots left on the public form
    -- 1 once the sweep has queued the calendar push for a form that closed
    -- by itself (closing time or event start passed). Reset to 0 whenever
    -- the form is saved.
    closed_pushed INTEGER NOT NULL DEFAULT 0,
    created_at    TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at    TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE signup_questions (
    id          INTEGER PRIMARY KEY,
    event_id    INTEGER NOT NULL REFERENCES signup_forms(event_id) ON DELETE CASCADE,
    position    INTEGER NOT NULL,
    kind        TEXT NOT NULL
                CHECK (kind IN ('one', 'many', 'text', 'long_text', 'number', 'yes_no')),
    label       TEXT NOT NULL CHECK (label <> ''),
    help        TEXT NOT NULL DEFAULT '',
    required    INTEGER NOT NULL DEFAULT 0,
    per_person  INTEGER NOT NULL DEFAULT 0,  -- needs people = 'list'
    public_list INTEGER NOT NULL DEFAULT 0,  -- "who's bringing what"; per-sign-up questions only
    retired     INTEGER NOT NULL DEFAULT 0,  -- answered ones are retired, not deleted
    CHECK (NOT (per_person AND public_list))
);
CREATE INDEX signup_questions_event ON signup_questions(event_id, position);

CREATE TABLE signup_choices (
    id          INTEGER PRIMARY KEY,
    question_id INTEGER NOT NULL REFERENCES signup_questions(id) ON DELETE CASCADE,
    position    INTEGER NOT NULL,
    label       TEXT NOT NULL CHECK (label <> ''),
    spots       INTEGER CHECK (spots IS NULL OR spots > 0),  -- NULL: no limit
    retired     INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX signup_choices_question ON signup_choices(question_id, position);

-- "Only ask when one of these is picked." Empty: always asked. The
-- choices must belong to an earlier question (checked in the store).
CREATE TABLE signup_question_when (
    question_id INTEGER NOT NULL REFERENCES signup_questions(id) ON DELETE CASCADE,
    choice_id   INTEGER NOT NULL REFERENCES signup_choices(id) ON DELETE CASCADE,
    PRIMARY KEY (question_id, choice_id)
);
CREATE INDEX signup_question_when_choice ON signup_question_when(choice_id);

-- "Can't be picked with": Make chili x Judge chili. Stored once (lower
-- ID first), read both ways. Both choices must be on questions with the
-- same per_person, checked in the store.
CREATE TABLE signup_choice_excludes (
    choice_id INTEGER NOT NULL REFERENCES signup_choices(id) ON DELETE CASCADE,
    other_id  INTEGER NOT NULL REFERENCES signup_choices(id) ON DELETE CASCADE,
    PRIMARY KEY (choice_id, other_id),
    CHECK (choice_id < other_id)
);
CREATE INDEX signup_choice_excludes_other ON signup_choice_excludes(other_id);

-- One per submission. name/phone/email are the signer's: from the
-- account when signed in, typed by visitors.
CREATE TABLE signups (
    id           INTEGER PRIMARY KEY,
    event_id     INTEGER NOT NULL REFERENCES signup_forms(event_id) ON DELETE CASCADE,
    code_hash    TEXT NOT NULL UNIQUE,  -- SHA-256 hex of their status-link code; the code is shown once
    user_id      INTEGER REFERENCES users(id) ON DELETE SET NULL,
    name         TEXT NOT NULL CHECK (name <> ''),
    phone        TEXT NOT NULL DEFAULT '',
    email        TEXT NOT NULL DEFAULT '',
    guests       INTEGER NOT NULL DEFAULT 0 CHECK (guests >= 0),
    status       TEXT NOT NULL DEFAULT 'in' CHECK (status IN ('in', 'withdrawn')),
    planner_note TEXT NOT NULL DEFAULT '',  -- internal, never shown to the person
    created_at   TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at   TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX signups_event ON signups(event_id);
CREATE INDEX signups_user ON signups(user_id);

-- Who the sign-up is for. directory_person_id links a person ticked from
-- the directory (with a cached name); NULL means typed in. Directory notes
-- and ages are never copied here. allergies is only what was typed for
-- this event.
CREATE TABLE signup_people (
    id                  INTEGER PRIMARY KEY,
    signup_id           INTEGER NOT NULL REFERENCES signups(id) ON DELETE CASCADE,
    position            INTEGER NOT NULL,
    name                TEXT NOT NULL CHECK (name <> ''),
    directory_person_id INTEGER,
    allergies           TEXT NOT NULL DEFAULT ''
);
CREATE INDEX signup_people_signup ON signup_people(signup_id);

-- Picked choices are one row each (choice_id set, value ''); text,
-- number and yes/no answers are one row with value. person_id is set
-- for per-person questions, NULL otherwise.
CREATE TABLE signup_answers (
    signup_id   INTEGER NOT NULL REFERENCES signups(id) ON DELETE CASCADE,
    person_id   INTEGER REFERENCES signup_people(id) ON DELETE CASCADE,
    question_id INTEGER NOT NULL REFERENCES signup_questions(id) ON DELETE CASCADE,
    choice_id   INTEGER REFERENCES signup_choices(id) ON DELETE CASCADE,
    value       TEXT NOT NULL DEFAULT ''
);
CREATE UNIQUE INDEX signup_answers_one
    ON signup_answers (signup_id, COALESCE(person_id, 0), question_id, COALESCE(choice_id, 0));
CREATE INDEX signup_answers_question ON signup_answers(question_id);
CREATE INDEX signup_answers_choice ON signup_answers(choice_id);

-- A form change the calendar shows (button text, status, closing time)
-- queues the event, the same way the 004 triggers do. A state change from
-- answers (open <-> full) is queued by the store inside the sign-up's
-- transaction instead, so a plain answer queues nothing. Deleting an event
-- deletes its form too; that mustn't turn the event's 'delete' back into a
-- 'put', hence the check that the event is still there.
CREATE TRIGGER calendar_signup_form_insert AFTER INSERT ON signup_forms BEGIN
    INSERT OR REPLACE INTO calendar_outbox (event_id, op, seq)
    VALUES (NEW.event_id, 'put', (SELECT COALESCE(MAX(seq), 0) + 1 FROM calendar_outbox));
END;

CREATE TRIGGER calendar_signup_form_update AFTER UPDATE OF status, label, closes_at ON signup_forms
WHEN OLD.status IS NOT NEW.status OR OLD.label IS NOT NEW.label OR OLD.closes_at IS NOT NEW.closes_at
BEGIN
    INSERT OR REPLACE INTO calendar_outbox (event_id, op, seq)
    VALUES (NEW.event_id, 'put', (SELECT COALESCE(MAX(seq), 0) + 1 FROM calendar_outbox));
END;

CREATE TRIGGER calendar_signup_form_delete AFTER DELETE ON signup_forms
WHEN EXISTS (SELECT 1 FROM events WHERE id = OLD.event_id)
BEGIN
    INSERT OR REPLACE INTO calendar_outbox (event_id, op, seq)
    VALUES (OLD.event_id, 'put', (SELECT COALESCE(MAX(seq), 0) + 1 FROM calendar_outbox));
END;
