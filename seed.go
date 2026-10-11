package main

import (
	"time"

	"github.com/tylergoza/event-planner/internal/recur"
	"github.com/tylergoza/event-planner/internal/store"
)

// seedDemo adds a sample fellowship dinner: task lists and tasks, a
// schedule, rooms, a couple of things needed and helper jobs.
func seedDemo(st *store.Store) error {
	today := time.Now()
	day := func(days int) string { return today.AddDate(0, 0, days).Format(store.DateLayout) }

	e := store.Event{
		Title: "Fall Fellowship Dinner", Kind: "Fellowship dinner", Status: "planning",
		StartsOn: day(21), StartTime: "17:30", EndTime: "20:00",
		Coordinator: "Pat Morgan", ExpectedCount: 80,
		Description:       "Potluck-style dinner for the whole church, with games for the kids afterwards.",
		Visibility:        "public",
		PublicDescription: "Everyone is welcome!\nBring a side dish to share; we provide the main course and drinks.\nGames for the kids afterwards.",
	}
	id, err := st.CreateEvent(&e)
	if err != nil {
		return err
	}

	// A private meeting, so the calendar shows both kinds of entry.
	deacons := store.Event{
		Title: "Deacons' meeting", Kind: "Meeting", Status: "planning", Visibility: "private",
		StartsOn: day(24), StartTime: "14:00", EndTime: "16:00", Coordinator: "Chris",
	}
	did, err := st.CreateEvent(&deacons)
	if err != nil {
		return err
	}
	if _, err := st.AddRoom(did, 0, "Lifecenter"); err != nil {
		return err
	}

	var hall, kitchen int64
	for _, rm := range []struct {
		id    *int64
		name  string
		setup string
	}{
		{&hall, "Fellowship Hall", "10 round tables of 8, chairs, stage left clear for the games, serving line along the east wall."},
		{&kitchen, "Kitchen", "Both ovens on by 4:00, coffee urns on the counter by the window, dish station by the back door."},
	} {
		rid, err := st.AddRoom(id, 0, rm.name)
		if err != nil {
			return err
		}
		if err := st.UpdateRoom(&store.Room{ID: rid, Setup: rm.setup}); err != nil {
			return err
		}
		*rm.id = rid
	}

	for _, it := range []store.ScheduleItem{
		{Date: day(21), Time: "15:00", EndTime: "17:00", Label: "Set up tables and chairs", RoomID: hall},
		{Date: day(21), Time: "17:30", Label: "Doors open"},
		{Date: day(21), Time: "18:00", EndTime: "19:00", Label: "Dinner", RoomID: hall, Notes: "Blessing first, then the line opens by table."},
		{Date: day(21), Time: "19:00", EndTime: "20:00", Label: "Kids' games and clean-up", RoomID: hall},
	} {
		it.EventID = id
		if _, err := st.CreateScheduleItem(&it); err != nil {
			return err
		}
	}

	lists := map[string]int64{}
	for _, name := range []string{"Before", "Kitchen", "Clean-up"} {
		lid, err := st.CreateTaskList(id, name)
		if err != nil {
			return err
		}
		lists[name] = lid
	}
	for _, t := range []store.Task{
		{ListID: lists["Before"], Title: "Send the invitation in the bulletin", Status: "done", AssigneeName: "Office"},
		{ListID: lists["Before"], Title: "Sign-up sheet for side dishes", Status: "in_progress", AssigneeName: "Dana", DueOn: day(10)},
		{ListID: lists["Before"], Title: "Book the fellowship hall", Status: "blocked", AssigneeName: "Pat", DueOn: day(-2), Notes: "Waiting to hear about the youth room."},
		{ListID: lists["Kitchen"], Title: "Order the main dishes", Status: "todo", RoomID: kitchen, AssigneeName: "Chris", DueOn: day(14)},
		{ListID: lists["Kitchen"], Title: "Borrow the large coffee urns", Status: "todo", RoomID: kitchen, AssigneeName: "Riley", DueOn: day(18)},
		{ListID: lists["Kitchen"], Title: "Label the serving dishes", Status: "todo", RoomID: kitchen},
		{ListID: lists["Clean-up"], Title: "Wash and return borrowed dishes", Status: "todo", AssigneeName: "Youth group"},
		{ListID: lists["Clean-up"], Title: "Take out the trash and recycling", Status: "todo"},
		{Title: "Thank-you notes to the volunteers", Status: "todo", DueOn: day(28)},
		{Title: "Count the leftover chairs", Status: "todo", RoomID: hall},
	} {
		t.EventID, t.UpdatedBy = id, "Demo"
		if _, err := st.CreateTask(&t); err != nil {
			return err
		}
	}

	for _, n := range []store.Need{
		{Name: "Folding tables", Quantity: 10, RoomID: hall},
		{Name: "Paper plates and napkins", Quantity: 100, RoomID: kitchen, Notes: "Ask the Millers; they lent some last year."},
		{Name: "Gaffer tape", Quantity: 2, Status: "gathered"},
	} {
		n.EventID, n.UpdatedBy = id, "Demo"
		if _, err := st.CreateNeed(&n); err != nil {
			return err
		}
	}

	if _, err := st.AddHelperRoles(id, "Greeters x2\nServers x4\nDishwashing x3"); err != nil {
		return err
	}
	roles, err := st.ListHelperRoles(id)
	if err != nil {
		return err
	}
	for _, r := range roles {
		r.RoomID = hall
		if r.Name == "Dishwashing" {
			r.RoomID = kitchen
		}
		if err := st.UpdateHelperRole(&r); err != nil {
			return err
		}
		var who []string
		switch r.Name {
		case "Greeters":
			who = []string{"Dana", "Sam Ortiz"}
		case "Servers":
			who = []string{"Riley"}
		case "Dishwashing":
			who = []string{"Youth group"}
		}
		for _, n := range who {
			if _, err := st.SignUpName(r.ID, n, ""); err != nil {
				return err
			}
		}
	}
	return seedSupper(st, today)
}

// seedSupper adds a weekly Wednesday supper, so the demo has a series.
func seedSupper(st *store.Store, now time.Time) error {
	wed := now
	for wed.Weekday() != time.Wednesday {
		wed = wed.AddDate(0, 0, 1)
	}
	e := store.Event{
		Title: "Wednesday Family Supper", Kind: "Fellowship dinner", Status: "planning", Visibility: "public",
		StartsOn: wed.Format(store.DateLayout), StartTime: "18:00", EndTime: "19:30",
		Coordinator: "Dana Reyes", ExpectedCount: 40,
		Description:       "A simple weekly meal before midweek activities.",
		PublicDescription: "Join us every Wednesday for a family-style supper. All are welcome.",
	}
	id, err := st.CreateEvent(&e)
	if err != nil {
		return err
	}
	if _, err := st.AddRoom(id, 0, "Fellowship Hall"); err != nil {
		return err
	}
	list, err := st.CreateTaskList(id, "Supper")
	if err != nil {
		return err
	}
	for _, title := range []string{"Set up tables", "Prepare the main dish", "Serve", "Clean up and wash dishes"} {
		if _, err := st.CreateTask(&store.Task{EventID: id, ListID: list, Title: title, Status: "todo", UpdatedBy: "Demo"}); err != nil {
			return err
		}
	}
	rule := recur.Rule{Freq: recur.Weekly, Every: 1, Weekdays: []time.Weekday{time.Wednesday}}
	_, err = st.MakeSeries(id, rule, now.Format(store.DateLayout))
	return err
}
