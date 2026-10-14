# Event Planner: sign-ups

The design is in `../.claude/plans/event-signups.md`; read its Decisions
first. This file is the event planner's part. Everything up to step 5
works with no other app connected; step 6 adds the member directory.

Status: planned (2026-10-14, updated the same day with the user's
answers), nothing built yet.

## How it fits with what's here

| Already here | Stays | Sign-ups add |
|--------------|-------|--------------|
| `helper_roles` / `helpers` | Internal volunteer slots for people signed in | Nothing; helpers aren't moved into forms |
| `expected_count` | The planner's guess | The headcount from sign-ups (people + guests) shown next to it |
| `event_members` | The event's team | The team (plus planners and admins) sees people's details and the allergy list |
| Public calendar `/calendar/{id}` | Visibility rules unchanged | A **Sign up** button when the form is open to that viewer |
| Calendar push | Same outbox and trigger pattern | An optional `signup` object |

## Words used here

- **Sign-up**: one submission, made by one **signer** (a signed-in user,
  or a visitor with a name and contact).
- **People**: who the sign-up is for. Always at least one. With
  **Who's coming: just me** it's the signer; with **a list** the signer
  ticks people from their household (directory connected) and/or types
  names. Children's events use the list.
- **Per-person question**: asked once for each person ("Which class?").
  Other questions are asked once per sign-up ("What are you bringing?").

## Data (migration `0NN_signups.sql`: the next free number; meetings plans `008`)

```sql
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
    closes_at     TEXT NOT NULL DEFAULT '',     -- '' = when the event starts
    show_spots    INTEGER NOT NULL DEFAULT 1,   -- spots left on the public form
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

CREATE TABLE signup_choices (
    id          INTEGER PRIMARY KEY,
    question_id INTEGER NOT NULL REFERENCES signup_questions(id) ON DELETE CASCADE,
    position    INTEGER NOT NULL,
    label       TEXT NOT NULL CHECK (label <> ''),
    spots       INTEGER CHECK (spots IS NULL OR spots > 0),  -- NULL: no limit
    retired     INTEGER NOT NULL DEFAULT 0
);

-- "Only ask when one of these is picked." Empty: always asked. The
-- choices must belong to an earlier question (checked in the store).
CREATE TABLE signup_question_when (
    question_id INTEGER NOT NULL REFERENCES signup_questions(id) ON DELETE CASCADE,
    choice_id   INTEGER NOT NULL REFERENCES signup_choices(id) ON DELETE CASCADE,
    PRIMARY KEY (question_id, choice_id)
);

-- "Can't be picked with": Make chili x Judge chili. Stored once (lower
-- ID first), read both ways. Both choices must be on questions with the
-- same per_person, checked in the store.
CREATE TABLE signup_choice_excludes (
    choice_id INTEGER NOT NULL REFERENCES signup_choices(id) ON DELETE CASCADE,
    other_id  INTEGER NOT NULL REFERENCES signup_choices(id) ON DELETE CASCADE,
    PRIMARY KEY (choice_id, other_id),
    CHECK (choice_id < other_id)
);

-- One per submission. name/phone/email are the signer's: from the
-- account when signed in, typed by visitors.
CREATE TABLE signups (
    id           INTEGER PRIMARY KEY,
    event_id     INTEGER NOT NULL REFERENCES signup_forms(event_id) ON DELETE CASCADE,
    code_hash    TEXT NOT NULL UNIQUE,  -- their status link; the code itself is shown once
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

-- Who the sign-up is for. directory_person_id links a person ticked from
-- the directory (with a cached name, the constellation rule); NULL means
-- typed in. Directory notes and ages are never copied here: they're read
-- when a page needs them. allergies is only what was typed for this event.
CREATE TABLE signup_people (
    id                  INTEGER PRIMARY KEY,
    signup_id           INTEGER NOT NULL REFERENCES signups(id) ON DELETE CASCADE,
    position            INTEGER NOT NULL,
    name                TEXT NOT NULL CHECK (name <> ''),
    directory_person_id INTEGER,
    allergies           TEXT NOT NULL DEFAULT ''
);

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
```

### Rules the store keeps

- **Spots taken** = answers for that choice on `in` sign-ups. A
  per-person choice takes one spot per person who picks it (three
  children in the craft class take three). Worked out when read, like
  conflicts. Saving a sign-up runs `BEGIN IMMEDIATE`, re-counts the picked
  choices and refuses with "Make chili just filled up" if one is now
  full. The rest of what they typed stays on the form.
- **Can't be picked with** is checked within one sign-up (per-sign-up
  questions) or within one person (per-person questions): "You can't pick
  both Make chili and Judge chili." Exclusions can link choices on the
  same or different questions.
- **Planners can go over** spots and exclusions when they add or edit a
  sign-up by hand, with a warning ("Over by 1", "Also judging chili"), the
  way the calendar flags equipment shortages. The public form never can.
- **A directory person is signed up once per event.** Ticking someone
  already on another sign-up (one parent signed the children up, then the
  other tries) shows "Sam is already signed up by Jane" and leaves them
  unticked. Typed names aren't checked; planners can merge.
- **Effective state** (for pages and the push): `draft`; `closed` when
  status is closed, `closes_at` (or the event's start) has passed, or the
  event is `cancelled` / not scheduled; `full` when every choice on every
  required pick question is full; else `open`.
- Deleting a question or choice that has answers is refused; the UI
  offers **Retire** instead. Retiring a choice keeps its exclusions for
  the answers already given.
- **Headcount** = people on `in` sign-ups + their `guests`.

## Who sees what

| What | Public (per audience) | Signer | Signed in | Event team (`event_members`, planners, admins) |
|------|------|--------|-----------|------------|
| Spots left (when `show_spots`) | yes | yes | yes | yes |
| "Who's bringing what" (questions with **Show on the form**) | signer's first name + last initial and those answers | yes | yes | yes |
| Their own answers and people | | yes | | yes |
| The list of sign-ups, counts, answers | | | yes | yes |
| People's details from the directory (age, household) and the **allergy list** | | | | yes |
| Shared directory notes in the CSV | | | | never |

- **Who's bringing what** shows on the form page only, grouped by choice
  ("Make chili: Tyler G. · *Smoky three-bean*, Jane D."), from `in`
  sign-ups. Only per-sign-up questions can be shown, under the signer's
  name, so children's names never appear on a public page. A ticked
  question says on the form: "Your first name, last initial and this
  answer show on this page." It follows the form's audience (signed-in
  forms show it only to signed-in people) and never goes to the calendar.

## Allergies and dietary needs

Turned on with **Ask about allergies and dietary needs** on the form.
It's per person, so it needs nothing else set up, and it works the same
way for members and visitors on the same form:

| Person | What the form shows | Where it comes from |
|--------|--------------------|---------------------|
| Typed in (visitor, or a member without the directory) | "Allergies or dietary needs" box | `signup_people.allergies` |
| Ticked from the directory, with shared notes | "From Sam's profile: *Peanuts (allergy)*. Change it in the directory." and an optional "Anything else for this event?" box | Directory notes with **Share with sign-ups**, read live; the box is `signup_people.allergies` |
| Ticked from the directory, no shared notes | "Nothing on Sam's profile." and the box | Same |

**The allergy list** (`/events/{id}/allergies`, event team): everyone on
`in` sign-ups with anything from either source, with their age (if the
directory knows it), who signed them up and that person's phone, and
where each line came from (*profile* / *typed for this event*). It
prints, because the kitchen or the children's leaders need it on paper
on the day. Print it before the event: if the directory is down, the
page says "Couldn't reach the directory: allergies from profiles are
missing" and shows only what was typed.

Directory notes are never stored here, never in the CSV, never logged.

## Pages

| Page | Who | What |
|------|-----|------|
| `/events/{id}/edit`, **Sign-up** section | planners | **Add a sign-up form**, then the form settings (label, intro, who can sign up, contact, who's coming, guests, allergies, closes, thank you, status) and the questions. Each question and its choices is its own small form below the event form (forms can't nest), with move up/down like helper roles. Per question: per person, **Show on the form**, "Only ask when". Per choice: spots and **Can't be picked with** (pick other choices). Choices can be pasted one per line, `Make chili | 12`. **Preview** opens the public form, even as a draft |
| `/events/{id}/signups` | signed in | Counts per choice (*9 / 12*, **Full**), headcount next to expected, the list by sign-up or **by person**, a filter per choice, withdrawn hidden by default, **Print**, **CSV** (`encoding/csv`). Planners edit or withdraw anyone's sign-up and add one by hand (phoned in). The team also sees each person's age and household from the directory |
| `/events/{id}/allergies` | event team | The allergy list, printable |
| `/events/{id}` | signed in | A line under the event: "Sign-ups: 23 people, open until Nov 1" linking to the list, and **Allergies (4)** for the team |
| `/signup/{code}` | public, by audience | The form and the "who's bringing what" lists. Signed in: name from the account, no contact asked, and with the directory connected the household to tick. Shows spots left when `show_spots`. Works without JS |
| `POST /signup/{code}` | | Rate limited, CSRF, a honeypot field. Visitors: name and the contact the form asks for |
| `/signup/{code}/s/{signup code}` | the signer | Their answers and people, **Change**, **Withdraw**, while the form isn't closed |
| `/my/signups` | signed in | Their sign-ups, upcoming first |
| `/calendar/{id}` | public calendar | **Sign up** button when the effective state is `open` and the viewer is in the audience; "Full" or "Sign-ups closed" otherwise |

`isPublicPath` gains `/signup/`. It still has a session and CSRF (it's a
form), so it isn't `isCookieFree`.

## Stimulus

- `signup_controller.js`: shows and hides follow-up questions, greys out
  full choices and choices that can't be picked with one already picked
  ("Can't pick with Make chili"), and adds a block of per-person
  questions for each person ticked or typed (**Add another person**).
  Without JS: follow-ups show with "(only if you picked …)", and a
  **list** form shows three empty person rows plus **Add more people**
  (a plain submit that comes back with more rows). The server is the
  real check.
- `signup_builder_controller.js` (optional): the "Only ask when" and
  "Can't be picked with" pickers list the right choices. Work as plain
  multi-selects without it.

## Steps

### 1. Forms and questions (planners, no public page yet)
- [ ] Migration `0NN_signups.sql` (next free number); store: form, questions, choices, `signup_question_when`, `signup_choice_excludes`, positions, retire instead of delete when answered
- [ ] **Sign-up** section on `/events/{id}/edit`: add form, settings, questions (per person, show on the form), choices with spots and **Can't be picked with**, paste, move, "Only ask when"
- [ ] Copying an event or a series occurrence copies the form, questions and exclusions (new `code`, no answers, status `draft`)
- [ ] Tests: "Only ask when" refuses a later question's choice, an exclusion across per-person and per-sign-up questions is refused, per-person + show on the form refused, retire vs delete, copy gets a new code and no answers

### 2. The public form
- [ ] `/signup/{code}`, `POST`, status link page, `/my/signups`; audience check (anyone needs a public, unlocked event)
- [ ] Who's coming: just me / a list (typed names, **Add another person**), guests; per-person question blocks; the allergies box per person
- [ ] Effective state (draft / open / full / closed) in one function, used everywhere
- [ ] In `BEGIN IMMEDIATE`: spots (per person for per-person choices), can't-be-picked-with (per sign-up or per person); follow-up answers that don't apply are dropped; required checks skip questions that don't apply
- [ ] `signup_controller.js`; page works without JS
- [ ] Rate limit, honeypot, code shown once and stored hashed
- [ ] **Sign up** button on the event planner's own `/calendar/{id}`
- [ ] Tests: last spot taken by two at once (one wins), three people picking a 2-spot choice refused, Make chili + Judge chili refused on the form, a locked or private event refuses visitors, closes at the event start, withdrawing frees the spots, follow-ups ignored when not picked

### 3. Responses and the allergy list
- [ ] `/events/{id}/signups`: counts, headcount (people + guests) vs `expected_count`, by sign-up / by person, filter per choice, print CSS
- [ ] CSV: one row per person, sign-up questions repeated on each of its people's rows, picked choices joined with "; ", retired questions last, typed allergies included, directory notes never
- [ ] Planners add, edit and withdraw sign-ups; going over spots or exclusions warns and saves; `planner_note`
- [ ] `/events/{id}/allergies` (team only) from typed allergies; printable
- [ ] Line on `/events/{id}` and a count on the dashboard for open forms
- [ ] Tests: CSV columns with retired questions, planner over-spots warning, allergy page refused to a signed-in user who isn't on the team, answers never on `/calendar`, `/api/v1/events` or `.ics`

### 4. Who's bringing what
- [ ] **Show on the form** lists on `/signup/{code}`, grouped by choice, first name + last initial, `in` only, the notice on ticked questions, following the form's audience
- [ ] Tests: withdrawn sign-ups gone from the list, a signed-in-only form's list hidden from visitors, per-person answers and people's names never listed

### 5. Embed and the calendar link
- [ ] `/signup/{code}?embed=1`: no nav, same form. `frame-ancestors` lists the sites in a **Sites that can show sign-up forms** setting (default none, so `'none'` stays the rule); it only changes on `/signup/` paths
- [ ] `calendar.Booking` gains `Signup *Signup \`json:"signup,omitempty"\`` with `label`, `url`, `state`, `closes_at`; left out for drafts, locked events and when there's no form (shows once `../calendar/SIGNUPS_PLAN.md` step 1 is built; harmless before)
- [ ] Triggers on `signup_forms` writing the event's outbox row; a state change from answers (open → full, full → open) also writes it, inside the sign-up's transaction; a plain answer doesn't
- [ ] A sweep (with the 6-hourly series top-up) pushes forms whose `closes_at` has passed
- [ ] Tests: CSP on embed vs other pages; outbox written on state change only

### 6. People from the directory (needs `../member_directory/PLAN.md` step 14)
- [ ] **Directory** connection on `/admin/connections` (address + app token), the `Configured()` pattern; off = steps 1–5 exactly as they are
- [ ] Signed-in signer with a linked profile: their household to tick (`GET /api/v1/people/by-user/{sub}`), stored as `signup_people` with `directory_person_id` and a cached name; typed names still allowed alongside. **Just me** forms use their own profile
- [ ] "Already signed up by …" check for directory people, per event
- [ ] Allergies on the form: shared notes shown read-only for ticked people, plus the box
- [ ] Event team pages: age, household and shared `allergy` / `dietary` / `medical` notes read with one `GET /api/v1/people?ids=…&notes=…` per page, cached in memory for a few minutes, never written to the database; the "Couldn't reach the directory" banner
- [ ] Tests (fake directory server): household ticks, the same child refused on a second sign-up, notes absent from the CSV and the database, directory down shows the banner and the typed allergies

## Things not to do (yet)

- Waiting lists, payments, email or text confirmations and reminders.
- More than one form per event.
- Matching a visitor's typed names to directory people. Planners can
  read the list; a **Match to directory person** button needs a search in
  the directory's API, which isn't planned.
- Writing back to the directory (a typed allergy doesn't update a profile).
- A form library. **Copy event** and copying from the series template cover reuse.
