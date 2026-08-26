package ops

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/scottzx/mycontext/internal/system"
)

type tagFn func(entityType, entityID string, raw sql.NullString) error

// writeProjectNode turns a legacy PROJ or SUB into a project row. A SUB
// becomes a sprint: same table, same commands, one extra field saying it is a
// time box inside its parent.
func writeProjectNode(ctx context.Context, tx *sql.Tx, ts string,
	n, parent legacyNode, byLegacyID map[int64]legacyNode,
	newID, entityOf, areaOf, initiativeOf, fallbackInit map[int64]string,
	report *ImportReport, writeTags tagFn) error {

	kind := "project"
	if n.Kind == "SUB" {
		kind = "sprint"
	}

	var parentProject any
	var initiativeID any
	switch parent.Kind {
	case "O":
		// A project hanging straight off an objective has no grouping of its
		// own; give the area one catch-all initiative rather than inventing a
		// different one per project.
		id, ok := fallbackInit[parent.ID]
		if !ok {
			id = system.NewID("init")
			if _, err := tx.ExecContext(ctx, `
                INSERT INTO initiatives (id, area_id, name, status, description,
                                         sort_order, version, created_at, updated_at)
                VALUES (?,?,?,'active',?,99,1,?,?)`,
				id, areaOf[parent.ID], "未归入 KR 的工作",
				"projects that hung directly off the legacy objective", ts, ts); err != nil {
				return err
			}
			fallbackInit[parent.ID] = id
			report.Written["initiative"]++
		}
		initiativeID = id
	case "KR":
		initiativeID = initiativeOf[parent.ID]
	case "PROJ", "SUB":
		parentProject = newID[parent.ID]
		if v := inheritedInitiative(ctx, tx, newID[parent.ID]); v != "" {
			initiativeID = v
		}
	}

	id := system.NewID("proj")
	if kind == "sprint" {
		id = system.NewID("sprint")
	}

	// A SUB's due date is the end of its window; a project's is an intention.
	var startDate, endDate, targetDate any
	startDate = calendarDate(n.StartDate)
	if kind == "sprint" {
		endDate = calendarDate(n.DueDate)
	} else {
		targetDate = calendarDate(n.DueDate)
	}

	if _, err := tx.ExecContext(ctx, `
        INSERT INTO projects (id, initiative_id, parent_project_id, kind, name, description,
                              status, stage, importance, start_date, end_date, target_date,
                              outcome, metric_name, metric_unit, target_value, current_value,
                              legacy_ref, sort_order, version, created_at, updated_at)
        VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,0,1,?,?)`,
		id, initiativeID, parentProject, kind, n.Title, ns(n.Detail),
		projectStatus(str(n.Status)), stage(str(n.Stage)), importance(str(n.Priority)),
		startDate, endDate, targetDate,
		firstNonEmpty(ns(n.Outcome), ns(n.Result)),
		ns(n.MetricName), ns(n.MetricUnit), nf(n.MetricTarget), nf(n.MetricNow),
		legacyRef(n.Kind, n.ID), ts, ts); err != nil {
		return fmt.Errorf("%s %d (%s): %w", n.Kind, n.ID, n.Title, err)
	}
	newID[n.ID] = id
	entityOf[n.ID] = "project"
	report.Written[kind]++
	return writeTags("project", id, n.Tags)
}

// inheritedInitiative reads the initiative a parent project already sits in,
// so a nested project or sprint stays in the same branch of the tree.
func inheritedInitiative(ctx context.Context, tx *sql.Tx, projectID string) string {
	if projectID == "" {
		return ""
	}
	var v sql.NullString
	if err := tx.QueryRowContext(ctx,
		`SELECT initiative_id FROM projects WHERE id = ?`, projectID).Scan(&v); err != nil {
		return ""
	}
	return v.String
}

// writeTaskNode turns a legacy TASK into a task row. Its due date lands in
// legacy_due_date on purpose: the design refuses to guess whether a carried
// over date was a real deadline, an intention or a plan, and surfaces it as a
// data quality issue for the user to classify.
func writeTaskNode(ctx context.Context, tx *sql.Tx, ts string, n legacyNode,
	byLegacyID map[int64]legacyNode, newID, entityOf, msNewID map[int64]string,
	links map[int64]int64, report *ImportReport, writeTags tagFn) error {

	var projectID, parentTaskID any
	if n.ParentID.Valid {
		parent := byLegacyID[n.ParentID.Int64]
		switch parent.Kind {
		case "PROJ", "SUB":
			projectID = newID[parent.ID]
		case "TASK":
			parentTaskID = newID[parent.ID]
			var owner sql.NullString
			if err := tx.QueryRowContext(ctx,
				`SELECT project_id FROM tasks WHERE id = ?`, newID[parent.ID]).Scan(&owner); err == nil && owner.Valid {
				projectID = owner.String
			}
		}
	}

	// The derived link, when the source gave an unambiguous one.
	var milestoneID any
	if msLegacy, ok := links[n.ID]; ok {
		if mapped, ok := msNewID[msLegacy]; ok {
			milestoneID = mapped
			report.Written["task_milestone_link"]++
		}
	}

	id := system.NewID("task")
	if _, err := tx.ExecContext(ctx, `
        INSERT INTO tasks (id, project_id, parent_task_id, milestone_id, title, detail,
                           completion_criteria, status, importance, earliest_start_at,
                           estimate_minutes, metric_name, metric_unit, target_value,
                           current_value, legacy_ref, legacy_due_date,
                           version, created_at, updated_at, completed_at)
        VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,1,?,?,?)`,
		id, projectID, parentTaskID, milestoneID, n.Title, ns(n.Detail), ns(n.Result),
		taskStatus(str(n.Status)), importance(str(n.Priority)),
		endOfDay(str(n.StartDate)), estimateMinutes(n.EstHours),
		ns(n.MetricName), ns(n.MetricUnit), nf(n.MetricTarget), nf(n.MetricNow),
		legacyRef(n.Kind, n.ID), calendarDate(n.DueDate),
		ts, ts, endOfDay(str(n.DoneDate))); err != nil {
		return fmt.Errorf("TASK %d (%s): %w", n.ID, n.Title, err)
	}
	newID[n.ID] = id
	entityOf[n.ID] = "task"
	report.Written["task"]++
	if calendarDate(n.DueDate) != nil {
		report.Written["legacy_due_date_to_classify"]++
	}
	return writeTags("task", id, n.Tags)
}

// importMilestones turns each legacy milestone into a task marked as a dated
// checkpoint. It is not a separate element: everything that already knows how
// to list, filter and complete a task handles it unchanged.
func importMilestones(ctx context.Context, src *sql.DB, tx *sql.Tx, ts string,
	newID, entityOf map[int64]string, report *ImportReport) (map[int64]string, error) {

	msNewID := map[int64]string{}
	rows, err := src.QueryContext(ctx, `
        SELECT id, o_id, kr_id, proj_id, title, target_date, metric_target,
               metric_name, metric_unit, status, note
          FROM milestones ORDER BY id`)
	if err != nil {
		return msNewID, nil // a legacy database without milestones is fine
	}
	defer rows.Close()

	for rows.Next() {
		var id int64
		var oID, krID, projID sql.NullInt64
		var title string
		var targetDate, metricName, metricUnit, status, note sql.NullString
		var metricTarget sql.NullFloat64
		if err := rows.Scan(&id, &oID, &krID, &projID, &title, &targetDate,
			&metricTarget, &metricName, &metricUnit, &status, &note); err != nil {
			return msNewID, err
		}
		date := calendarDate(targetDate)
		if date == nil {
			report.Skipped = append(report.Skipped,
				fmt.Sprintf("milestone %d (%s) has no usable date; skipped", id, title))
			continue
		}
		var projectID, keyResultID any
		if projID.Valid {
			if mapped, ok := newID[projID.Int64]; ok && entityOf[projID.Int64] == "project" {
				projectID = mapped
			}
		}
		if krID.Valid {
			if mapped, ok := newID[krID.Int64]; ok && entityOf[krID.Int64] == "key_result" {
				keyResultID = mapped
			}
		}
		newMsID := system.NewID("ms")
		msStatus := milestoneStatus(str(status))
		var reachedAt any
		if msStatus == "hit" {
			reachedAt = ts
		}
		if _, err := tx.ExecContext(ctx, `
            INSERT INTO milestones (id, project_id, key_result_id, name, note, target_date,
                                    status, importance, metric_name, metric_unit, target_value,
                                    legacy_ref, version, created_at, updated_at, reached_at)
            VALUES (?,?,?,?,?,?,?,'P1',?,?,?,?,1,?,?,?)`,
			newMsID, projectID, keyResultID, title, ns(note), date,
			msStatus, ns(metricName), ns(metricUnit), nf(metricTarget),
			legacyRef("MILESTONE", id), ts, ts, reachedAt); err != nil {
			return msNewID, fmt.Errorf("milestone %d (%s): %w", id, title, err)
		}
		msNewID[id] = newMsID
		report.Written["milestone"]++

		// The key result link also becomes a contribution edge, so the score
		// the user recorded has somewhere to live.
		if keyResultID != nil {
			if err := insertDep(ctx, tx, ts, "milestone", newMsID, "key_result",
				keyResultID.(string), "supports", nil,
				"milestone contributes to this key result"); err != nil {
				return msNewID, err
			}
			report.Written["dependency"]++
		}
	}
	if err := importContributions(ctx, src, tx, ts, msNewID, newID, entityOf, report); err != nil {
		return msNewID, err
	}
	return msNewID, nil
}

// milestoneStatus maps the legacy verbs onto pending/at_risk/hit/missed/
// cancelled. `pending` is the honest default: the legacy tree marks nothing
// as reached.
func milestoneStatus(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "done", "hit", "contributed":
		return "hit"
	case "missed", "failed":
		return "missed"
	case "cancelled", "canceled", "unlinked":
		return "cancelled"
	case "at_risk", "risk":
		return "at_risk"
	default:
		return "pending"
	}
}

// importContributions records how much each milestone was judged to have
// contributed to a key result, as the weight on the supporting edge.
func importContributions(ctx context.Context, src *sql.DB, tx *sql.Tx, ts string,
	msNewID map[int64]string, newID, entityOf map[int64]string, report *ImportReport) error {

	rows, err := src.QueryContext(ctx, `
        SELECT milestone_id, kr_id, contribution_score, status, note
          FROM milestone_kr_contributions`)
	if err != nil {
		return nil
	}
	defer rows.Close()

	for rows.Next() {
		var msID int64
		var krID sql.NullInt64
		var score sql.NullFloat64
		var status, note sql.NullString
		if err := rows.Scan(&msID, &krID, &score, &status, &note); err != nil {
			return err
		}
		milestoneID, ok := msNewID[msID]
		if !ok || !krID.Valid {
			continue
		}
		krNew, ok := newID[krID.Int64]
		if !ok || entityOf[krID.Int64] != "key_result" {
			continue
		}
		var weight any
		if score.Valid && score.Float64 >= 0 && score.Float64 <= 1 {
			weight = score.Float64
		}
		if _, err := tx.ExecContext(ctx, `
            UPDATE dependencies SET weight = ?, note = COALESCE(?, note)
             WHERE from_type='milestone' AND from_id=? AND to_type='key_result' AND to_id=?
               AND dependency_type='supports'`,
			weight, ns(note), milestoneID, krNew); err != nil {
			return err
		}
		report.Written["contribution"]++
	}
	return nil
}

// importLinks carries over which project serves which key result.
func importLinks(ctx context.Context, src *sql.DB, tx *sql.Tx, ts string,
	newID, entityOf map[int64]string, report *ImportReport) error {

	rows, err := src.QueryContext(ctx, `
        SELECT project_id, kr_id, link_type, note FROM project_kr_links WHERE status <> 'unlinked'`)
	if err != nil {
		return nil
	}
	defer rows.Close()

	for rows.Next() {
		var projID int64
		var krID sql.NullInt64
		var linkType, note sql.NullString
		if err := rows.Scan(&projID, &krID, &linkType, &note); err != nil {
			return err
		}
		if !krID.Valid {
			continue
		}
		projNew, okP := newID[projID]
		krNew, okK := newID[krID.Int64]
		if !okP || !okK || entityOf[projID] != "project" || entityOf[krID.Int64] != "key_result" {
			report.Skipped = append(report.Skipped,
				fmt.Sprintf("project_kr_link %d->%d references a node that was not imported", projID, krID.Int64))
			continue
		}
		if _, err := tx.ExecContext(ctx, `
            INSERT INTO project_kr_links (project_id, key_result_id, note, created_at)
            VALUES (?,?,?,?)
            ON CONFLICT(project_id, key_result_id) DO UPDATE SET note = excluded.note`,
			projNew, krNew, ns(note), ts); err != nil {
			return err
		}
		report.Written["project_kr_link"]++
	}
	return nil
}

// importDeps carries the whole edge set over, including the 13 of 18 edges
// that are not task-to-task and the `supports` type the old table could not
// express.
func importDeps(ctx context.Context, src *sql.DB, tx *sql.Tx, ts string,
	newID, entityOf map[int64]string, report *ImportReport) error {

	rows, err := src.QueryContext(ctx, `SELECT from_node_id, to_node_id, type, note FROM deps`)
	if err != nil {
		return nil
	}
	defer rows.Close()

	for rows.Next() {
		var from, to int64
		var depType, note sql.NullString
		if err := rows.Scan(&from, &to, &depType, &note); err != nil {
			return err
		}
		fromNew, okF := newID[from]
		toNew, okT := newID[to]
		if !okF || !okT {
			report.Skipped = append(report.Skipped,
				fmt.Sprintf("dependency %d->%d references a node that was not imported", from, to))
			continue
		}
		t := strings.TrimSpace(strings.ToLower(depType.String))
		if !validDependency[t] {
			t = "related"
		}
		if err := insertDep(ctx, tx, ts, entityOf[from], fromNew, entityOf[to], toNew,
			t, nil, str(note)); err != nil {
			return err
		}
		report.Written["dependency"]++
	}
	return nil
}

func insertDep(ctx context.Context, tx *sql.Tx, ts, fromType, fromID, toType, toID,
	depType string, weight any, note string) error {

	_, err := tx.ExecContext(ctx, `
        INSERT INTO dependencies (id, from_type, from_id, to_type, to_id,
                                  dependency_type, weight, note, created_at)
        VALUES (?,?,?,?,?,?,?,?,?)
        ON CONFLICT(from_type, from_id, to_type, to_id, dependency_type) DO NOTHING`,
		system.NewID("dep"), fromType, fromID, toType, toID, depType,
		weight, nullString(note), ts)
	return err
}

// importEvents replays the legacy history. The original verb is preserved
// verbatim in `reason`, so mapping onto the canonical vocabulary loses
// nothing that a review would want to read.
func importEvents(ctx context.Context, src *sql.DB, tx *sql.Tx, wc WriteContext,
	newID, entityOf map[int64]string, report *ImportReport) error {

	rows, err := src.QueryContext(ctx, `
        SELECT date, node_id, type, from_val, to_val, note FROM events ORDER BY id`)
	if err != nil {
		return nil
	}
	defer rows.Close()

	for rows.Next() {
		var date, eventType, fromVal, toVal, note sql.NullString
		var nodeID sql.NullInt64
		if err := rows.Scan(&date, &nodeID, &eventType, &fromVal, &toVal, &note); err != nil {
			return err
		}
		if !nodeID.Valid {
			continue
		}
		entityID, ok := newID[nodeID.Int64]
		if !ok {
			report.Written["event_orphaned"]++
			continue
		}
		occurred := endOfDay(str(date))
		if occurred == nil {
			continue
		}
		before, after := any(nil), any(nil)
		if v := str(fromVal); v != "" {
			before = `{"value":` + quoteJSON(v) + `}`
		}
		if v := str(toVal); v != "" {
			after = `{"value":` + quoteJSON(v) + `}`
		}
		if _, err := tx.ExecContext(ctx, `
            INSERT INTO events (id, entity_type, entity_id, event_type, before_json, after_json,
                                actor_type, actor_id, entry_point, reason, confirmed,
                                request_id, correlation_id, occurred_at)
            VALUES (?,?,?,?,?,?,'migration',NULL,'import',?,0,?,NULL,?)`,
			system.NewID("evt"), entityOf[nodeID.Int64], entityID,
			canonicalEventType(str(eventType)), before, after,
			legacyEventReason(str(eventType), str(note)),
			nullString(wc.RequestID), occurred); err != nil {
			return err
		}
		report.Written["event"]++
	}
	return nil
}

// canonicalEventType maps the 21 free-text legacy verbs onto the 13 the audit
// log defines.
func canonicalEventType(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "created":
		return "created"
	case "metric_update", "amount_set", "target_change":
		return "metric_updated"
	case "status_change", "status", "phase_close":
		return "status_changed"
	case "done", "completed":
		return "completed"
	case "plan_change":
		return "rescheduled"
	case "due_change":
		return "deadline_changed"
	case "deleted":
		return "unlinked"
	case "milestone", "restructure", "schema_v3", "schema_upgrade":
		return "migrated"
	case "title_update", "updated":
		return "updated"
	default:
		return "note"
	}
}

func legacyEventReason(rawType, note string) any {
	parts := []string{}
	if rawType != "" {
		parts = append(parts, "legacy:"+rawType)
	}
	if note != "" {
		parts = append(parts, note)
	}
	if len(parts) == 0 {
		return nil
	}
	return strings.Join(parts, " · ")
}

func quoteJSON(v string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`, "\r", `\r`, "\t", `\t`)
	return `"` + r.Replace(v) + `"`
}

func firstNonEmpty(values ...any) any {
	for _, v := range values {
		if v != nil {
			return v
		}
	}
	return nil
}
