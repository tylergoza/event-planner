package server

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/tylergoza/event-planner/internal/store"
	"github.com/tylergoza/event-planner/internal/tracker"
)

// Needs: items and supplies, mostly from the maintenance tracker ---------------

// needRow is a need with what the maintenance tracker says about it.
type needRow struct {
	store.Need
	Found     bool   // the tracker still has it
	Have      int    // how many there are in all (products) or on hand (supplies)
	Unit      string // a supply's unit, e.g. "rolls"
	Stock     string // supplies: "ok", "low" or "out"
	Where     string
	Short     bool // fewer than needed
	Shortfall int
	Link      string
}

func (s *Server) needRows(ctx context.Context, needs []store.Need) ([]needRow, string) {
	t := s.Tracker()
	rows := make([]needRow, len(needs))
	var products map[int64]tracker.Product
	var supplies map[int64]tracker.Supply
	var problem string
	for i, n := range needs {
		row := needRow{Need: n}
		switch {
		case !n.FromTracker():
		case !t.Configured():
			problem = trackerErr(tracker.ErrNotConfigured)
		case n.TrackerKind == "product":
			if products == nil {
				list, err := t.Products(ctx, "")
				if err != nil {
					problem = trackerErr(err)
					break
				}
				products = map[int64]tracker.Product{}
				for _, p := range list {
					products[p.ID] = p
				}
			}
			if p, ok := products[n.TrackerID]; ok {
				row.Found, row.Have = true, p.Total
				row.Where = pluralize(p.PlaceCount, "place")
				row.Link = t.PageURL("products", p.ID)
			}
		case n.TrackerKind == "supply":
			if supplies == nil {
				list, err := t.Supplies(ctx, "")
				if err != nil {
					problem = trackerErr(err)
					break
				}
				supplies = map[int64]tracker.Supply{}
				for _, sp := range list {
					supplies[sp.ID] = sp
				}
			}
			if sp, ok := supplies[n.TrackerID]; ok {
				row.Found, row.Have, row.Unit, row.Stock = true, sp.OnHand, sp.Unit, sp.Stock
				row.Where = sp.Place.Path
				row.Link = t.PageURL("supplies", sp.ID)
			}
		}
		row.Short = row.Found && n.Status == "needed" && row.Have < n.Quantity
		if row.Short {
			row.Shortfall = n.Quantity - row.Have
		}
		rows[i] = row
	}
	return rows, problem
}

func (s *Server) needsData(r *http.Request, e *store.Event, data map[string]any) (map[string]any, error) {
	needs, err := s.store.ListNeeds(e.ID)
	if err != nil {
		return nil, err
	}
	rooms, err := s.store.ListRooms(e.ID)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	rows, problem := s.needRows(ctx, needs)
	tally := store.Tally{Total: len(needs)}
	for _, n := range needs {
		if n.Status != "needed" {
			tally.Done++
		}
	}
	t := s.Tracker()
	data["Title"] = "Needs · " + e.Title
	data["Needs"] = rows
	data["Tally"] = tally
	data["TrackerProblem"] = problem
	data["TrackerOn"] = t.Configured()
	data["Rooms"] = rooms
	data["Statuses"] = store.NeedStatuses
	if _, ok := data["Form"]; !ok {
		data["Form"] = store.Need{Quantity: 1}
	}
	if q := r.URL.Query().Get("q"); q != "" && t.Configured() {
		data["Q"] = q
		products, err := t.Products(ctx, q)
		if err != nil {
			data["SearchError"] = trackerErr(err)
		}
		supplies, err := t.Supplies(ctx, q)
		if err != nil {
			data["SearchError"] = trackerErr(err)
		}
		data["FoundProducts"] = products
		data["FoundSupplies"] = supplies
	}
	return data, nil
}

func (s *Server) renderNeeds(w http.ResponseWriter, r *http.Request, status int, e *store.Event, data map[string]any) {
	data, err := s.needsData(r, e, data)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.eventPage(w, r, status, "needs/index", e, "needs", data)
}

func (s *Server) handleNeeds(w http.ResponseWriter, r *http.Request) {
	e, ok := s.event(w, r)
	if !ok {
		return
	}
	s.renderNeeds(w, r, http.StatusOK, e, map[string]any{"Live": true})
}

func (s *Server) readNeed(r *http.Request, n *store.Need) []string {
	n.Name, n.Notes, n.RoomID = formStr(r, "name"), formStr(r, "notes"), formInt(r, "room_id")
	if v := formStr(r, "status"); v != "" {
		n.Status = v
	}
	q, err := strconv.Atoi(formStr(r, "quantity"))
	n.Quantity = q
	n.UpdatedBy = currentUser(r).Name()
	var errs []string
	if n.Name == "" {
		errs = append(errs, "Say what's needed.")
	}
	if err != nil || q < 1 || q > 100000 {
		errs = append(errs, "How many are needed? At least 1.")
	}
	if n.RoomID != 0 {
		if ok, err := s.store.RoomInEvent(n.RoomID, n.EventID); err != nil || !ok {
			errs = append(errs, "That room isn't part of this event.")
		}
	}
	if !store.ValidNeedStatus(n.Status) {
		errs = append(errs, "Pick a status.")
	}
	return errs
}

func (s *Server) handleNeedCreate(w http.ResponseWriter, r *http.Request) {
	e, ok := s.event(w, r)
	if !ok {
		return
	}
	n := store.Need{EventID: e.ID, Status: "needed"}
	errs := s.readNeed(r, &n)
	switch kind := formStr(r, "tracker_kind"); kind {
	case "":
	case "product", "supply":
		n.TrackerKind, n.TrackerID = kind, formInt(r, "tracker_id")
		if n.TrackerID < 1 {
			errs = append(errs, "That isn't something in the maintenance tracker.")
		}
	default:
		errs = append(errs, "That isn't something in the maintenance tracker.")
	}
	if errs != nil {
		s.renderNeeds(w, r, http.StatusUnprocessableEntity, e, map[string]any{"Form": n, "Errors": errs})
		return
	}
	if _, err := s.store.CreateNeed(&n); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.redirect(w, r, eventURL(e.ID, "/needs"), strconv.Itoa(n.Quantity)+" × "+n.Name+" added.")
}

func (s *Server) needAndEvent(w http.ResponseWriter, r *http.Request) (*store.Need, *store.Event, bool) {
	n, err := s.store.GetNeed(pathID(r))
	if err != nil {
		s.serverError(w, r, err)
		return nil, nil, false
	}
	e, ok := s.eventByID(w, r, n.EventID)
	return n, e, ok
}

func (s *Server) needForm(w http.ResponseWriter, r *http.Request, status int, e *store.Event, n store.Need, errs []string) {
	link := ""
	if n.FromTracker() {
		kind := "products"
		if n.TrackerKind == "supply" {
			kind = "supplies"
		}
		link = s.Tracker().PageURL(kind, n.TrackerID)
	}
	rooms, err := s.store.ListRooms(e.ID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.eventPage(w, r, status, "needs/form", e, "needs", map[string]any{
		"Form": n, "Errors": errs, "Rooms": rooms, "Statuses": store.NeedStatuses, "TrackerLink": link,
	})
}

func (s *Server) handleNeedEdit(w http.ResponseWriter, r *http.Request) {
	n, e, ok := s.needAndEvent(w, r)
	if !ok {
		return
	}
	s.needForm(w, r, http.StatusOK, e, *n, nil)
}

func (s *Server) handleNeedUpdate(w http.ResponseWriter, r *http.Request) {
	n, e, ok := s.needAndEvent(w, r)
	if !ok {
		return
	}
	if errs := s.readNeed(r, n); errs != nil {
		s.needForm(w, r, http.StatusUnprocessableEntity, e, *n, errs)
		return
	}
	if err := s.store.UpdateNeed(n); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.redirect(w, r, eventURL(e.ID, "/needs"), "Saved.")
}

func (s *Server) handleNeedStatus(w http.ResponseWriter, r *http.Request) {
	n, e, ok := s.needAndEvent(w, r)
	if !ok {
		return
	}
	status := formStr(r, "status")
	if !store.ValidNeedStatus(status) {
		s.setFlash(w, r, "error", "Pick a status.")
		http.Redirect(w, r, eventURL(e.ID, "/needs"), http.StatusSeeOther)
		return
	}
	if err := s.store.SetNeedStatus(n.ID, status, currentUser(r).Name()); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.redirect(w, r, eventURL(e.ID, "/needs"), n.Name+": "+store.NeedStatusLabel(status)+".")
}

func (s *Server) handleNeedDelete(w http.ResponseWriter, r *http.Request) {
	n, e, ok := s.needAndEvent(w, r)
	if !ok {
		return
	}
	if err := s.store.DeleteNeed(n.ID); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.redirect(w, r, eventURL(e.ID, "/needs"), n.Name+" removed.")
}
