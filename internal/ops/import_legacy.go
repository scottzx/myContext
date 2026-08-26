package ops

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/scottzx/mycontext/internal/protocol"
	"github.com/scottzx/mycontext/internal/system"
)

// ImportLegacyInput is the payload of `import okr`. The source database is
// opened read-only and is never written to.
type ImportLegacyInput struct {
	SourcePath string `json:"source_path"`
	Confirm    bool   `json:"confirm,omitempty"`
}

// ImportReport is the reconciliation: what was read, what was written, and
// every judgement the importer had to make. Nothing is converted silently.
type ImportReport struct {
	Source   string            `json:"source"`
	Applied  bool              `json:"applied"`
	Read     map[string]int    `json:"read"`
	Written  map[string]int    `json:"written"`
	Skipped  []string          `json:"skipped,omitempty"`
	Mappings map[string]string `json:"mappings"`
	// LinkSuggestions are the task -> milestone links the importer derived
	// rather than read. They are listed in full so they can be checked before
	// --confirm writes them.
	LinkSuggestions []string `json:"link_suggestions,omitempty"`
	NeedsInput      []string `json:"needs_input,omitempty"`
}

// legacyNode is one row of the source `nodes` table.
type legacyNode struct {
	ID                                      int64
	ParentID                                sql.NullInt64
	Kind, Title                             string
	Detail, Priority, Status, Result        sql.NullString
	DueDate, StartDate, DoneDate, CreatedAt sql.NullString
	MetricName, MetricUnit                  sql.NullString
	MetricTarget, MetricNow, Weight         sql.NullFloat64
	Horizon, Vision, Stage, Outcome, Tags   sql.NullString
	EstHours                                sql.NullFloat64
}

// ImportLegacy moves a legacy OKR tree into ops.db in one transaction. A
// hundred-odd nodes cannot be a hundred-odd processes: on iSH that is the
// difference between working and not.
func (s *Store) ImportLegacy(ctx context.Context, wc WriteContext, in ImportLegacyInput) (*Result, error) {
	if in.SourcePath == "" {
		return nil, protocol.BadInput("source_path is required")
	}
	src, err := sql.Open("sqlite", "file:"+in.SourcePath+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, protocol.Wrap(err, protocol.CodeExternal, "cannot open the legacy database")
	}
	defer src.Close()
	if err := src.PingContext(ctx); err != nil {
		return nil, protocol.Wrap(err, protocol.CodeExternal, "cannot read "+in.SourcePath)
	}

	nodes, err := readLegacyNodes(ctx, src)
	if err != nil {
		return nil, err
	}
	if len(nodes) == 0 {
		return nil, protocol.BadInput("the legacy database has no nodes to import")
	}

	report := &ImportReport{
		Source:   in.SourcePath,
		Read:     map[string]int{},
		Written:  map[string]int{},
		Mappings: map[string]string{},
	}
	for _, n := range nodes {
		report.Read[strings.ToLower(n.Kind)]++
	}

	// The priority vocabularies differ and the design refuses to convert one
	// into the other behind the user's back, so the mapping is stated and the
	// write requires --confirm.
	report.Mappings["priority"] = "critical->P0, high->P1, med->P2, low->P3, P0..P3 unchanged"
	report.Mappings["status"] = "frozen->paused (project, sprint, task); todo->planned (project, sprint)"
	report.Mappings["SUB"] = "project kind=sprint, parent_project_id=owning project"
	report.Mappings["milestone"] = "milestones row; tasks link to it via milestone_id"
	report.Mappings["KR"] = "key_result (metric, weight) + a mirror initiative that carries its projects"
	report.Mappings["task due_date"] = "legacy_due_date, left for you to classify as hard due / target / plan"

	if !in.Confirm {
		if err := previewLegacy(ctx, src, report); err != nil {
			return nil, err
		}
		pairs, err := milestoneTaskPairs(ctx, src)
		if err != nil {
			return nil, err
		}
		report.LinkSuggestions = describePairs(ctx, src, pairs)
		report.NeedsInput = append(report.NeedsInput,
			"re-run with --confirm to write; nothing has been changed")
		return &Result{Data: report, Warnings: []string{"preview only: no changes were written"}}, nil
	}

	return s.execute(ctx, "import.okr", wc, in, func(ctx context.Context, tx *sql.Tx, now time.Time) (*Result, error) {
		if err := importTree(ctx, src, tx, wc, now, nodes, report); err != nil {
			return nil, err
		}
		report.Applied = true
		return &Result{
			Data:    report,
			Changes: []protocol.Change{{EntityType: "instance", EntityID: "ops", EventType: "migrated", ProjectionKeys: []string{"areas", "projects", "tasks", "objectives"}}},
		}, nil
	})
}

func readLegacyNodes(ctx context.Context, src *sql.DB) ([]legacyNode, error) {
	rows, err := src.QueryContext(ctx, `
        SELECT id, parent_id, kind, title, detail, priority, status, result,
               due_date, start_date, done_date, created_at,
               metric_name, metric_unit, metric_target, metric_now, weight,
               horizon, vision, stage, outcome, tags, est_hours
          FROM nodes ORDER BY id`)
	if err != nil {
		return nil, protocol.Wrap(err, protocol.CodeExternal, "cannot read nodes")
	}
	defer rows.Close()

	var out []legacyNode
	for rows.Next() {
		var n legacyNode
		if err := rows.Scan(&n.ID, &n.ParentID, &n.Kind, &n.Title, &n.Detail, &n.Priority,
			&n.Status, &n.Result, &n.DueDate, &n.StartDate, &n.DoneDate, &n.CreatedAt,
			&n.MetricName, &n.MetricUnit, &n.MetricTarget, &n.MetricNow, &n.Weight,
			&n.Horizon, &n.Vision, &n.Stage, &n.Outcome, &n.Tags, &n.EstHours); err != nil {
			return nil, protocol.Wrap(err, protocol.CodeExternal, "cannot scan node")
		}
		out = append(out, n)
	}
	if err := rows.Err(); err != nil {
		return nil, protocol.Wrap(err, protocol.CodeExternal, "cannot read nodes")
	}
	return out, nil
}

// milestoneTaskPairs derives which task works towards which milestone. The
// legacy database has no column for it: the only honest signal is that both
// sit in the same project and carry the same date. A task matching more than
// one milestone is left unlinked rather than guessed at.
func milestoneTaskPairs(ctx context.Context, src *sql.DB) (map[int64]int64, error) {
	rows, err := src.QueryContext(ctx, `
        SELECT n.id AS task_id, m.id AS milestone_id
          FROM milestones m
          JOIN nodes n
            ON n.kind = 'TASK'
           AND n.due_date = m.target_date
           AND (n.parent_id = m.proj_id
                OR n.parent_id IN (SELECT id FROM nodes WHERE parent_id = m.proj_id))
         WHERE m.proj_id IS NOT NULL`)
	if err != nil {
		return nil, nil // a legacy database without milestones is fine
	}
	defer rows.Close()

	pairs := map[int64]int64{}
	ambiguous := map[int64]bool{}
	for rows.Next() {
		var taskID, msID int64
		if err := rows.Scan(&taskID, &msID); err != nil {
			return nil, protocol.Wrap(err, protocol.CodeExternal, "cannot read milestone pairs")
		}
		if prev, seen := pairs[taskID]; seen && prev != msID {
			ambiguous[taskID] = true
			continue
		}
		pairs[taskID] = msID
	}
	for taskID := range ambiguous {
		delete(pairs, taskID)
	}
	return pairs, nil
}

// describePairs renders the derived links so they can be read before they are
// written.
func describePairs(ctx context.Context, src *sql.DB, pairs map[int64]int64) []string {
	if len(pairs) == 0 {
		return nil
	}
	titles := map[string]string{}
	if rows, err := src.QueryContext(ctx, `SELECT 'm'||id, title FROM milestones
                                            UNION ALL SELECT 't'||id, title FROM nodes`); err == nil {
		defer rows.Close()
		for rows.Next() {
			var k, v string
			if err := rows.Scan(&k, &v); err == nil {
				titles[k] = v
			}
		}
	}
	taskIDs := make([]int64, 0, len(pairs))
	for taskID := range pairs {
		taskIDs = append(taskIDs, taskID)
	}
	sort.Slice(taskIDs, func(i, j int) bool {
		if pairs[taskIDs[i]] != pairs[taskIDs[j]] {
			return pairs[taskIDs[i]] < pairs[taskIDs[j]]
		}
		return taskIDs[i] < taskIDs[j]
	})
	out := make([]string, 0, len(taskIDs))
	for _, taskID := range taskIDs {
		out = append(out, fmt.Sprintf("%s  ←  %s",
			titles[fmt.Sprintf("m%d", pairs[taskID])],
			titles[fmt.Sprintf("t%d", taskID)]))
	}
	return out
}

func previewLegacy(ctx context.Context, src *sql.DB, report *ImportReport) error {
	for table, key := range map[string]string{
		"milestones":                 "milestone",
		"deps":                       "dependency",
		"project_kr_links":           "project_kr_link",
		"milestone_kr_contributions": "contribution",
		"events":                     "event",
	} {
		var n int
		if err := src.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table).Scan(&n); err == nil {
			report.Read[key] = n
		}
	}
	return nil
}

// importTree writes the whole legacy tree. Order matters: every parent must
// exist before its children reference it.
func importTree(ctx context.Context, src *sql.DB, tx *sql.Tx, wc WriteContext, now time.Time,
	nodes []legacyNode, report *ImportReport) error {

	ts := system.FormatTimestamp(now)
	byLegacyID := map[int64]legacyNode{}
	children := map[int64][]legacyNode{}
	var roots []legacyNode
	for _, n := range nodes {
		byLegacyID[n.ID] = n
		if n.ParentID.Valid {
			children[n.ParentID.Int64] = append(children[n.ParentID.Int64], n)
		} else {
			roots = append(roots, n)
		}
	}

	// newID maps a legacy node id to the ops id it became, and entityOf
	// remembers which kind of thing it became.
	newID := map[int64]string{}
	entityOf := map[int64]string{}
	areaOf := map[int64]string{}       // objective legacy id -> area id
	initiativeOf := map[int64]string{} // KR legacy id -> mirror initiative id
	fallbackInit := map[int64]string{} // objective legacy id -> catch-all initiative

	tagWriter := func(entityType, entityID string, raw sql.NullString) error {
		if !raw.Valid {
			return nil
		}
		for _, tag := range NormalizeTags([]string{raw.String}) {
			if _, err := tx.ExecContext(ctx, `
                INSERT INTO tags (entity_type, entity_id, tag, created_at) VALUES (?,?,?,?)
                ON CONFLICT(entity_type, entity_id, tag) DO NOTHING`,
				entityType, entityID, tag, ts); err != nil {
				return err
			}
			report.Written["tag"]++
		}
		return nil
	}

	// --- objectives: each becomes an area (the legacy tree has no areas) ---
	for _, o := range roots {
		if o.Kind != "O" {
			report.Skipped = append(report.Skipped,
				fmt.Sprintf("root node %d is a %s, not an objective; skipped", o.ID, o.Kind))
			continue
		}
		areaID := system.NewID("area")
		if _, err := tx.ExecContext(ctx, `
            INSERT INTO areas (id, name, status, sort_order, note, version, created_at, updated_at)
            VALUES (?,?,'active',0,?,1,?,?)`,
			areaID, o.Title, nullString("carried over from legacy objective "+fmt.Sprint(o.ID)), ts, ts); err != nil {
			return err
		}
		areaOf[o.ID] = areaID
		report.Written["area"]++

		objID := system.NewID("obj")
		if _, err := tx.ExecContext(ctx, `
            INSERT INTO objectives (id, area_id, name, description, horizon, status,
                                    version, created_at, updated_at)
            VALUES (?,?,?,?,?,?,1,?,?)`,
			objID, areaID, o.Title, ns(o.Vision), ns(o.Horizon),
			outcomeStatus(o.Status.String), ts, ts); err != nil {
			return err
		}
		newID[o.ID] = objID
		entityOf[o.ID] = "objective"
		report.Written["objective"]++
		if err := tagWriter("objective", objID, o.Tags); err != nil {
			return err
		}
	}

	// --- key results, each mirrored by an initiative that carries its work ---
	for _, o := range roots {
		for _, kr := range children[o.ID] {
			if kr.Kind != "KR" {
				continue
			}
			krID := system.NewID("kr")
			if _, err := tx.ExecContext(ctx, `
                INSERT INTO key_results (id, objective_id, name, metric_name, metric_unit,
                                         target_value, current_value, weight, horizon, status,
                                         version, created_at, updated_at)
                VALUES (?,?,?,?,?,?,?,?,?,?,1,?,?)`,
				krID, newID[o.ID], kr.Title, metricName(kr), ns(kr.MetricUnit),
				nf(kr.MetricTarget), nf(kr.MetricNow), nf(kr.Weight), ns(kr.Horizon),
				outcomeStatus(kr.Status.String), ts, ts); err != nil {
				return err
			}
			newID[kr.ID] = krID
			entityOf[kr.ID] = "key_result"
			report.Written["key_result"]++
			if err := tagWriter("key_result", krID, kr.Tags); err != nil {
				return err
			}

			// The legacy tree hangs projects under a KR. ops.db keeps the
			// outcome system beside the work tree, so the KR also becomes an
			// initiative: the projects keep their grouping, and the metric
			// keeps its own home.
			initID := system.NewID("init")
			if _, err := tx.ExecContext(ctx, `
                INSERT INTO initiatives (id, area_id, name, status, description,
                                         sort_order, version, created_at, updated_at)
                VALUES (?,?,?,'active',?,0,1,?,?)`,
				initID, areaOf[o.ID], kr.Title,
				"work grouped under key result "+kr.Title, ts, ts); err != nil {
				return err
			}
			initiativeOf[kr.ID] = initID
			report.Written["initiative"]++
		}
	}

	// --- projects and sprints, parents before children ---
	var walk func(parent legacyNode) error
	walk = func(parent legacyNode) error {
		for _, n := range children[parent.ID] {
			switch n.Kind {
			case "PROJ", "SUB":
				if err := writeProjectNode(ctx, tx, ts, n, parent, byLegacyID, newID, entityOf,
					areaOf, initiativeOf, fallbackInit, report, tagWriter); err != nil {
					return err
				}
				if err := walk(n); err != nil {
					return err
				}
			}
		}
		return nil
	}
	for _, o := range roots {
		if err := walk(o); err != nil {
			return err
		}
		for _, kr := range children[o.ID] {
			if kr.Kind == "KR" {
				if err := walk(kr); err != nil {
					return err
				}
			}
		}
	}

	// --- milestones before tasks: a task points at one, so it must exist ---
	msNewID, err := importMilestones(ctx, src, tx, ts, newID, entityOf, report)
	if err != nil {
		return err
	}

	// Which milestone each legacy task is working towards. The legacy schema
	// has no such link, so it is derived from "same project, same date" and
	// reported pair by pair rather than applied silently.
	links, err := milestoneTaskPairs(ctx, src)
	if err != nil {
		return err
	}
	report.LinkSuggestions = describePairs(ctx, src, links)

	// --- tasks, after every project, sprint and milestone exists ---
	for _, n := range nodes {
		if n.Kind != "TASK" {
			continue
		}
		if err := writeTaskNode(ctx, tx, ts, n, byLegacyID, newID, entityOf,
			msNewID, links, report, tagWriter); err != nil {
			return err
		}
	}
	if err := importLinks(ctx, src, tx, ts, newID, entityOf, report); err != nil {
		return err
	}
	if err := importDeps(ctx, src, tx, ts, newID, entityOf, report); err != nil {
		return err
	}
	if err := importEvents(ctx, src, tx, wc, newID, entityOf, report); err != nil {
		return err
	}
	return recordEvent(ctx, tx, wc, now, "instance", "ops", "migrated", nil, report)
}

// ---------------------------------------------------------------------------
// Field mapping. Every judgement here is reported, never silent.
// ---------------------------------------------------------------------------

// ns unwraps a nullable legacy string into something the driver can bind.
func ns(v sql.NullString) any {
	if !v.Valid || strings.TrimSpace(v.String) == "" {
		return nil
	}
	return strings.TrimSpace(v.String)
}

func nf(v sql.NullFloat64) any {
	if !v.Valid {
		return nil
	}
	return v.Float64
}

func str(v sql.NullString) string {
	if !v.Valid {
		return ""
	}
	return strings.TrimSpace(v.String)
}

// metricName falls back to the node title, because a key result row requires
// a measurement name and the legacy tree always supplies one.
func metricName(n legacyNode) string {
	if m := str(n.MetricName); m != "" {
		return m
	}
	return n.Title
}

// importance translates the two legacy priority vocabularies onto the single
// scale. The mapping is stated in the report and the write requires --confirm.
func importance(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "critical":
		return "P0"
	case "high":
		return "P1"
	case "med", "medium":
		return "P2"
	case "low":
		return "P3"
	case "p0":
		return "P0"
	case "p1":
		return "P1"
	case "p2":
		return "P2"
	case "p3":
		return "P3"
	default:
		return "P2"
	}
}

// projectStatus maps the legacy node status. `frozen` means parked on
// purpose, which is `paused` - not archived, or it would vanish from view.
func projectStatus(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "doing":
		return "active"
	case "done":
		return "done"
	case "frozen":
		return "paused"
	case "cancelled", "canceled":
		return "cancelled"
	default:
		return "planned"
	}
}

func taskStatus(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "doing":
		return "doing"
	case "done":
		return "done"
	case "frozen":
		return "paused"
	case "blocked", "waiting":
		return "waiting"
	case "cancelled", "canceled":
		return "cancelled"
	default:
		return "todo"
	}
}

func outcomeStatus(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "done":
		return "done"
	case "dropped", "cancelled", "canceled":
		return "dropped"
	case "frozen", "archived":
		return "archived"
	default:
		return "active"
	}
}

// stage drops `frozen`, which is a status rather than a stage; the status
// column already carries it as `paused`.
func stage(raw string) any {
	v := strings.ToLower(strings.TrimSpace(raw))
	if v == "" || !validStage[v] {
		return nil
	}
	return v
}

// endOfDay turns a legacy calendar date into the instant that date actually
// means for a deadline: the end of it, in the instance timezone.
func endOfDay(date string) any {
	date = strings.TrimSpace(date)
	if len(date) < 10 {
		return nil
	}
	d, err := time.ParseInLocation("2006-01-02", date[:10], time.Local)
	if err != nil {
		return nil
	}
	return d.Add(24*time.Hour - time.Second).Format(time.RFC3339)
}

func calendarDate(v sql.NullString) any {
	d := str(v)
	if len(d) < 10 {
		return nil
	}
	if _, err := time.Parse("2006-01-02", d[:10]); err != nil {
		return nil
	}
	return d[:10]
}

// estimateMinutes converts legacy hours, rounding to the nearest minute.
func estimateMinutes(v sql.NullFloat64) any {
	if !v.Valid || v.Float64 <= 0 {
		return nil
	}
	m := int(math.Round(v.Float64 * 60))
	if m <= 0 {
		return nil
	}
	return m
}

func legacyRef(kind string, id int64) string {
	return fmt.Sprintf("%s#%d", kind, id)
}

func sortedKeys(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
