# Event Planner: members area (the `member` role and `/api/v1/me/…`)

The cross-app design is in `../.claude/plans/members-site.md`; this is the
event planner's part. The website (static, `www.<zone>`) signs people in
with User Management, gets a short-lived `umd_…` token for this app and
calls `events.<zone>/api/v1/me/…` straight from the browser. The planner
itself stays the back office.

Status: planned 2026-10-16, not built. Depends on `SIGNUPS_PLAN.md`:
steps 1-5 are built (2026-10-14), so the `/me` sign-up API in steps 5-6
below can start. Anything that needs the household to tick (step 6 of
`SIGNUPS_PLAN.md`, the directory connection) is **not built**, so until it
is the `/me` API takes typed names only, exactly as the public form does,
and gains the household list when that step lands. Also depends on User
Management's token check (`GET /api/v1/token`) from its own
`MEMBERS_PLAN.md`; steps 1-3 here don't need it.

From the first deploy on, `001_initial.sql` is frozen. The next free
migration is **`010`** (`009_signups.sql` is the latest).

## What changes, in short

| | Now | After |
|--|-----|-------|
| Roles | `helper`, `planner`, `admin` (`users.role` CHECK, `store.Roles`, `RoleFromSSO`) | A new **`member`** below helper. Unknown or empty SSO roles still become `helper`; only the exact string `member` becomes `member` |
| What a signed-in user can open | Any signed-in user reaches every `auth(...)` page (events, tasks, helpers, needs, notes) | `auth(...)` means **staff** (helper and up). A `member` gets only `/my/signups…`, `/signup/{code}`, `/account`, the public calendar and `/signed-out`. Everything else is a 403 error page |
| The planner's calendar for a member | Signed-in users see private events' details | A `member` sees the **public** calendar view (`CalendarPublic`), not the staff view |
| Tokens | `ep_…` API tokens (read-only, `/api/v1/events`) | Also `umd_…` Bearer tokens, **only** on `/api/v1/me/…`, checked with User Management. `ep_` tokens never work on `/me`, and `umd_` never works on `/api/v1/events` |
| API | `GET /api/v1/events…`, `.ics` | Adds `/api/v1/me/signups…` (open sign-ups, a form, sign up, my sign-ups, change, cancel) |
| Sign-up logic | `store.SaveSignup`, `WithdrawSignup`, `SignupsByUser`, `SignupState` used by the web handlers | **Same functions**, called from the `/me` handlers. No second copy of spots, exclusions or "already signed up" |
| Cross-origin | None (same-origin pages; embed origins only for frames) | CORS on `/api/v1/me/` for the one website origin set on the User Management connection, never `*` |
| Connection page | SSO fields: address, client ID, secret, base URL | Adds **Website origin** (CORS) and **Accepts tokens from** (read-only list, see step 4) |
| Off switch | SSO unset = local login only | SSO unset = `/api/v1/me/…` answers 404, nothing else changes |

## Words used here

- **Member API**: the `/api/v1/me/…` routes. One person, one token, no cookies.
- **Capability**: what a role may do, asked in one place (`internal/server/caps.go`),
  so the handlers never test `Role == "member"` themselves. Today:
  `Staff` (helper and up: the back office), `Plan`, `Admin`, `MemberAPI`
  (member and up), and `OwnHousehold` (member, later guest).
- **Website origin**: `https://www.<zone>`, the only origin CORS allows.

## What a `member` sees if they open the planner directly

A member who goes to `events.<zone>` and signs in with User Management
lands on **My sign-ups** (`/my/signups`), not the dashboard. They can open:

| Page | Member |
|------|--------|
| `/calendar`, `/calendar/{id}`, `/calendar.ics` | Public view only: public events in full, private and locked as busy blocks. The **Sign up** button still shows when the form is open to them |
| `/signup/{code}` and its status pages | Yes, the same form visitors and staff use (the web page and the `/me` API share the store) |
| `/my/signups…` | Yes: view, change, withdraw while open |
| `/account` | Yes (sign-out; no password change when SSO is on) |
| `/`, `/events…`, `/series`, `/agenda`, tasks, helpers, needs, rooms, schedule, `/events/{id}/signups…`, `/allergies`, `/live`, `/admin/…` | **No**: 403 "Staff only" page. `/` redirects a member to `/my/signups` instead |
| The menu | Only Calendar, My sign-ups, Account, Sign out (`.User.CanStaff` hides the rest) |

A member never sees a back-office link, so nothing leads to a 403 by
accident. A member who opens a `signed_in` form for a **private** (not
locked) event can see what the form page already shows (title, time, the
form); the event's notes, tasks, helpers and needs stay hidden. Locked
events: no.

## Steps

### 1. The `member` role (migration `010_member_role.sql`)
- [ ] `internal/store/migrations/010_member_role.sql` starting with `-- foreign_keys: off` (the runner's `noForeignKeys`, `internal/store/store.go`): rebuild `users` with `role TEXT NOT NULL DEFAULT 'helper' CHECK (role IN ('member','helper','planner','admin'))`. Copy every column (`id, username, display_name, password_hash, sso_subject, active, created_at, role`), recreate the unique indexes, drop the old table, rename. Six tables point at `users(id)`; the runner's foreign-key check before commit is the proof nothing was lost
- [ ] Default stays `helper`, so every existing row keeps its role and nobody is demoted
- [ ] `internal/store/users.go`: add `RoleMember = "member"`; `Roles = {member, helper, planner, admin}` (least to most); `roleLabels["member"] = "Member"`; `NormalizeRole` keeps `member`; unknown and empty still map to `helper` (so the legacy `user` stays helper)
- [ ] `RoleFromSSO`: `member` -> `member`, `helper`/`user`/anything else -> `helper` (same fail-safe as today: an unknown string is never *more* than helper, and never silently `member`-only, which would lock a legacy user out)
- [ ] `User.CanStaff()` (helper and up), `User.IsMember()`; `CanPlan` and `IsAdmin` unchanged
- [ ] The role picker on `/admin/users/{id}/edit` (`handleUserUpdate`) and the SSO access list (`handleSSOAccessUpdate`, `internal/server/sso_users.go`) list the new role; `UpdateAppUser` can set it in User Management; the Roles list in `sso.AppUsers` already comes from User Management
- [ ] Tests: `internal/store/roles_test.go` extended (`RoleFromSSO("member")`, `"user"`, `""`, `"zzz"`), migration 009 -> 010 on a copy holding users of every role, sessions, helpers and sign-ups that keep their foreign keys

### 2. Capabilities and the staff gate
- [ ] New `internal/server/caps.go`: `type caps struct{ ... }` and `capsOf(u *store.User) caps` from a role table, e.g. `map[string]caps{member: {MemberAPI, OwnHousehold}, helper: {Staff, MemberAPI, OwnHousehold}, ...}`. One table to extend for `guest`
- [ ] `internal/server/routes.go`: the `auth(...)` wrapper becomes `requireStaff`; add `own(...)` (the old `requireUser`, any signed-in role) and use it for `/my/signups…`, `/account…`, `GET /live` stays staff. `plan` and `admin` unchanged. A member on a staff route gets the 403 "Staff only" error page
- [ ] `handleHome`: a signed-in non-staff user is redirected to `/my/signups`
- [ ] `calAccess` (`handlers_calendar.go`): a `member` gets `store.CalendarPublic`, staff get `CalendarSignedIn(u)`
- [ ] `layout.html` and partials: wrap the back-office menu in `{{if .User.CanStaff}}`
- [ ] Handlers that were safe because "signed in = helper" check themselves: `handleEventShow`, `handleSignups`, `handleSignupsCSV`, `handleAllergies`, `handlePeople` call `requireStaff` through the route, so no handler change; check each `store.CanSee` caller that takes a `*User` (`inAudience` in `handlers_signup.go`) for a member
- [ ] Tests: `internal/server/roles_test.go` walks every route in `routes()` as a member (use `mux` pattern list) and expects only the allow-list above to return 200; a member gets 403 on `/events`, `/events/{id}`, `/events/{id}/tasks|helpers|needs|signups|allergies`, `/admin/…`, `/live`; `/` redirects; the calendar page shows no private titles to a member

### 3. Verifying a token (`internal/sso`)
- [ ] `sso.Client.CheckToken(ctx, token string) (*TokenUser, error)`: `GET {internal or public}/api/v1/token` with the app's Basic auth (client ID and secret, as `do` already sends) and an `X-Token: umd_…` header. Parses `{"user": {sub, username, name, email, role}, "via": "website"}`. A 401 returns `ErrTokenInvalid`; `invalid_client` or an outage wraps `ErrUnreachable` (same split as `CheckGrant`)
- [ ] Only tokens starting with `umd_` are sent to User Management; anything else is refused locally without a call
- [ ] A small cache in the server (`internal/server/me_auth.go`): key = SHA-256 of the token, value = the answer, **up to 60 s** and never past the token's own expiry; only good answers are cached; 401 is never cached and always refuses. Same in-flight de-duplication as `grantChecks` (`internal/server/sso.go`)
- [ ] User Management unreachable: refuse with 503 `unavailable` (fail closed; a token that can't be checked is not accepted). A cached good answer may serve until its 60 s is up
- [ ] `via` must be `website`; any other value is refused (this API is for the website)
- [ ] Tests (fake User Management, `httptest`): good answer cached and the second call makes no request, cache ends at 60 s, 401 refused and not cached, a token for another audience (User Management's 401) refused, a non-`umd_` token refused without a call, unreachable gives 503, `via` other than `website` refused

### 4. The connection: origin, off switch, "Accepts tokens from"
- [ ] `internal/server/connections.go`, `connSSO`: add a field **Website origin** (`Env: "SSO_WEBSITE_ORIGIN"`, `Key: "sso_website_origin"`, URL, hint `https://www.example.org`). It lives on the User Management connection, so the existing whole-connection env-or-settings rule and the pending/test-sign-in flow apply. `parseOrigins` (`handlers_admin.go`) validates it: scheme + host only, one origin, no path
- [ ] `Server.memberAPIOrigin()` returns it, or `""`. **`Server.memberAPIOn()`** is true only when SSO is connected (`ssoSignIn()`) **and** an origin is set. Off means every `/api/v1/me/` route answers 404 (not 401, so it doesn't advertise itself) and no CORS headers are sent
- [ ] **Accepts tokens from**: in this design User Management holds the list (the `website` app's "May act for" and each app's "Accepts from", planned in its `MEMBERS_PLAN.md`) and refuses a token for an app that doesn't accept it, with a 401 the planner treats as "refuse". The connection page shows it **read-only** ("Accepts tokens from: website, set in User Management") from the same call the Test button makes, plus a note when the answer is empty. If User Management's check call exposes the list, show it; if not, show a link to the Apps page. Nothing here is a setting that lets the planner accept a token User Management would refuse
- [ ] The **Test** button on `/admin/connections` also reports "Member API: on for https://www.example.org" or "off (no website origin)"
- [ ] Ansible: `deploy/ansible/templates` env template gains `SSO_WEBSITE_ORIGIN` from an optional var in `vars.example.yml`; unset = absent
- [ ] Tests: `memberAPIOn` false with SSO unset, true with SSO and an origin, false without an origin; `ConnectionEnv()` lists the new variable; saving an origin with a path is refused

### 5. The member API, middleware and CORS
- [ ] `internal/server/api_me.go`: `meRoutes(mux)`, registered from `apiRoutes` so `/api/v1/me/` stays under `isCookieFree` (no session, no CSRF, no cookie set; `Vary: Origin`; every response `Cache-Control: private, no-store`)
- [ ] `s.meHandler(h)` = `memberAPIOn` (else 404) -> CORS -> token check (step 3) -> local user -> capability check -> handler. **Not** `requireToken` (that one is for `ep_` tokens)
- [ ] **CORS**: the request's `Origin` must equal `memberAPIOrigin()` exactly. If so: `Access-Control-Allow-Origin: <that origin>`, `Allow-Methods: GET, POST, PUT, DELETE`, `Allow-Headers: Authorization, Content-Type, Idempotency-Key`, `Max-Age: 600`, `Vary: Origin`. Never `*`, never `Allow-Credentials` (no cookies). A preflight `OPTIONS` from the right origin answers 204 without a token. Any other `Origin` gets **no** CORS headers (the browser blocks it) and, for a non-preflight request, a 403 `origin_not_allowed`; a request with no `Origin` header (curl) is still checked by its token
- [ ] **No cookies**: the `/me` path never reads `ep_session` or `ep_csrf` and never sets a cookie, even if the browser sends one. A request carrying only a cookie and no Bearer is a 401
- [ ] **Find or create the local user** (`internal/server/me_auth.go`): `store.UpsertSSOUser(sub, username, name, store.RoleFromSSO(role))`, the same call SSO sign-in makes at `internal/server/sso.go`. A returning user with `Active = false` (access removed) is refused 403 `no_access`. The role is refreshed from the token answer on every cache miss, so a role change in User Management applies within 60 s
- [ ] `memberAllowed(u *store.User, f *store.SignupForm, ev *store.Event) bool` in `caps.go`: requires `capsOf(u).MemberAPI` and then the same `Server.inAudience` the web uses (not a `locked` event; audience `anyone` or `signed_in`). **No `Role == member` anywhere in the handlers**; "signed in" is never enough on its own. Add `guest` later by adding the role to the `caps` table and making `memberAllowed` return true only for forms where `OwnHousehold` is enough (see "Later: `guest`")
- [ ] **Idempotency-Key** on `POST`, `PUT` and `DELETE` under `/me`: required (400 `idempotency_key_required` if missing, 1-64 printable characters). Store it in a new table `api_idempotency(user_id, key, method, path, status, body, created_at, PRIMARY KEY (user_id, key))` added to **`010`** (so one migration; name the file `010_member_role.sql` anyway). Same key + same method + same path replays the stored status and body and writes nothing; same key with a different request is 422 `idempotency_key_reused`. The first request runs inside the same write transaction that records the key, so a crash can't make a sign-up without a key. Rows older than 48 h are deleted by the 6-hourly sweep that already runs `SweepSignupClosings`
- [ ] JSON in and out only: `Content-Type: application/json` for bodies (`http.MaxBytesReader` at 64 KB); errors are `{"error": {"code": "...", "message": "..."}}`; no HTML anywhere
- [ ] Tests: see "Tests" below

### 6. `/api/v1/me/…` endpoints (built on the store the web form uses)
Routes (all `Authorization: Bearer umd_…`, JSON):

| Route | What |
|-------|------|
| `GET /api/v1/me/signups/open` | Events with a sign-up form whose **effective state** (`store.SignupState`) is `open` or `full`, that `memberAllowed` for this person, soonest first. Per event: `event_id`, `title`, `starts_on`, `start_time`, `public_description`, rooms, `form`: `{code, label, state, closes_at, spots_left_by_choice (if show_spots)}`, and `signed_up`: the person's own sign-up ID if they have one. Private-event details beyond title, time and rooms are not included |
| `GET /api/v1/me/signups/forms/{code}` | One form's definition: intro, thank-you text, `people` (self/list), `ask_guests`, `ask_allergies`, `contact` (always `none`: a signed-in user's name comes from the account), questions in order with kind, label, help, `required`, `per_person`, "only ask when" (`when` choice IDs), choices with `spots_left` (when `show_spots`), exclusions (`excludes` choice IDs), and **Who's bringing what** lists for the questions marked `public_list`. 404 for a draft, a locked event or a form this person isn't in the audience of |
| `POST /api/v1/me/signups` | Sign up. Body: `{form: code, guests, people: [{name, directory_person_id?, allergies?}], answers: [{question_id, person (index), choice_ids, value}]}` (the store's `SignupInput`, with `UserID`, `Name`, `Phone`, `Email` filled from the local user, never from the body). Returns 201 `{id, ...}` (the same shape as `GET /me/signups/{id}`). **No status code**: the member API uses the user's ID, not the hashed status link |
| `GET /api/v1/me/signups` | My sign-ups: `store.SignupsByUser`, upcoming first, with event title and date, status and `can_change` (form state is open or full and status is `in`) |
| `GET /api/v1/me/signups/{id}` | One of mine, with people and answers (only mine; another user's ID is 404, not 403) |
| `PUT /api/v1/me/signups/{id}` | Change mine while the form isn't closed (`SignupInput.SignupID` set; same body as POST) |
| `DELETE /api/v1/me/signups/{id}` | Cancel (`store.WithdrawSignup`; the row stays as `withdrawn`, the spots are freed) while the form is open or full. Idempotent: cancelling a withdrawn one is 200 |
| `GET /api/v1/me/signups/household` | Only once the directory connection exists (`SIGNUPS_PLAN.md` step 6): the signer's household members to tick (id, name, age band), through the same helper as the web form. Absent (404 `directory_not_connected`) until then |

- [ ] `internal/server/api_me_signups.go`: handlers above. **No new rules.** Each handler builds a `store.SignupInput` and calls `s.store.SaveSignup(in, store.SignupOptions{})` (never `Planner: true`), `WithdrawSignup`, `SignupsByUser`, `SignupFormByCode`, `SignupQuestions`, `SignupState`, `WhosBringingWhat`. `store.SignupError.Messages` become `422 {error: {code: "invalid", messages: [...]}}` with the failing question IDs; "X just filled up" is `409 full`; "already signed up by Jane" is `409 already_signed_up`
- [ ] The conversion between JSON and `SignupInput` goes in one function in `api_me_signups.go` (the JSON twin of `readSignup` in `handlers_signup.go`). Where `readSignup` has logic beyond reading fields (person rows, "just me" uses the signer, dropping follow-up answers) that logic moves into `store` (or a shared `server` helper) used by **both**, rather than being written twice. Done when `readSignup` and the JSON reader share it and the existing `signup_public_test.go` still passes
- [ ] **Directory people**: `directory_person_id` is accepted only when the directory connection is on, and only for the signer's own household (checked against the directory's answer, never trusted from the body). Allergies and shared notes are read live and **never stored**: the API returns a profile's shared allergy note in the form response for display, and `signup_people.allergies` holds only what was typed for this event
- [ ] **Ownership**: every `{id}` is checked with `su.UserID == localUser.ID`; otherwise 404 (same as `ownCtx` for `/my/signups/{id}`). A planner or admin using a member token still only sees their own through `/me`
- [ ] **Answers never public**: the only places other people's content appears are `spots_left` and the **Who's bringing what** lists (signer's first name + last initial and the answers to `public_list` questions, from `in` sign-ups, following the form's audience). Per-person answers and people's names never; withdrawn sign-ups never; the signer's own sign-up includes their full answers
- [ ] A `member` whose local user has no linked directory profile (before the directory exists, or unclaimed) gets `people: self` forms and typed names, as on the web
- [ ] Tests: see below

### 7. Docs, deploy and CORS in practice
- [ ] README section "Member API" (routes, headers, error codes, the 60 s cache); `CLAUDE.md` pointer line and roles bullet (this change)
- [ ] `sw.js`/PWA: nothing new; the `/me` API is cross-origin from the website and the planner's service worker must not cache `/api/` (check `pwa.go` already skips it)
- [ ] `deploy/Caddyfile`: no change (CORS is in the app). Confirm Caddy doesn't strip `Authorization` or add `Access-Control-*`
- [ ] Ansible smoke test in `deploy/ansible/deploy.yml`: `curl -s -o /dev/null -w '%{http_code}' https://events.<zone>/api/v1/me/signups` returns 401 when connected and 404 when not

## Later: the `guest` role (non-member logins, e.g. VBS parents)

Not built now; the checks are shaped so adding it is a small change:

- [ ] `store.RoleGuest = "guest"` below `member` in `Roles`; migration `0NN` extends the CHECK (another `users` rebuild, same pattern as step 1); `RoleFromSSO("guest")`
- [ ] `caps.go`: `guest: {MemberAPI, OwnHousehold}` with **no** `Staff`. `member` keeps the same; the difference is `memberAllowed`: a guest may use a form only if the form's audience allows guests (a new `audience` value `signed_in_guests`, or a `signup_forms.allow_guests` flag, chosen then), and may only sign up **their own household** (`OwnHousehold`: directory people limited to the household the directory returns for their own `sso_subject`, typed names allowed for their own family only if the form allows)
- [ ] `/me/signups/open` and `/me/signups/household` already go through `memberAllowed`/`capsOf`, so a guest gets a shorter list from the same code
- [ ] Tests: a guest can't open the back office (the route walk in step 2 already covers it once `guest` has no `Staff`), can't read another household's directory person, sees only guest-allowed forms

## Tests

All in `internal/server/` with a fake User Management (`httptest`) and the existing test store helpers. Tick when written and passing.

- [ ] **A member can't reach the back office**: the route walk from step 2 over every pattern in `routes()`; member gets 403 on `/events`, `/events/{id}`, tasks, helpers, needs, notes, `/events/{id}/signups`, `/allergies`, `/admin/…`; only the allow-list returns 200
- [ ] **Existing public API unchanged for a member**: a `umd_` token on `/api/v1/events` and `/api/v1/events/{id}` is 401 (it's not an `ep_` token); an `ep_` token on `/api/v1/me/…` is 401; `/api/v1/events` fields are the same as before (no notes, tasks, helpers, needs, answers, member names); `.ics` unchanged
- [ ] **A member's pages show public data only**: `/calendar` as a member lists no private event title
- [ ] **A token for another audience is refused**: the fake User Management answers 401 for a token meant for another app; `/me` returns 401 and creates no local user and no session
- [ ] **Tokens**: expired token 401; token after sign-out (UM answers 401 on the next call, after the 60 s cache) 401; revoked access 401; User Management down 503; `via` not `website` refused
- [ ] **Find or create**: first call creates the local user with `RoleFromSSO(role)`, second call reuses it by `sso_subject`; a role change in the answer updates the row; an inactive row is 403
- [ ] **CORS refuses other origins**: `Origin: https://evil.example` gets no `Access-Control-Allow-Origin` and 403; the website origin gets exactly that origin echoed, never `*`, no `Allow-Credentials`; the preflight for the website origin is 204 without a token and for another origin is 403; with no website origin set, or SSO off, every `/me` route is 404 and no `Access-Control-*` header is sent
- [ ] **No cookies**: a response from `/me` has no `Set-Cookie`; a valid `ep_session` cookie without a Bearer is 401; a valid Bearer with a stale `ep_csrf` still works (no CSRF on `/me`)
- [ ] **An idempotent replay makes one sign-up**: `POST /me/signups` twice with the same `Idempotency-Key` returns the same 201 body and leaves one `signups` row, one set of `signup_people`, and spots taken counted once; missing key is 400; same key with a different body is 422; two concurrent requests with one key make one sign-up; different users with the same key don't collide
- [ ] **Spots and "can't be picked with" are enforced as on the public form**: last spot taken by two members at once (one wins, the other 409 `full`); three people picking a 2-spot per-person choice refused; Make chili + Judge chili refused (per sign-up and per person); a draft, closed or locked form refused; closes at the event start; withdrawing (`DELETE`) frees spots; a `Planner`-only override is not reachable from `/me`. Run the same cases through `POST /signup/{code}` and `/me` from one table-driven test so the two can't drift
- [ ] **Already signed up**: a directory person (once step 6 of `SIGNUPS_PLAN.md` exists) already on another sign-up is refused on `/me` as on the web; until then the case is skipped with a note
- [ ] **Answers never leak to other members**: member B's `GET /me/signups`, `/me/signups/{A's id}` (404), `/me/signups/open` and `/me/signups/forms/{code}` never contain A's phone, email, people, allergies, `planner_note`, per-person answers or a withdrawn sign-up; the "Who's bringing what" list shows only A's first name + last initial and `public_list` answers; the whole form response is checked with a string search for A's last name, phone and allergy text
- [ ] **Allergies and directory notes**: with a fake directory, the profile's shared allergy appears in the form response and is **absent** from the database, the `signups` CSV and the log output
- [ ] **Ownership**: member B gets 404 on `PUT` and `DELETE` of member A's sign-up
- [ ] **Migration**: 009 -> 010 test from step 1 passes, and the `api_idempotency` table exists

## Things not to do (yet)

- No cookies, sessions, CSRF or HTML on `/me`. No `*` in CORS, no credentials.
- No second implementation of spots, exclusions, "already signed up" or the effective state: the `/me` API calls the store the web form calls.
- No back-office data on `/me` (notes, tasks, helpers, needs, the full sign-up list, the allergy list, CSV). Members only ever see their own sign-ups.
- No staff features for members in the website: planners and the event team use the planner directly.
- The `guest` role (see above). Nothing here assumes "signed in = member": every route goes through `capsOf` and `memberAllowed`.
- Waiting lists, email or text confirmations, payments (as in `SIGNUPS_PLAN.md`).
