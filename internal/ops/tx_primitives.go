package ops

import (
	"context"
	"database/sql"
	"time"

	"github.com/scottzx/mycontext/internal/protocol"
	"github.com/scottzx/mycontext/internal/system"
)

// Tx primitives: the composable half of every domain write.
//
// Until now each public command opened its own execute(...) transaction and
// held its business rules inside that closure. ConfirmInbox has to create an
// account, a contact, an opportunity, an interaction, a project, milestones and
// tasks in ONE transaction (design §5), and a transaction cannot be nested - so
// the rules had to come out of the closures and into functions that take a *sql.Tx.
//
// The public commands are now thin wrappers around these, which is the point:
// there is exactly one implementation of "what it means to create an
// opportunity", and confirm cannot drift from `opportunity create` because they
// run the same code.
//
// Every primitive takes an explicit id. Confirm pre-allocates all ids before
// executing so it can resolve candidate references into real foreign keys
// before the first INSERT; an empty id means "allocate one", which is what the
// public commands pass.

func orNewID(id, prefix string) string {
	if id != "" {
		return id
	}
	return system.NewID(prefix)
}

// ---------------------------------------------------------------------------
// accounts
// ---------------------------------------------------------------------------

func createAccountTx(ctx context.Context, tx *sql.Tx, wc WriteContext, now time.Time,
	id string, in CreateAccountInput) (*Result, error) {

	id = orNewID(id, "acct")
	ts := system.FormatTimestamp(now)
	if _, err := tx.ExecContext(ctx, `
        INSERT INTO accounts (id, name, short_name, account_type, industry, region,
                              status, owner, note, legacy_ref, version, created_at, updated_at)
        VALUES (?,?,?,?,?,?,?,?,?,?,1,?,?)`,
		id, in.Name, nullString(in.ShortName), in.AccountType, nullString(in.Industry),
		nullString(in.Region), in.Status, nullString(in.Owner), nullString(in.Note),
		nullString(in.LegacyRef), ts, ts); err != nil {
		return nil, err
	}
	if err := recordEvent(ctx, tx, wc, now, "account", id, "created", nil, in); err != nil {
		return nil, err
	}
	acct, err := loadAccount(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	return &Result{
		Data: acct,
		Changes: []protocol.Change{{EntityType: "account", EntityID: id, EventType: "created",
			Version: 1, ProjectionKeys: []string{"accounts"}}},
	}, nil
}

func updateAccountTx(ctx context.Context, tx *sql.Tx, wc WriteContext, now time.Time,
	in UpdateAccountInput) (*Result, error) {

	before, err := loadAccount(ctx, tx, in.AccountID)
	if err != nil {
		return nil, err
	}
	if before.Version != in.ExpectedVersion {
		return nil, protocol.VersionConflict("account", in.ExpectedVersion, before.Version)
	}
	set := newPatch()
	set.str("name", in.Name)
	set.str("short_name", in.ShortName)
	set.str("account_type", in.AccountType)
	set.str("industry", in.Industry)
	set.str("region", in.Region)
	set.str("status", in.Status)
	set.str("owner", in.Owner)
	set.str("note", in.Note)
	if set.empty() {
		return nil, protocol.BadInput("no fields to update")
	}
	if err := set.apply(ctx, tx, "accounts", "account", in.AccountID, in.ExpectedVersion, now); err != nil {
		return nil, err
	}
	eventType := "updated"
	if in.Status != nil {
		eventType = "status_changed"
	}
	after, err := loadAccount(ctx, tx, in.AccountID)
	if err != nil {
		return nil, err
	}
	if err := recordEvent(ctx, tx, wc, now, "account", in.AccountID, eventType, before, after); err != nil {
		return nil, err
	}
	return &Result{
		Data: after,
		Changes: []protocol.Change{{EntityType: "account", EntityID: in.AccountID,
			EventType: eventType, Version: after.Version, ProjectionKeys: []string{"accounts"}}},
	}, nil
}

// ---------------------------------------------------------------------------
// contacts
// ---------------------------------------------------------------------------

func createContactTx(ctx context.Context, tx *sql.Tx, wc WriteContext, now time.Time,
	id string, in CreateContactInput) (*Result, error) {

	if err := requireExists(ctx, tx, "accounts", in.AccountID, "account"); err != nil {
		return nil, err
	}
	id = orNewID(id, "ctc")
	ts := system.FormatTimestamp(now)
	if _, err := tx.ExecContext(ctx, `
        INSERT INTO contacts (id, account_id, name, title, deal_role, phone, email,
                              wechat, status, note, legacy_ref, version, created_at, updated_at)
        VALUES (?,?,?,?,?,?,?,?,?,?,?,1,?,?)`,
		id, in.AccountID, in.Name, nullString(in.Title), nullString(in.DealRole),
		nullString(in.Phone), nullString(in.Email), nullString(in.Wechat), in.Status,
		nullString(in.Note), nullString(in.LegacyRef), ts, ts); err != nil {
		return nil, err
	}
	if err := recordEvent(ctx, tx, wc, now, "contact", id, "created", nil, in); err != nil {
		return nil, err
	}
	c, err := loadContact(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	return &Result{
		Data: c,
		Changes: []protocol.Change{{EntityType: "contact", EntityID: id, EventType: "created",
			Version: 1, ProjectionKeys: []string{"contacts", "account:" + in.AccountID}}},
	}, nil
}

func updateContactTx(ctx context.Context, tx *sql.Tx, wc WriteContext, now time.Time,
	in UpdateContactInput) (*Result, error) {

	before, err := loadContact(ctx, tx, in.ContactID)
	if err != nil {
		return nil, err
	}
	if before.Version != in.ExpectedVersion {
		return nil, protocol.VersionConflict("contact", in.ExpectedVersion, before.Version)
	}
	if in.AccountID != nil && *in.AccountID != "" {
		if err := requireExists(ctx, tx, "accounts", *in.AccountID, "account"); err != nil {
			return nil, err
		}
	}
	set := newPatch()
	set.str("name", in.Name)
	set.str("title", in.Title)
	set.str("deal_role", in.DealRole)
	set.str("phone", in.Phone)
	set.str("email", in.Email)
	set.str("wechat", in.Wechat)
	set.str("status", in.Status)
	set.str("note", in.Note)
	set.str("account_id", in.AccountID)
	if set.empty() {
		return nil, protocol.BadInput("no fields to update")
	}
	if err := set.apply(ctx, tx, "contacts", "contact", in.ContactID, in.ExpectedVersion, now); err != nil {
		return nil, err
	}
	eventType := "updated"
	if in.Status != nil {
		eventType = "status_changed"
	}
	after, err := loadContact(ctx, tx, in.ContactID)
	if err != nil {
		return nil, err
	}
	if err := recordEvent(ctx, tx, wc, now, "contact", in.ContactID, eventType, before, after); err != nil {
		return nil, err
	}
	return &Result{
		Data: after,
		Changes: []protocol.Change{{EntityType: "contact", EntityID: in.ContactID,
			EventType: eventType, Version: after.Version, ProjectionKeys: []string{"contacts"}}},
	}, nil
}

// ---------------------------------------------------------------------------
// opportunities
// ---------------------------------------------------------------------------

func createOpportunityTx(ctx context.Context, tx *sql.Tx, wc WriteContext, now time.Time,
	id string, in CreateOpportunityInput) (*Result, error) {

	if err := requireExists(ctx, tx, "accounts", in.AccountID, "account"); err != nil {
		return nil, err
	}
	if in.AreaID != "" {
		if err := requireExists(ctx, tx, "areas", in.AreaID, "area"); err != nil {
			return nil, err
		}
	}
	if in.PrimaryContactID != "" {
		if err := requireExists(ctx, tx, "contacts", in.PrimaryContactID, "contact"); err != nil {
			return nil, err
		}
	}
	id = orNewID(id, "opp")
	ts := system.FormatTimestamp(now)
	var closedAt any
	if in.Stage == "won" || in.Stage == "lost" {
		closedAt = ts
	}
	if _, err := tx.ExecContext(ctx, `
        INSERT INTO opportunities (id, account_id, area_id, primary_contact_id, name,
                                   source, source_batch, stage, est_amount, win_probability,
                                   expected_sign_date, owner, next_step, lost_reason,
                                   closed_at, legacy_ref, version, created_at, updated_at)
        VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,1,?,?)`,
		id, in.AccountID, nullString(in.AreaID), nullString(in.PrimaryContactID), in.Name,
		nullString(in.Source), nullString(in.SourceBatch), in.Stage, nullFloat(in.EstAmount),
		nullFloat(in.WinProbability), nullString(in.ExpectedSignDate), nullString(in.Owner),
		nullString(in.NextStep), nullString(in.LostReason), closedAt, nullString(in.LegacyRef),
		ts, ts); err != nil {
		return nil, err
	}
	if err := recordEvent(ctx, tx, wc, now, "opportunity", id, "created", nil, in); err != nil {
		return nil, err
	}
	o, err := loadOpportunity(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	return &Result{
		Data: o,
		Changes: []protocol.Change{{EntityType: "opportunity", EntityID: id, EventType: "created",
			Version: 1, ProjectionKeys: []string{"opportunities", "account:" + in.AccountID}}},
	}, nil
}

func updateOpportunityTx(ctx context.Context, tx *sql.Tx, wc WriteContext, now time.Time,
	in UpdateOpportunityInput) (*Result, error) {

	before, err := loadOpportunity(ctx, tx, in.OpportunityID)
	if err != nil {
		return nil, err
	}
	if before.Version != in.ExpectedVersion {
		return nil, protocol.VersionConflict("opportunity", in.ExpectedVersion, before.Version)
	}
	if in.PrimaryContactID != nil && *in.PrimaryContactID != "" {
		if err := requireExists(ctx, tx, "contacts", *in.PrimaryContactID, "contact"); err != nil {
			return nil, err
		}
	}

	eventType := "updated"
	set := newPatch()
	set.str("name", in.Name)
	set.str("source", in.Source)
	set.str("source_batch", in.SourceBatch)
	set.str("primary_contact_id", in.PrimaryContactID)
	set.str("expected_sign_date", in.ExpectedSignDate)
	set.str("owner", in.Owner)
	set.str("next_step", in.NextStep)
	set.str("note", in.Note)
	set.flt("est_amount", in.EstAmount)
	set.flt("win_probability", in.WinProbability)

	if in.Stage != nil && *in.Stage != before.Stage {
		// A hard rule: leaving a terminal stage is how a closed deal
		// quietly reopens. Reaching a terminal stage needs no reason -
		// closing a deal is the normal, expected path.
		terminal := before.Stage == "won" || before.Stage == "lost"
		if terminal && wc.Reason == "" {
			return nil, &protocol.AppError{
				Code:    protocol.CodeForbidden,
				Message: "moving an opportunity out of a won/lost stage requires --reason",
			}
		}
		set.raw("stage", *in.Stage)
		switch *in.Stage {
		case "won":
			eventType = "won"
			if before.ClosedAt == nil {
				set.raw("closed_at", system.FormatTimestamp(now))
			}
		case "lost":
			eventType = "lost"
			reason := before.LostReason
			if in.LostReason != nil {
				reason = in.LostReason
			}
			if reason == nil || *reason == "" {
				return nil, protocol.BadInput("stage lost requires lost_reason")
			}
			if before.ClosedAt == nil {
				set.raw("closed_at", system.FormatTimestamp(now))
			}
		default:
			eventType = "stage_changed"
			if before.ClosedAt != nil {
				set.raw("closed_at", nil)
			}
		}
	}
	if in.LostReason != nil {
		set.str("lost_reason", in.LostReason)
	}
	if set.empty() {
		return nil, protocol.BadInput("no fields to update")
	}
	if err := set.apply(ctx, tx, "opportunities", "opportunity", in.OpportunityID, in.ExpectedVersion, now); err != nil {
		return nil, err
	}
	after, err := loadOpportunity(ctx, tx, in.OpportunityID)
	if err != nil {
		return nil, err
	}
	if err := recordEvent(ctx, tx, wc, now, "opportunity", in.OpportunityID, eventType, before, after); err != nil {
		return nil, err
	}
	return &Result{
		Data: after,
		Changes: []protocol.Change{{EntityType: "opportunity", EntityID: in.OpportunityID,
			EventType: eventType, Version: after.Version, ProjectionKeys: []string{"opportunities"}}},
	}, nil
}

// ---------------------------------------------------------------------------
// interactions
// ---------------------------------------------------------------------------

func logInteractionTx(ctx context.Context, tx *sql.Tx, wc WriteContext, now time.Time,
	id string, in LogInteractionInput) (*Result, error) {

	if err := requireExists(ctx, tx, entityTables[in.SubjectType], in.SubjectID, in.SubjectType); err != nil {
		return nil, err
	}
	id = orNewID(id, "itx")
	ts := system.FormatTimestamp(now)
	if _, err := tx.ExecContext(ctx, `
        INSERT INTO interactions (id, subject_type, subject_id, occurred_at, channel,
                                  summary, participants, owner, created_at)
        VALUES (?,?,?,?,?,?,?,?,?)`,
		id, in.SubjectType, in.SubjectID, in.OccurredAt, in.Channel,
		nullString(in.Summary), nullString(in.Participants), nullString(in.Owner), ts); err != nil {
		return nil, err
	}
	if err := recordEvent(ctx, tx, wc, now, in.SubjectType, in.SubjectID, "note", nil, in); err != nil {
		return nil, err
	}
	itx := Interaction{ID: id, SubjectType: in.SubjectType, SubjectID: in.SubjectID,
		OccurredAt: in.OccurredAt, Channel: in.Channel, CreatedAt: ts}
	assignOptional(&itx.Summary, in.Summary)
	assignOptional(&itx.Participants, in.Participants)
	assignOptional(&itx.Owner, in.Owner)
	return &Result{
		Data: itx,
		Changes: []protocol.Change{{EntityType: in.SubjectType, EntityID: in.SubjectID,
			EventType: "note", ProjectionKeys: []string{"interactions"}}},
	}, nil
}

// linkInteractionDocumentTx records that a document IS the transcript, the
// minutes or the evidence of one conversation. Repeating the same link is a
// no-op rather than an error: confirm may re-derive it from a second run over
// the same evidence, and a duplicate link carries no new information.
func linkInteractionDocumentTx(ctx context.Context, tx *sql.Tx, now time.Time,
	interactionID, documentID, role string) error {

	if role == "" {
		role = "evidence"
	}
	if !validInteractionDocRole[role] {
		return protocol.BadInput("interaction document role must be transcript|minutes|attachment|evidence")
	}
	if err := requireExists(ctx, tx, "documents", documentID, "document"); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `
        INSERT INTO interaction_documents (interaction_id, document_id, role, created_at)
        VALUES (?,?,?,?)
        ON CONFLICT(interaction_id, document_id, role) DO NOTHING`,
		interactionID, documentID, role, system.FormatTimestamp(now))
	return err
}

var validInteractionDocRole = map[string]bool{
	"transcript": true, "minutes": true, "attachment": true, "evidence": true,
}

// ---------------------------------------------------------------------------
// execution: project, milestone, task
// ---------------------------------------------------------------------------

func createProjectTx(ctx context.Context, tx *sql.Tx, wc WriteContext, now time.Time,
	id string, in CreateProjectInput) (*Result, error) {

	if in.InitiativeID != "" {
		if err := requireExists(ctx, tx, "initiatives", in.InitiativeID, "initiative"); err != nil {
			return nil, err
		}
	}
	if in.ParentProjectID != "" {
		if err := requireExists(ctx, tx, "projects", in.ParentProjectID, "parent project"); err != nil {
			return nil, err
		}
	}
	if id == "" {
		id = system.NewID("proj")
		if in.Kind == "sprint" {
			id = system.NewID("sprint")
		}
	}
	ts := system.FormatTimestamp(now)
	_, err := tx.ExecContext(ctx, `
        INSERT INTO projects (id, initiative_id, parent_project_id, kind, name, description,
                              status, stage, importance, start_date, end_date,
                              target_date, hard_due_at, next_review_at, outcome,
                              completion_criteria, metric_name, metric_unit,
                              target_value, current_value, legacy_ref, sort_order,
                              version, created_at, updated_at)
        VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,1,?,?)`,
		id, nullString(in.InitiativeID), nullString(in.ParentProjectID), in.Kind,
		in.Name, nullString(in.Description), in.Status, nullString(in.Stage), in.Importance,
		nullString(in.StartDate), nullString(in.EndDate),
		nullString(in.TargetDate), nullString(in.HardDueAt),
		nullString(in.NextReviewAt), nullString(in.Outcome), nullString(in.CompletionCriteria),
		nullString(in.MetricName), nullString(in.MetricUnit),
		nullFloat(in.TargetValue), nullFloat(in.CurrentValue),
		nullString(in.LegacyRef), in.SortOrder, ts, ts)
	if err != nil {
		return nil, err
	}
	if err := recordEvent(ctx, tx, wc, now, "project", id, "created", nil, in); err != nil {
		return nil, err
	}
	project, err := loadProjectTx(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	return &Result{
		Data: project,
		Changes: []protocol.Change{{
			EntityType: "project", EntityID: id, EventType: "created", Version: 1,
			ProjectionKeys: []string{"projects", "project:" + id},
		}},
	}, nil
}

func createMilestoneTx(ctx context.Context, tx *sql.Tx, wc WriteContext, now time.Time,
	id string, in CreateMilestoneInput) (*Result, error) {

	if in.ProjectID != "" {
		if err := requireExists(ctx, tx, "projects", in.ProjectID, "project"); err != nil {
			return nil, err
		}
	}
	if in.KeyResultID != "" {
		if err := requireExists(ctx, tx, "key_results", in.KeyResultID, "key result"); err != nil {
			return nil, err
		}
	}
	id = orNewID(id, "ms")
	ts := system.FormatTimestamp(now)
	var reachedAt any
	if in.Status == "hit" {
		reachedAt = ts
	}
	if _, err := tx.ExecContext(ctx, `
        INSERT INTO milestones (id, project_id, key_result_id, name, description,
                                target_date, status, importance, metric_name, metric_unit,
                                target_value, current_value, note, legacy_ref,
                                sort_order, version, created_at, updated_at, reached_at)
        VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,1,?,?,?)`,
		id, nullString(in.ProjectID), nullString(in.KeyResultID), in.Name,
		nullString(in.Description), in.TargetDate, in.Status, in.Importance,
		nullString(in.MetricName), nullString(in.MetricUnit),
		nullFloat(in.TargetValue), nullFloat(in.CurrentValue),
		nullString(in.Note), nullString(in.LegacyRef), in.SortOrder,
		ts, ts, reachedAt); err != nil {
		return nil, err
	}
	if err := recordEvent(ctx, tx, wc, now, "milestone", id, "created", nil, in); err != nil {
		return nil, err
	}
	m, err := loadMilestone(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	return &Result{
		Data: m,
		Changes: []protocol.Change{{EntityType: "milestone", EntityID: id, EventType: "created",
			Version: 1, ProjectionKeys: []string{"milestones", "day:" + in.TargetDate}}},
	}, nil
}

func createTaskTx(ctx context.Context, tx *sql.Tx, wc WriteContext, now time.Time,
	id string, in CreateTaskInput) (*Result, error) {

	if in.ProjectID != "" {
		if err := requireExists(ctx, tx, "projects", in.ProjectID, "project"); err != nil {
			return nil, err
		}
	}
	if in.ParentTaskID != "" {
		if err := requireExists(ctx, tx, "tasks", in.ParentTaskID, "parent task"); err != nil {
			return nil, err
		}
	}
	if in.MilestoneID != "" {
		if err := requireExists(ctx, tx, "milestones", in.MilestoneID, "milestone"); err != nil {
			return nil, err
		}
	}
	if in.SubjectType != "" {
		if err := requireExists(ctx, tx, entityTables[in.SubjectType], in.SubjectID, in.SubjectType); err != nil {
			return nil, err
		}
	}

	id = orNewID(id, "task")
	ts := system.FormatTimestamp(now)
	_, err := tx.ExecContext(ctx, `
        INSERT INTO tasks (id, project_id, parent_task_id, milestone_id, title, detail,
                           completion_criteria, status, importance, hard_due_at,
                           earliest_start_at, next_review_at, estimate_minutes,
                           metric_name, metric_unit, target_value, current_value,
                           waiting_for, subject_type, subject_id, legacy_ref,
                           version, created_at, updated_at)
        VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,1,?,?)`,
		id, nullString(in.ProjectID), nullString(in.ParentTaskID),
		nullString(in.MilestoneID), in.Title,
		nullString(in.Detail), nullString(in.CompletionCriteria), in.Status, in.Importance,
		nullString(in.HardDueAt), nullString(in.EarliestStartAt), nullString(in.NextReviewAt),
		nullInt(in.EstimateMinutes), nullString(in.MetricName), nullString(in.MetricUnit),
		nullFloat(in.TargetValue), nullFloat(in.CurrentValue),
		nullString(in.WaitingFor), nullString(in.SubjectType), nullString(in.SubjectID),
		nullString(in.LegacyRef), ts, ts)
	if err != nil {
		return nil, err
	}

	if in.PlannedDate != "" {
		if err := insertSchedule(ctx, tx, wc, now, system.NewID("sch"), id,
			in.PlannedDate, in.TimeSlot, in.PlannedMinutes, ""); err != nil {
			return nil, err
		}
	}
	if err := recordEvent(ctx, tx, wc, now, "task", id, "created", nil, in); err != nil {
		return nil, err
	}

	task, err := loadTaskTx(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	return &Result{
		Data: task,
		Changes: []protocol.Change{{
			EntityType: "task", EntityID: id, EventType: "created", Version: 1,
			ProjectionKeys: projectionKeysForTask(task),
		}},
	}, nil
}

// ---------------------------------------------------------------------------
// relation storage
// ---------------------------------------------------------------------------

// linkOpportunityProjectTx is the sole authority for "this project advances
// this deal" (design §3). Both primary uniqueness rules are enforced by partial
// indexes; this reports the collision as a normal review error instead of
// letting a constraint violation surface as an internal failure.
func linkOpportunityProjectTx(ctx context.Context, tx *sql.Tx, now time.Time,
	opportunityID, projectID, role string) error {

	if role == "" {
		role = "primary"
	}
	if role != "primary" && role != "support" {
		return protocol.BadInput("opportunity project role must be primary or support")
	}
	if err := requireExists(ctx, tx, "opportunities", opportunityID, "opportunity"); err != nil {
		return err
	}
	if err := requireExists(ctx, tx, "projects", projectID, "project"); err != nil {
		return err
	}
	if role == "primary" {
		var other string
		err := tx.QueryRowContext(ctx, `
            SELECT project_id FROM opportunity_projects
             WHERE opportunity_id = ? AND role = 'primary' AND project_id <> ?`,
			opportunityID, projectID).Scan(&other)
		if err == nil {
			return relationCardinalityConflict("opportunity already has a primary project",
				map[string]any{"opportunity_id": opportunityID, "existing_project_id": other})
		} else if err != sql.ErrNoRows {
			return err
		}
		err = tx.QueryRowContext(ctx, `
            SELECT opportunity_id FROM opportunity_projects
             WHERE project_id = ? AND role = 'primary' AND opportunity_id <> ?`,
			projectID, opportunityID).Scan(&other)
		if err == nil {
			return relationCardinalityConflict("project is already the primary project of another opportunity",
				map[string]any{"project_id": projectID, "existing_opportunity_id": other})
		} else if err != sql.ErrNoRows {
			return err
		}
	}
	_, err := tx.ExecContext(ctx, `
        INSERT INTO opportunity_projects (opportunity_id, project_id, role, created_at)
        VALUES (?,?,?,?)
        ON CONFLICT(opportunity_id, project_id) DO UPDATE SET role = excluded.role`,
		opportunityID, projectID, role, system.FormatTimestamp(now))
	return err
}

// linkDocumentToEntityTx attaches evidence to a business object. Duplicate
// links are ignored: the same article confirmed twice is one piece of evidence.
func linkDocumentToEntityTx(ctx context.Context, tx *sql.Tx, now time.Time,
	documentID, entityType, entityID, linkType string) error {

	if linkType == "" {
		linkType = "evidence"
	}
	if err := requireExists(ctx, tx, "documents", documentID, "document"); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `
        INSERT INTO doc_links (id, doc_id, entity_type, entity_id, link_type, created_at)
        VALUES (?,?,?,?,?,?)
        ON CONFLICT(doc_id, entity_type, entity_id, link_type) DO NOTHING`,
		system.NewID("dl"), documentID, entityType, entityID, linkType,
		system.FormatTimestamp(now))
	return err
}

func relationCardinalityConflict(message string, details any) error {
	return protocol.Review(protocol.CodeRelationCardinal, message, details)
}
