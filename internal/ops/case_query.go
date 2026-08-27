package ops

import (
	"context"
	"database/sql"
	"encoding/json"
	"strconv"

	"github.com/scottzx/mycontext/internal/adapters/sqlite"
	"github.com/scottzx/mycontext/internal/protocol"
)

// The case workspace's read model (design §7).
//
// Every query here is a thin wrapper over a view. That is deliberate: which
// rows belong to a case, and in what order, is decided once in SQL and reused
// by the CLI, the HTTP API and the frontend. React re-implementing "does this
// document belong to this deal" is how three surfaces end up with three
// different answers to the same question.

// CaseIndexRow is one row of v_case_index: the case list, and the header of the
// workspace.
type CaseIndexRow struct {
	RootType          string  `json:"root_type"`
	RootID            string  `json:"root_id"`
	Title             string  `json:"title"`
	Kind              string  `json:"kind"`
	Stage             string  `json:"stage"`
	Owner             *string `json:"owner,omitempty"`
	Importance        string  `json:"importance"`
	PrimaryProjectID  *string `json:"primary_project_id,omitempty"`
	CounterpartyName  string  `json:"counterparty_name"`
	NextReviewAt      *string `json:"next_review_at,omitempty"`
	NextMilestoneAt   *string `json:"next_milestone_at,omitempty"`
	NextMilestoneName *string `json:"next_milestone_name,omitempty"`
	NextActionAt      *string `json:"next_action_at,omitempty"`
	LastInteractionAt *string `json:"last_interaction_at,omitempty"`
	LastEvidenceAt    *string `json:"last_evidence_at,omitempty"`
	OpenTaskCount     int     `json:"open_task_count"`
	OverdueCount      int     `json:"overdue_count"`
	WarningCount      int     `json:"warning_count"`
}

const caseIndexColumns = `
    root_type, root_id, title, kind, stage, owner, importance, primary_project_id,
    counterparty_name, next_review_at, next_milestone_at, next_milestone_name,
    next_action_at, last_interaction_at, last_evidence_at,
    open_task_count, overdue_count, warning_count`

func scanCaseIndex(row interface{ Scan(...any) error }) (CaseIndexRow, error) {
	var c CaseIndexRow
	err := row.Scan(&c.RootType, &c.RootID, &c.Title, &c.Kind, &c.Stage, &c.Owner,
		&c.Importance, &c.PrimaryProjectID, &c.CounterpartyName, &c.NextReviewAt,
		&c.NextMilestoneAt, &c.NextMilestoneName, &c.NextActionAt, &c.LastInteractionAt,
		&c.LastEvidenceAt, &c.OpenTaskCount, &c.OverdueCount, &c.WarningCount)
	return c, err
}

// CaseFilter is the query surface of `case.list`.
type CaseFilter struct {
	RootType string
	Stage    string
	OpenOnly bool
	Search   string
	Limit    int
}

// ListCases answers `case.list`. Ordering puts the ones that need attention
// first - overdue work, then the nearest commitment - because the list is a
// worklist, not a directory.
func (s *Store) ListCases(ctx context.Context, f CaseFilter) ([]CaseIndexRow, error) {
	query := `SELECT ` + caseIndexColumns + ` FROM v_case_index WHERE 1=1`
	var args []any
	if f.RootType != "" {
		query += ` AND root_type = ?`
		args = append(args, f.RootType)
	}
	if f.Stage != "" {
		query += ` AND stage = ?`
		args = append(args, f.Stage)
	}
	if f.OpenOnly {
		query += ` AND stage NOT IN ('won','lost')`
	}
	if f.Search != "" {
		query += ` AND (title LIKE ? OR counterparty_name LIKE ?)`
		args = append(args, "%"+f.Search+"%", "%"+f.Search+"%")
	}
	limit := f.Limit
	if limit <= 0 {
		limit = 100
	}
	query += ` ORDER BY overdue_count DESC,
                        COALESCE(next_action_at, next_milestone_at, '9999-12-31'),
                        title
               LIMIT ?`
	args = append(args, limit)

	rows, err := s.db.SQL().QueryContext(ctx, query, args...)
	if err != nil {
		return nil, sqlite.Classify(err)
	}
	defer rows.Close()
	out := []CaseIndexRow{}
	for rows.Next() {
		c, err := scanCaseIndex(rows)
		if err != nil {
			return nil, sqlite.Classify(err)
		}
		out = append(out, c)
	}
	return out, sqlite.Classify(rows.Err())
}

// CaseTimelineItem is one row of v_case_timeline.
type CaseTimelineItem struct {
	ItemType      string  `json:"item_type"`
	ItemID        string  `json:"item_id"`
	OccurredAt    string  `json:"occurred_at"`
	Title         *string `json:"title,omitempty"`
	Summary       *string `json:"summary,omitempty"`
	Actor         *string `json:"actor,omitempty"`
	DocumentID    *string `json:"document_id,omitempty"`
	SourceCount   int     `json:"source_count"`
	CorrelationID *string `json:"correlation_id,omitempty"`
}

// CaseTimeline is what `case.timeline` returns.
type CaseTimeline struct {
	RootType          string             `json:"root_type"`
	RootID            string             `json:"root_id"`
	ProjectionVersion int                `json:"projection_version"`
	Items             []CaseTimelineItem `json:"items"`
	NextCursor        string             `json:"next_cursor,omitempty"`
}

// GetCaseTimeline reads the timeline newest-first, paging on a stable cursor of
// (occurred_at, item_id) so a confirm landing mid-scroll cannot make a client
// skip or repeat a row.
func (s *Store) GetCaseTimeline(ctx context.Context, rootType, rootID, cursor string, limit int) (*CaseTimeline, error) {
	if rootType == "" || rootID == "" {
		return nil, protocol.BadInput("root_type and root_id are required")
	}
	if limit <= 0 {
		limit = 100
	}
	query := `
        SELECT item_type, item_id, occurred_at, title, summary, actor,
               document_id, source_count, correlation_id
          FROM v_case_timeline
         WHERE root_type = ? AND root_id = ?`
	args := []any{rootType, rootID}
	if cursor != "" {
		at, id, err := decodeCursor(cursor)
		if err != nil {
			return nil, err
		}
		query += ` AND (occurred_at < ? OR (occurred_at = ? AND item_id < ?))`
		args = append(args, at, at, id)
	}
	query += ` ORDER BY occurred_at DESC, sort_priority, item_id DESC LIMIT ?`
	args = append(args, limit+1)

	rows, err := s.db.SQL().QueryContext(ctx, query, args...)
	if err != nil {
		return nil, sqlite.Classify(err)
	}
	defer rows.Close()

	out := &CaseTimeline{RootType: rootType, RootID: rootID,
		ProjectionVersion: ProjectionVersion, Items: []CaseTimelineItem{}}
	for rows.Next() {
		var it CaseTimelineItem
		if err := rows.Scan(&it.ItemType, &it.ItemID, &it.OccurredAt, &it.Title, &it.Summary,
			&it.Actor, &it.DocumentID, &it.SourceCount, &it.CorrelationID); err != nil {
			return nil, sqlite.Classify(err)
		}
		out.Items = append(out.Items, it)
	}
	if err := rows.Err(); err != nil {
		return nil, sqlite.Classify(err)
	}
	if len(out.Items) > limit {
		last := out.Items[limit-1]
		out.Items = out.Items[:limit]
		out.NextCursor = encodeCursor(last.OccurredAt, last.ItemID)
	}
	return out, nil
}

func encodeCursor(occurredAt, itemID string) string {
	raw, _ := json.Marshal([2]string{occurredAt, itemID})
	return string(raw)
}

func decodeCursor(cursor string) (string, string, error) {
	var parts [2]string
	if err := json.Unmarshal([]byte(cursor), &parts); err != nil {
		return "", "", protocol.BadInput("invalid cursor")
	}
	return parts[0], parts[1], nil
}

// CaseNextActions is what `case.next-actions` returns: everything ahead of the
// case, which is the complement of the timeline rather than a slice of it.
type CaseNextActions struct {
	RootType          string          `json:"root_type"`
	RootID            string          `json:"root_id"`
	ProjectionVersion int             `json:"projection_version"`
	NextMilestoneAt   *string         `json:"next_milestone_at,omitempty"`
	NextMilestoneName *string         `json:"next_milestone_name,omitempty"`
	NextActionAt      *string         `json:"next_action_at,omitempty"`
	OpenTaskCount     int             `json:"open_task_count"`
	OverdueCount      int             `json:"overdue_count"`
	Milestones        []CaseMilestone `json:"milestones"`
	Tasks             []CaseTask      `json:"tasks"`
}

type CaseMilestone struct {
	MilestoneID string  `json:"milestone_id"`
	Name        string  `json:"name"`
	TargetDate  string  `json:"target_date"`
	Status      string  `json:"status"`
	ReachedAt   *string `json:"reached_at,omitempty"`
	OpenTasks   int     `json:"open_tasks"`
	TotalTasks  int     `json:"total_tasks"`
}

type CaseTask struct {
	TaskID      string  `json:"task_id"`
	Title       string  `json:"title"`
	Status      string  `json:"status"`
	Importance  string  `json:"importance"`
	PlannedDate *string `json:"planned_date,omitempty"`
	HardDueAt   *string `json:"hard_due_at,omitempty"`
	MilestoneID *string `json:"milestone_id,omitempty"`
}

// GetCaseNextActions reads the forward-looking half of a case.
func (s *Store) GetCaseNextActions(ctx context.Context, rootType, rootID string) (*CaseNextActions, error) {
	if rootType == "" || rootID == "" {
		return nil, protocol.BadInput("root_type and root_id are required")
	}
	out := &CaseNextActions{RootType: rootType, RootID: rootID,
		ProjectionVersion: ProjectionVersion,
		Milestones:        []CaseMilestone{}, Tasks: []CaseTask{}}

	err := s.db.SQL().QueryRowContext(ctx, `
        SELECT next_milestone_at, next_milestone_name, next_action_at,
               open_task_count, overdue_count
          FROM v_case_next_action WHERE root_type = ? AND root_id = ?`,
		rootType, rootID).Scan(&out.NextMilestoneAt, &out.NextMilestoneName,
		&out.NextActionAt, &out.OpenTaskCount, &out.OverdueCount)
	if err != nil && err != sql.ErrNoRows {
		return nil, sqlite.Classify(err)
	}

	rows, err := s.db.SQL().QueryContext(ctx, `
        SELECT m.id, m.name, m.target_date, m.status, m.reached_at,
               (SELECT COUNT(*) FROM tasks t WHERE t.milestone_id = m.id
                 AND t.status NOT IN ('done','cancelled','archived')),
               (SELECT COUNT(*) FROM tasks t WHERE t.milestone_id = m.id)
          FROM v_case_root_membership mb
          JOIN milestones m ON m.id = mb.member_id
         WHERE mb.root_type = ? AND mb.root_id = ? AND mb.member_type = 'milestone'
         ORDER BY m.target_date`, rootType, rootID)
	if err != nil {
		return nil, sqlite.Classify(err)
	}
	for rows.Next() {
		var m CaseMilestone
		if err := rows.Scan(&m.MilestoneID, &m.Name, &m.TargetDate, &m.Status,
			&m.ReachedAt, &m.OpenTasks, &m.TotalTasks); err != nil {
			rows.Close()
			return nil, sqlite.Classify(err)
		}
		out.Milestones = append(out.Milestones, m)
	}
	rows.Close()

	rows, err = s.db.SQL().QueryContext(ctx, `
        SELECT t.id, t.title, t.status, t.importance,
               (SELECT s.planned_date FROM task_schedules s
                 WHERE s.task_id = t.id AND s.status = 'active' LIMIT 1),
               t.hard_due_at, t.milestone_id
          FROM v_case_root_membership mb
          JOIN tasks t ON t.id = mb.member_id
         WHERE mb.root_type = ? AND mb.root_id = ? AND mb.member_type = 'task'
           AND t.status NOT IN ('done','cancelled','archived')
         ORDER BY CASE t.importance WHEN 'P0' THEN 0 WHEN 'P1' THEN 1
                                    WHEN 'P2' THEN 2 ELSE 3 END,
                  COALESCE(t.hard_due_at, '9999'), t.created_at`, rootType, rootID)
	if err != nil {
		return nil, sqlite.Classify(err)
	}
	defer rows.Close()
	for rows.Next() {
		var t CaseTask
		if err := rows.Scan(&t.TaskID, &t.Title, &t.Status, &t.Importance,
			&t.PlannedDate, &t.HardDueAt, &t.MilestoneID); err != nil {
			return nil, sqlite.Classify(err)
		}
		out.Tasks = append(out.Tasks, t)
	}
	return out, sqlite.Classify(rows.Err())
}

// CaseEvidenceRow is one confirmed field with the byte range it came from.
type CaseEvidenceRow struct {
	EntityType    string  `json:"entity_type"`
	EntityID      string  `json:"entity_id"`
	FieldName     string  `json:"field_name"`
	DocumentID    *string `json:"document_id,omitempty"`
	DocumentTitle *string `json:"document_title,omitempty"`
	Locator       *string `json:"source_locator,omitempty"`
	OriginType    string  `json:"origin_type"`
	CreatedAt     string  `json:"created_at"`
	IsCurrent     bool    `json:"is_current"`
}

// CaseWarning is one quality issue anywhere inside the case.
type CaseWarning struct {
	EntityType string `json:"entity_type"`
	EntityID   string `json:"entity_id"`
	Title      string `json:"title"`
	Issue      string `json:"issue"`
	Detail     string `json:"detail"`
}

// CaseDetail is what `case.get` returns: enough for the whole workspace header,
// facts panel and warning banner in one round trip.
type CaseDetail struct {
	ProjectionVersion int               `json:"projection_version"`
	Index             CaseIndexRow      `json:"index"`
	Facts             map[string]string `json:"facts"`
	Evidence          []CaseEvidenceRow `json:"evidence"`
	Warnings          []CaseWarning     `json:"warnings"`
}

// GetCase loads one case.
//
// is_current is computed here rather than in the view because "the same value"
// means whatever the Field Registry's normaliser says it means; duplicating
// that rule in SQL would guarantee the two eventually disagree.
func (s *Store) GetCase(ctx context.Context, rootType, rootID string) (*CaseDetail, error) {
	if rootType == "" || rootID == "" {
		return nil, protocol.BadInput("root_type and root_id are required")
	}
	row := s.db.SQL().QueryRowContext(ctx,
		`SELECT `+caseIndexColumns+` FROM v_case_index WHERE root_type = ? AND root_id = ?`,
		rootType, rootID)
	index, err := scanCaseIndex(row)
	if err == sql.ErrNoRows {
		return nil, protocol.NotFound("%s %s is not a case root", rootType, rootID)
	}
	if err != nil {
		return nil, sqlite.Classify(err)
	}

	out := &CaseDetail{ProjectionVersion: ProjectionVersion, Index: index,
		Facts: map[string]string{}, Evidence: []CaseEvidenceRow{}, Warnings: []CaseWarning{}}

	if rootType == "opportunity" {
		if out.Facts, err = s.opportunityFacts(ctx, rootID); err != nil {
			return nil, err
		}
	}

	rows, err := s.db.SQL().QueryContext(ctx, `
        SELECT entity_type, entity_id, field_name, document_id, document_title,
               source_locator_json, origin_type, created_at, normalized_value_hash
          FROM v_case_evidence
         WHERE root_type = ? AND root_id = ?
         ORDER BY created_at DESC`, rootType, rootID)
	if err != nil {
		return nil, sqlite.Classify(err)
	}
	for rows.Next() {
		var e CaseEvidenceRow
		var hash string
		if err := rows.Scan(&e.EntityType, &e.EntityID, &e.FieldName, &e.DocumentID,
			&e.DocumentTitle, &e.Locator, &e.OriginType, &e.CreatedAt, &hash); err != nil {
			rows.Close()
			return nil, sqlite.Classify(err)
		}
		if e.EntityID == rootID {
			e.IsCurrent = hashString(out.Facts[e.FieldName]) == hash
		}
		out.Evidence = append(out.Evidence, e)
	}
	rows.Close()

	rows, err = s.db.SQL().QueryContext(ctx, `
        SELECT entity_type, entity_id, title, issue, detail
          FROM v_case_quality WHERE root_type = ? AND root_id = ?`, rootType, rootID)
	if err != nil {
		return nil, sqlite.Classify(err)
	}
	defer rows.Close()
	for rows.Next() {
		var w CaseWarning
		if err := rows.Scan(&w.EntityType, &w.EntityID, &w.Title, &w.Issue, &w.Detail); err != nil {
			return nil, sqlite.Classify(err)
		}
		out.Warnings = append(out.Warnings, w)
	}
	return out, sqlite.Classify(rows.Err())
}

// opportunityFacts reads the registry-writable fields in their normalised form,
// which is exactly what an attribution hash is taken over.
func (s *Store) opportunityFacts(ctx context.Context, id string) (map[string]string, error) {
	var name, stage string
	var owner, nextStep, source, expected sql.NullString
	var est, win sql.NullFloat64
	err := s.db.SQL().QueryRowContext(ctx, `
        SELECT name, stage, owner, next_step, source, expected_sign_date,
               est_amount, win_probability
          FROM opportunities WHERE id = ?`, id).
		Scan(&name, &stage, &owner, &nextStep, &source, &expected, &est, &win)
	if err != nil {
		return nil, sqlite.Classify(err)
	}
	facts := map[string]string{"name": name, "stage": stage}
	for key, v := range map[string]sql.NullString{
		"owner": owner, "next_step": nextStep, "source": source, "expected_sign_date": expected,
	} {
		if v.Valid {
			facts[key] = v.String
		}
	}
	if est.Valid {
		facts["est_amount"] = formatFloat(est.Float64) + " CNY"
	}
	if win.Valid {
		facts["win_probability"] = formatFloat(win.Float64)
	}
	return facts, nil
}

func formatFloat(f float64) string { return strconv.FormatFloat(f, 'g', -1, 64) }
