package ops

import (
	"context"
	"database/sql"
	"time"

	"github.com/scottzx/mycontext/internal/adapters/sqlite"
	"github.com/scottzx/mycontext/internal/system"
)

// AgendaEntry is one line on a day board. The same task can appear twice with
// different reasons ("scheduled" and "hard_due") - that overlap is the point.
type AgendaEntry struct {
	Date             string  `json:"date"`
	Reason           string  `json:"reason"` // scheduled | hard_due | review
	TaskID           string  `json:"task_id"`
	Title            string  `json:"title"`
	Status           string  `json:"status"`
	Importance       string  `json:"importance"`
	ProjectID        *string `json:"project_id"`
	ProjectName      *string `json:"project_name"`
	AreaName         *string `json:"area_name"`
	HardDueAt        *string `json:"hard_due_at"`
	NextReviewAt     *string `json:"next_review_at"`
	EffectiveMinutes *int    `json:"effective_minutes"`
	ScheduleID       *string `json:"schedule_id"`
	TimeSlot         *string `json:"time_slot"`
}

// DayLoad states the arithmetic of a day. It never suggests what to drop.
type DayLoad struct {
	Date                 string `json:"date"`
	AvailableMinutes     int    `json:"available_minutes"`
	IsDefaultCapacity    bool   `json:"is_default_capacity"`
	PlannedMinutes       int    `json:"planned_minutes"`
	TaskCount            int    `json:"task_count"`
	TasksWithoutEstimate int    `json:"tasks_without_estimate"`
	OverloadMinutes      int    `json:"overload_minutes"`
}

// OverdueEntry is a missed hard deadline.
type OverdueEntry struct {
	TaskID      string  `json:"task_id"`
	Title       string  `json:"title"`
	Importance  string  `json:"importance"`
	ProjectName *string `json:"project_name"`
	HardDueAt   string  `json:"hard_due_at"`
	DaysOverdue int     `json:"days_overdue"`
}

// QualityIssue is one violated data-quality rule (§15).
type QualityIssue struct {
	EntityType string `json:"entity_type"`
	EntityID   string `json:"entity_id"`
	Title      string `json:"title"`
	Issue      string `json:"issue"`
	Detail     string `json:"detail"`
}

// ListCap bounds the long tail lists in the cockpit. A dashboard that prints
// thousands of rows is a dump, not a status; the totals still report reality.
const ListCap = 50

// Status is the answer to `mycontext ops status`: the whole cockpit in one read.
type Status struct {
	GeneratedAt          string         `json:"generated_at"`
	Today                string         `json:"today"`
	TodayLoad            DayLoad        `json:"today_load"`
	TodayAgenda          []AgendaEntry  `json:"today_agenda"`
	TomorrowAgenda       []AgendaEntry  `json:"tomorrow_agenda"`
	Week                 []DayLoad      `json:"week"`
	Overdue              []OverdueEntry `json:"overdue"`
	ReviewDue            []AgendaEntry  `json:"review_due"`
	UnscheduledImportant []AgendaEntry  `json:"unscheduled_important"`
	OverloadedDays       []DayLoad      `json:"overloaded_days"`
	QualityIssues        []QualityIssue `json:"quality_issues"`

	// Totals report the full counts even when a list above was capped, so a
	// truncated view can never understate the real situation.
	Totals            Totals `json:"totals"`
	ProjectionVersion int    `json:"projection_version"`
}

// Totals are the full counts behind the capped lists.
type Totals struct {
	TodayEntries         int  `json:"today_entries"`
	Overdue              int  `json:"overdue"`
	ReviewDue            int  `json:"review_due"`
	UnscheduledImportant int  `json:"unscheduled_important"`
	OverloadedDays       int  `json:"overloaded_days"`
	QualityIssues        int  `json:"quality_issues"`
	Truncated            bool `json:"truncated"`
}

// agendaSelect fixes the column order every agenda query must produce, so a
// later change to a view cannot silently shift the scan.
const agendaSelect = `
    SELECT date, reason, task_id, title, status, importance, project_id, project_name,
           area_name, hard_due_at, next_review_at, effective_minutes, schedule_id, time_slot
      FROM `

// ProjectionVersion lets the UI detect a read-model change without tracking
// every table (§17).
const ProjectionVersion = 1

// Status assembles the cockpit from the deterministic views only. No ranking,
// no model judgement, no hidden filtering.
func (s *Store) Status(ctx context.Context) (*Status, error) {
	today := s.clock.Today()
	out := &Status{
		GeneratedAt:       system.FormatTimestamp(s.clock.Now()),
		Today:             today,
		ProjectionVersion: ProjectionVersion,
	}

	var err error
	if out.TodayAgenda, err = s.agenda(ctx, agendaSelect+"v_today ORDER BY reason, importance, title"); err != nil {
		return nil, err
	}
	if out.TomorrowAgenda, err = s.agenda(ctx, agendaSelect+"v_tomorrow ORDER BY reason, importance, title"); err != nil {
		return nil, err
	}
	if out.ReviewDue, err = s.agenda(ctx, `
        SELECT next_review_at AS date, 'review' AS reason, task_id, title, status, importance,
               project_id, project_name, area_name, NULL, next_review_at, NULL, NULL, NULL
          FROM v_review_due ORDER BY next_review_at, importance LIMIT ?`, ListCap+1); err != nil {
		return nil, err
	}
	if out.UnscheduledImportant, err = s.agenda(ctx, `
        SELECT NULL AS date, 'unscheduled' AS reason, task_id, title, status, importance,
               project_id, project_name, area_name, NULL, next_review_at, estimate_minutes, NULL, NULL
          FROM v_unscheduled_important ORDER BY importance, title LIMIT ?`, ListCap+1); err != nil {
		return nil, err
	}

	weekEnd := endOfWeek(today)
	if out.Week, err = s.dayLoads(ctx, `
        SELECT date, available_minutes, is_default, planned_minutes, task_count,
               tasks_without_estimate, overload_minutes
          FROM v_day_load WHERE date BETWEEN ? AND ? ORDER BY date`, today, weekEnd); err != nil {
		return nil, err
	}
	if out.OverloadedDays, err = s.dayLoads(ctx, `
        SELECT date, available_minutes, is_default, planned_minutes, task_count,
               tasks_without_estimate, overload_minutes
          FROM v_overloaded_days ORDER BY date`); err != nil {
		return nil, err
	}

	// A day with no plans at all is absent from v_day_load; report it as an
	// empty day rather than omitting today from the cockpit.
	out.TodayLoad = DayLoad{Date: today}
	for _, d := range out.Week {
		if d.Date == today {
			out.TodayLoad = d
			break
		}
	}
	if out.TodayLoad.AvailableMinutes == 0 {
		if minutes, isDefault, err := s.capacityFor(ctx, today); err == nil {
			out.TodayLoad.AvailableMinutes = minutes
			out.TodayLoad.IsDefaultCapacity = isDefault
			out.TodayLoad.OverloadMinutes = out.TodayLoad.PlannedMinutes - minutes
		}
	}

	if out.Overdue, err = s.overdue(ctx); err != nil {
		return nil, err
	}
	if out.QualityIssues, err = s.QualityIssues(ctx); err != nil {
		return nil, err
	}

	if err := s.fillTotals(ctx, out); err != nil {
		return nil, err
	}
	out.ReviewDue, out.Totals.Truncated = capAgenda(out.ReviewDue, out.Totals.Truncated)
	out.UnscheduledImportant, out.Totals.Truncated = capAgenda(out.UnscheduledImportant, out.Totals.Truncated)
	if len(out.QualityIssues) > ListCap {
		out.QualityIssues = out.QualityIssues[:ListCap]
		out.Totals.Truncated = true
	}
	return out, nil
}

// fillTotals counts each list at full size. COUNT(*) over a view is far
// cheaper than materialising every row.
func (s *Store) fillTotals(ctx context.Context, out *Status) error {
	counts := map[string]*int{
		"v_overdue":               &out.Totals.Overdue,
		"v_review_due":            &out.Totals.ReviewDue,
		"v_unscheduled_important": &out.Totals.UnscheduledImportant,
		"v_overloaded_days":       &out.Totals.OverloadedDays,
		"v_data_quality_issues":   &out.Totals.QualityIssues,
	}
	for view, target := range counts {
		// View names are constants in this map, never caller input.
		if err := s.db.SQL().QueryRowContext(ctx, "SELECT COUNT(*) FROM "+view).Scan(target); err != nil {
			return sqlite.Classify(err)
		}
	}
	out.Totals.TodayEntries = len(out.TodayAgenda)
	return nil
}

func capAgenda(entries []AgendaEntry, truncated bool) ([]AgendaEntry, bool) {
	if len(entries) > ListCap {
		return entries[:ListCap], true
	}
	return entries, truncated
}

func (s *Store) agenda(ctx context.Context, query string, args ...any) ([]AgendaEntry, error) {
	rows, err := s.db.SQL().QueryContext(ctx, query, args...)
	if err != nil {
		return nil, sqlite.Classify(err)
	}
	defer rows.Close()

	out := []AgendaEntry{}
	for rows.Next() {
		var e AgendaEntry
		var date sql.NullString
		var minutes sql.NullInt64
		if err := rows.Scan(&date, &e.Reason, &e.TaskID, &e.Title, &e.Status, &e.Importance,
			&e.ProjectID, &e.ProjectName, &e.AreaName, &e.HardDueAt, &e.NextReviewAt,
			&minutes, &e.ScheduleID, &e.TimeSlot); err != nil {
			return nil, sqlite.Classify(err)
		}
		e.Date = date.String
		if minutes.Valid {
			m := int(minutes.Int64)
			e.EffectiveMinutes = &m
		}
		out = append(out, e)
	}
	return out, sqlite.Classify(rows.Err())
}

func (s *Store) dayLoads(ctx context.Context, query string, args ...any) ([]DayLoad, error) {
	rows, err := s.db.SQL().QueryContext(ctx, query, args...)
	if err != nil {
		return nil, sqlite.Classify(err)
	}
	defer rows.Close()

	out := []DayLoad{}
	for rows.Next() {
		var d DayLoad
		var isDefault int
		var withoutEstimate sql.NullInt64
		if err := rows.Scan(&d.Date, &d.AvailableMinutes, &isDefault, &d.PlannedMinutes,
			&d.TaskCount, &withoutEstimate, &d.OverloadMinutes); err != nil {
			return nil, sqlite.Classify(err)
		}
		d.IsDefaultCapacity = isDefault == 1
		d.TasksWithoutEstimate = int(withoutEstimate.Int64)
		out = append(out, d)
	}
	return out, sqlite.Classify(rows.Err())
}

func (s *Store) overdue(ctx context.Context) ([]OverdueEntry, error) {
	rows, err := s.db.SQL().QueryContext(ctx, `
        SELECT task_id, title, importance, project_name, hard_due_at, days_overdue
          FROM v_overdue ORDER BY hard_due_at`)
	if err != nil {
		return nil, sqlite.Classify(err)
	}
	defer rows.Close()

	out := []OverdueEntry{}
	for rows.Next() {
		var e OverdueEntry
		var days sql.NullInt64
		if err := rows.Scan(&e.TaskID, &e.Title, &e.Importance, &e.ProjectName, &e.HardDueAt, &days); err != nil {
			return nil, sqlite.Classify(err)
		}
		e.DaysOverdue = int(days.Int64)
		out = append(out, e)
	}
	return out, sqlite.Classify(rows.Err())
}

// QualityIssues lists every checkable rule violation (§15).
func (s *Store) QualityIssues(ctx context.Context) ([]QualityIssue, error) {
	rows, err := s.db.SQL().QueryContext(ctx, `
        SELECT entity_type, entity_id, title, issue, detail
          FROM v_data_quality_issues ORDER BY entity_type, issue, title
         LIMIT ?`, ListCap+1)
	if err != nil {
		return nil, sqlite.Classify(err)
	}
	defer rows.Close()

	out := []QualityIssue{}
	for rows.Next() {
		var q QualityIssue
		if err := rows.Scan(&q.EntityType, &q.EntityID, &q.Title, &q.Issue, &q.Detail); err != nil {
			return nil, sqlite.Classify(err)
		}
		out = append(out, q)
	}
	return out, sqlite.Classify(rows.Err())
}

// DayView is one column of the week board.
type DayView struct {
	Load    DayLoad       `json:"load"`
	Entries []AgendaEntry `json:"entries"`
}

// Day returns the agenda and load for a single date.
func (s *Store) Day(ctx context.Context, date string) (*DayView, error) {
	if err := ValidateDate("date", date); err != nil {
		return nil, err
	}
	entries, err := s.agenda(ctx, agendaSelect+
		"v_day_agenda WHERE date = ? ORDER BY reason, importance, title", date)
	if err != nil {
		return nil, err
	}
	loads, err := s.dayLoads(ctx, `
        SELECT date, available_minutes, is_default, planned_minutes, task_count,
               tasks_without_estimate, overload_minutes
          FROM v_day_load WHERE date = ?`, date)
	if err != nil {
		return nil, err
	}
	view := &DayView{Load: DayLoad{Date: date}, Entries: entries}
	if len(loads) > 0 {
		view.Load = loads[0]
	} else if minutes, isDefault, err := s.capacityFor(ctx, date); err == nil {
		view.Load.AvailableMinutes = minutes
		view.Load.IsDefaultCapacity = isDefault
		view.Load.OverloadMinutes = -minutes
	}
	return view, nil
}

// Week returns seven consecutive day views starting at start. It reads the
// range in two queries rather than seven day lookups: v_day_load aggregates
// every planned day regardless of the filter, so querying it per day would
// repeat that work seven times over.
func (s *Store) Week(ctx context.Context, start string) ([]DayView, error) {
	if start == "" {
		start = s.clock.Today()
	}
	if err := ValidateDate("start", start); err != nil {
		return nil, err
	}
	from, _ := time.Parse(system.DateLayout, start)
	end := from.AddDate(0, 0, 6).Format(system.DateLayout)

	entries, err := s.agenda(ctx, agendaSelect+
		"v_day_agenda WHERE date BETWEEN ? AND ? ORDER BY date, reason, importance, title",
		start, end)
	if err != nil {
		return nil, err
	}
	loads, err := s.dayLoads(ctx, `
        SELECT date, available_minutes, is_default, planned_minutes, task_count,
               tasks_without_estimate, overload_minutes
          FROM v_day_load WHERE date BETWEEN ? AND ? ORDER BY date`, start, end)
	if err != nil {
		return nil, err
	}

	byDate := make(map[string]DayLoad, len(loads))
	for _, l := range loads {
		byDate[l.Date] = l
	}
	entriesByDate := make(map[string][]AgendaEntry, 7)
	for _, e := range entries {
		entriesByDate[e.Date] = append(entriesByDate[e.Date], e)
	}

	out := make([]DayView, 0, 7)
	for i := 0; i < 7; i++ {
		date := from.AddDate(0, 0, i).Format(system.DateLayout)
		view := DayView{Entries: entriesByDate[date]}
		if view.Entries == nil {
			view.Entries = []AgendaEntry{}
		}
		if load, ok := byDate[date]; ok {
			view.Load = load
		} else {
			// A day with nothing planned is absent from v_day_load; still
			// report its capacity so the column is not silently blank.
			view.Load = DayLoad{Date: date}
			if minutes, isDefault, err := s.capacityFor(ctx, date); err == nil {
				view.Load.AvailableMinutes = minutes
				view.Load.IsDefaultCapacity = isDefault
				view.Load.OverloadMinutes = -minutes
			}
		}
		out = append(out, view)
	}
	return out, nil
}

// capacityFor resolves a day's capacity, falling back to the configured
// weekday/weekend default and reporting which was used.
func (s *Store) capacityFor(ctx context.Context, date string) (int, bool, error) {
	var minutes int
	err := s.db.SQL().QueryRowContext(ctx,
		`SELECT available_minutes FROM daily_capacity WHERE date = ?`, date).Scan(&minutes)
	if err == nil {
		return minutes, false, nil
	}
	if err != sql.ErrNoRows {
		return 0, false, sqlite.Classify(err)
	}
	parsed, perr := time.Parse(system.DateLayout, date)
	if perr != nil {
		return 0, true, perr
	}
	key := "default_weekday_minutes"
	if wd := parsed.Weekday(); wd == time.Saturday || wd == time.Sunday {
		key = "default_weekend_minutes"
	}
	err = s.db.SQL().QueryRowContext(ctx,
		`SELECT CAST(value AS INTEGER) FROM ops_settings WHERE key = ?`, key).Scan(&minutes)
	if err != nil {
		return 0, true, sqlite.Classify(err)
	}
	return minutes, true, nil
}

func endOfWeek(today string) string {
	t, err := time.Parse(system.DateLayout, today)
	if err != nil {
		return today
	}
	return t.AddDate(0, 0, 6).Format(system.DateLayout)
}
