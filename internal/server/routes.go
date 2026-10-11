package server

import (
	"crypto/subtle"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/tylergoza/event-planner/internal/store"
)

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()

	// Static assets & PWA plumbing
	mux.Handle("GET /static/", s.staticHandler())
	mux.HandleFunc("GET /sw.js", s.handleServiceWorker)
	mux.HandleFunc("GET /manifest.webmanifest", s.handleManifest)
	mux.HandleFunc("GET /favicon.ico", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, s.asset("icons/icon.svg"), http.StatusMovedPermanently)
	})
	mux.HandleFunc("GET /offline", s.handleOffline)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		if err := s.store.DB.PingContext(r.Context()); err != nil {
			http.Error(w, "db unavailable", http.StatusServiceUnavailable)
			return
		}
		w.Write([]byte("ok"))
	})

	// Public
	mux.HandleFunc("GET /login", s.handleLoginForm)
	mux.HandleFunc("POST /login", s.handleLogin)
	mux.HandleFunc("POST /logout", s.handleLogout)
	mux.HandleFunc("GET /setup", s.handleSetupForm)
	mux.HandleFunc("POST /setup", s.handleSetup)
	mux.HandleFunc("GET /signed-out", s.handleSignedOut)
	// Single sign-on (404 unless SSO_URL is set). /auth/start goes to User
	// Management even while the break-glass local login is on.
	mux.HandleFunc("GET /auth/start", s.handleSSOStart)
	mux.HandleFunc("GET /auth/callback", s.handleSSOCallback)

	// Signed-in editors.
	auth := func(pattern string, h http.HandlerFunc) { mux.Handle(pattern, s.requireUser(h)) }

	// Planners (and admins) create and run events; helpers work inside them.
	plan := func(pattern string, h http.HandlerFunc) { mux.Handle(pattern, s.requirePlanner(h)) }

	// The calendar is public: no requireUser. Signed-in people get the same
	// pages with more detail (the session is loaded for these paths).
	mux.HandleFunc("GET /{$}", s.handleHome) // dashboard, or the calendar when signed out
	mux.HandleFunc("GET /calendar", s.handleCalendar)
	mux.HandleFunc("GET /calendar/{id}", s.handleCalendarEvent)
	auth("GET /live", s.handleLive)

	auth("GET /events", s.handleEvents)
	plan("GET /events/new", s.handleEventNew)
	plan("POST /events", s.handleEventCreate)
	auth("GET /events/{id}", s.handleEventShow)
	plan("GET /events/{id}/edit", s.handleEventEdit)
	plan("POST /events/{id}", s.handleEventUpdate)
	plan("POST /events/{id}/delete", s.handleEventDelete)
	plan("GET /events/{id}/copy", s.handleEventCopyForm)
	plan("POST /events/{id}/copy", s.handleEventCopy)
	auth("GET /events/{id}/people", s.handlePeople)

	// Repeating events: series actions are planner actions.
	auth("GET /series/{id}", s.handleSeriesShow)
	plan("POST /series/{id}/restore", s.handleSeriesRestore)
	plan("GET /series/{id}/end", s.handleSeriesEnd)
	plan("GET /events/{id}/match-later", s.handleMatchLaterForm)
	plan("POST /events/{id}/match-later", s.handleMatchLater)
	plan("GET /events/{id}/end-series", s.handleEndSeriesForm)
	plan("POST /events/{id}/end-series", s.handleEndSeries)
	plan("GET /events/{id}/repeat", s.handleRepeatForm)
	plan("POST /events/{id}/repeat", s.handleRepeatUpdate)

	auth("GET /events/{id}/schedule", s.handleSchedule)
	plan("POST /events/{id}/schedule", s.handleScheduleCreate)
	plan("GET /schedule/{id}/edit", s.handleScheduleEdit)
	plan("POST /schedule/{id}", s.handleScheduleUpdate)
	plan("POST /schedule/{id}/delete", s.handleScheduleDelete)

	auth("GET /events/{id}/tasks", s.handleTasks)
	auth("POST /events/{id}/tasks", s.handleTaskCreate)
	auth("POST /events/{id}/tasks/paste", s.handleTaskPaste)
	auth("GET /tasks/{id}/edit", s.handleTaskEdit)
	auth("POST /tasks/{id}", s.handleTaskUpdate)
	auth("POST /tasks/{id}/status", s.handleTaskStatus)
	auth("POST /tasks/{id}/delete", s.handleTaskDelete)
	auth("POST /events/{id}/task-lists", s.handleTaskListCreate)
	auth("POST /task-lists/{id}", s.handleTaskListRename)
	auth("POST /task-lists/{id}/delete", s.handleTaskListDelete)
	auth("POST /task-lists/{id}/move", s.handleMove("task_lists"))

	auth("GET /events/{id}/needs", s.handleNeeds)
	auth("POST /events/{id}/needs", s.handleNeedCreate)
	auth("GET /needs/{id}/edit", s.handleNeedEdit)
	auth("POST /needs/{id}", s.handleNeedUpdate)
	auth("POST /needs/{id}/status", s.handleNeedStatus)
	auth("POST /needs/{id}/delete", s.handleNeedDelete)

	// rooms routes
	auth("GET /events/{id}/rooms", s.handleRooms)
	plan("POST /events/{id}/rooms", s.handleRoomCreate)
	plan("GET /rooms/{id}/edit", s.handleRoomEdit)
	plan("POST /rooms/{id}", s.handleRoomUpdate)
	plan("POST /rooms/{id}/delete", s.handleRoomDelete)
	plan("POST /rooms/{id}/move", s.handleMove("event_rooms"))

	// helpers routes
	auth("GET /events/{id}/helpers", s.handleHelpers)
	plan("POST /events/{id}/helper-roles", s.handleHelperRoleCreate)
	plan("POST /events/{id}/helper-roles/paste", s.handleHelperRolePaste)
	plan("GET /helper-roles/{id}/edit", s.handleHelperRoleEdit)
	plan("POST /helper-roles/{id}", s.handleHelperRoleUpdate)
	plan("POST /helper-roles/{id}/delete", s.handleHelperRoleDelete)
	plan("POST /helper-roles/{id}/move", s.handleMove("helper_roles"))
	auth("POST /helper-roles/{id}/signup", s.handleHelperSignUp)
	auth("POST /helper-roles/{id}/me", s.handleHelperMe)
	auth("POST /helper-roles/{id}/me/delete", s.handleHelperMeDelete)
	auth("GET /helpers/{id}/edit", s.handleHelperEdit)
	auth("POST /helpers/{id}/edit", s.handleHelperUpdate)
	auth("POST /helpers/{id}/delete", s.handleHelperDelete)

	auth("GET /account", s.handleAccount)
	auth("POST /account/password", s.handleAccountPassword)

	// Admins
	admin := func(pattern string, h http.HandlerFunc) { mux.Handle(pattern, s.requireAdmin(h)) }
	admin("GET /admin/users", s.handleUsers)
	admin("GET /admin/users/new", s.handleUserNew)
	admin("POST /admin/users", s.handleUserCreate)
	admin("GET /admin/users/{id}/edit", s.handleUserEdit)
	admin("POST /admin/users/{id}", s.handleUserUpdate)
	admin("POST /admin/users/{id}/delete", s.handleUserDelete)
	admin("POST /admin/access/{sub}", s.handleSSOAccessUpdate) // SSO only
	admin("GET /admin/settings", s.handleSettings)
	admin("POST /admin/settings", s.handleSettingsUpdate)
	admin("GET /admin/backup", s.handleBackup)
	s.apiRoutes(mux, admin) // API tokens, /api/v1/ and /calendar.ics

	mux.HandleFunc("/", s.notFound)

	pages := s.loadSession(s.csrf(s.announceChanges(mux)))
	return s.logRequests(s.securityHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case isCookieFree(r.URL.Path):
			// The API and the feed ignore sessions and CSRF: no session is
			// looked up and no cookie is set.
			mux.ServeHTTP(w, r)
		default:
			if isPublicPath(r.URL.Path) {
				w.Header().Add("Vary", "Cookie") // signed-in people see more
			}
			pages.ServeHTTP(w, r)
		}
	})))
}

// Middleware -------------------------------------------------------------

func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: 200}
		defer func() {
			if err := recover(); err != nil {
				s.log.Error("panic", "path", r.URL.Path, "err", err)
				http.Error(rec, "internal error", http.StatusInternalServerError)
			}
			if !strings.HasPrefix(r.URL.Path, "/static/") {
				s.log.Info("request", "method", r.Method, "path", r.URL.Path, "status", rec.status,
					"dur", time.Since(start).Round(time.Millisecond), "ip", s.clientIP(r))
			}
		}()
		next.ServeHTTP(rec, r)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// Unwrap lets http.ResponseController reach Flush and write deadlines.
func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", s.csp)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "same-origin")
		if s.isHTTPS(r) {
			h.Set("Strict-Transport-Security", "max-age=31536000")
		}
		next.ServeHTTP(w, r)
	})
}

const sessionCookie = "ep_session"

func (s *Server) loadSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/static/") {
			next.ServeHTTP(w, r)
			return
		}
		if c, err := r.Cookie(sessionCookie); err == nil && c.Value != "" {
			sess, err := s.store.GetSession(c.Value)
			var user *store.User
			switch {
			case err != nil:
			case r.URL.Path == "/live":
				// The live update stream carries no data and can stay open
				// for hours, so it goes on the stored session alone: opening
				// it never waits on User Management, and the page refreshes
				// it sets off check the grant instead.
				user = &sess.User
			default:
				// SSO sessions are re-checked with User Management every
				// few minutes; nil means that sign-in has ended.
				user = s.checkGrant(sess)
			}
			if user != nil {
				r = withValue(r, ctxSession, sess)
				r = withValue(r, ctxUser, user)
			} else {
				http.SetCookie(w, &http.Cookie{Name: sessionCookie, Path: "/", MaxAge: -1})
			}
		}
		next.ServeHTTP(w, r)
	})
}

// csrf implements the double-submit cookie pattern: every visitor gets a
// random token cookie and every POST must echo it in the _csrf field (or
// X-CSRF-Token header). Combined with SameSite=Lax cookies this blocks
// cross-site form posts.
const csrfCookie = "ep_csrf"

func (s *Server) csrf(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/static/") {
			next.ServeHTTP(w, r)
			return
		}
		token := ""
		if c, err := r.Cookie(csrfCookie); err == nil && len(c.Value) >= 32 {
			token = c.Value
		}
		if r.Method == http.MethodPost || r.Method == http.MethodPut || r.Method == http.MethodDelete || r.Method == http.MethodPatch {
			sent := r.Header.Get("X-CSRF-Token")
			if sent == "" {
				sent = r.PostFormValue("_csrf")
			}
			if token == "" || subtle.ConstantTimeCompare([]byte(sent), []byte(token)) != 1 {
				s.render(w, r, http.StatusForbidden, "error", map[string]any{
					"Title":   "Form expired",
					"Message": "Your form session expired. Please go back, refresh the page, and try again.",
				})
				return
			}
		}
		if token == "" {
			token = randomToken()
			http.SetCookie(w, &http.Cookie{
				Name: csrfCookie, Value: token, Path: "/", HttpOnly: true,
				SameSite: http.SameSiteLaxMode, Secure: s.isHTTPS(r), MaxAge: 365 * 24 * 3600,
			})
		}
		next.ServeHTTP(w, withValue(r, ctxCSRF, token))
	})
}

func (s *Server) requireUser(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if currentUser(r) == nil {
			// The live stream and a live page refreshing itself in the
			// background get a plain 401: following a redirect to /login
			// would start a sign-in and replace the ep_sso cookie of one
			// the person may have under way in another tab.
			if r.URL.Path == "/live" || r.Header.Get("X-Live-Refresh") != "" {
				w.Header().Set("Cache-Control", "no-store")
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			dest := r.URL.RequestURI()
			if r.Method != http.MethodGet {
				dest = "/"
			}
			http.Redirect(w, r, "/login?next="+urlEscape(dest), http.StatusSeeOther)
			return
		}
		w.Header().Set("Cache-Control", "private, no-store")
		next(w, r)
	})
}

func (s *Server) requirePlanner(next http.HandlerFunc) http.Handler {
	return s.requireUser(func(w http.ResponseWriter, r *http.Request) {
		if !currentUser(r).CanPlan() {
			s.render(w, r, http.StatusForbidden, "error", map[string]any{
				"Title": "Planners only", "Message": "You need to be a planner or administrator to do that.",
			})
			return
		}
		next(w, r)
	})
}

func (s *Server) requireAdmin(next http.HandlerFunc) http.Handler {
	return s.requireUser(func(w http.ResponseWriter, r *http.Request) {
		if !currentUser(r).IsAdmin() {
			s.render(w, r, http.StatusForbidden, "error", map[string]any{
				"Title": "Admins only", "Message": "You need administrator access for that page.",
			})
			return
		}
		next(w, r)
	})
}

func (s *Server) isHTTPS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	return s.cfg.TrustProxy && strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

func (s *Server) clientIP(r *http.Request) string {
	if s.cfg.TrustProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			first, _, _ := strings.Cut(xff, ",")
			return strings.TrimSpace(first)
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
