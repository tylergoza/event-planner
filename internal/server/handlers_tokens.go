package server

import "net/http"

// API tokens (admin) ---------------------------------------------------------

func (s *Server) renderTokens(w http.ResponseWriter, r *http.Request, status int, name string, private bool, newToken string, errs []string) {
	tokens, err := s.store.ListTokens()
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.render(w, r, status, "tokens/index", map[string]any{
		"Title": "API tokens", "Tokens": tokens, "Name": name, "Private": private, "NewToken": newToken,
		"APIBase": s.baseURL(r) + "/api/v1", "CalendarURL": s.baseURL(r) + "/calendar.ics", "Errors": errs,
	})
}

func (s *Server) handleTokens(w http.ResponseWriter, r *http.Request) {
	s.renderTokens(w, r, http.StatusOK, "", false, "", nil)
}

// handleTokenCreate makes a token and shows it on the page, the one time it
// can be seen; it isn't put in a flash message or redirect.
func (s *Server) handleTokenCreate(w http.ResponseWriter, r *http.Request) {
	name := formStr(r, "name")
	private := r.PostFormValue("include_private") != ""
	var errs []string
	if name == "" {
		errs = append(errs, "Name what will use it, like Church website.")
	}
	if len(name) > 100 {
		errs = append(errs, "Keep the name under 100 characters.")
	}
	if errs != nil {
		s.renderTokens(w, r, http.StatusUnprocessableEntity, name, private, "", errs)
		return
	}
	token, err := s.store.CreateToken(name, private, currentUser(r).ID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	s.renderTokens(w, r, http.StatusOK, "", false, token, nil)
}

func (s *Server) handleTokenPrivate(w http.ResponseWriter, r *http.Request) {
	if err := s.store.SetTokenPrivate(pathID(r), r.PostFormValue("private") == "1"); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.redirect(w, r, "/admin/tokens", "Token updated.")
}

func (s *Server) handleTokenDelete(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeleteToken(pathID(r)); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.redirect(w, r, "/admin/tokens", "Token revoked. Anything using it can no longer read events.")
}
