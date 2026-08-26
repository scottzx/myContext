// Package ops implements the business execution core: areas, initiatives,
// projects, tasks, schedules, capacity and the audit trail (B+ design §7).
package ops

import (
	"sort"
	"strings"
	"time"

	"github.com/scottzx/mycontext/internal/protocol"
	"github.com/scottzx/mycontext/internal/system"
)

// Importance is the single priority scale. The old critical/high/med/low
// vocabulary is not accepted; migration produces a mapping for the user to
// confirm instead of converting silently.
type Importance string

const (
	P0 Importance = "P0"
	P1 Importance = "P1"
	P2 Importance = "P2"
	P3 Importance = "P3"
)

// TaskStatus values. inbox means "captured, not yet committed to".
type TaskStatus string

const (
	TaskInbox     TaskStatus = "inbox"
	TaskTodo      TaskStatus = "todo"
	TaskDoing     TaskStatus = "doing"
	TaskWaiting   TaskStatus = "waiting"
	TaskPaused    TaskStatus = "paused"
	TaskDone      TaskStatus = "done"
	TaskCancelled TaskStatus = "cancelled"
	TaskArchived  TaskStatus = "archived"
)

type ProjectStatus string

const (
	ProjectPlanned   ProjectStatus = "planned"
	ProjectActive    ProjectStatus = "active"
	ProjectWaiting   ProjectStatus = "waiting"
	ProjectPaused    ProjectStatus = "paused"
	ProjectDone      ProjectStatus = "done"
	ProjectCancelled ProjectStatus = "cancelled"
	ProjectArchived  ProjectStatus = "archived"
)

var (
	validImportance      = set("P0", "P1", "P2", "P3")
	validTaskStatus      = set("inbox", "todo", "doing", "waiting", "paused", "done", "cancelled", "archived")
	validProjectStatus   = set("planned", "active", "waiting", "paused", "done", "cancelled", "archived")
	validStage           = set("discover", "plan", "build", "iterate", "deliver", "operate", "close")
	validOutcomeStatus   = set("active", "done", "dropped", "archived")
	validDependency      = set("blocks", "requires", "related", "supports")
	validProjectKind     = set("project", "sprint")
	validMilestoneStatus = set("pending", "at_risk", "hit", "missed", "cancelled")
	validTimeSlot        = set("morning", "afternoon", "evening")
	validActorType       = set("user", "agent", "ui", "migration", "system")
	validEntryPoint      = set("cli", "bridge", "http", "import")
)

// entityTables maps an entity type to the table that owns it, so an edge or a
// tag can be checked against a real row before it is written. It is the single
// source of truth for the entity vocabulary: validEntityType is derived from
// its keys, so a type can never be accepted by validation without a table to
// look it up in. Adding a row here is the only step needed to admit a new type.
var entityTables = map[string]string{
	"objective":  "objectives",
	"key_result": "key_results",
	"initiative": "initiatives",
	"project":    "projects",
	"milestone":  "milestones",
	"task":       "tasks",
}

var validEntityType = keySet(entityTables)

// EntityTypeList renders the vocabulary for error messages and flag help, so
// those cannot drift from what is actually accepted either.
func EntityTypeList() string {
	keys := make([]string, 0, len(entityTables))
	for k := range entityTables {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return strings.Join(keys, "|")
}

func keySet(m map[string]string) map[string]bool {
	out := make(map[string]bool, len(m))
	for k := range m {
		out[k] = true
	}
	return out
}

func set(values ...string) map[string]bool {
	m := make(map[string]bool, len(values))
	for _, v := range values {
		m[v] = true
	}
	return m
}

// Actor records who made a change and through which entry point (§14.3).
type Actor struct {
	Type       string `json:"type"`
	ID         string `json:"id,omitempty"`
	EntryPoint string `json:"entry_point"`
}

func (a Actor) validate() error {
	if !validActorType[a.Type] {
		return protocol.BadInput("actor type %q is not one of user/agent/ui/migration/system", a.Type)
	}
	if !validEntryPoint[a.EntryPoint] {
		return protocol.BadInput("entry point %q is not one of cli/bridge/http/import", a.EntryPoint)
	}
	return nil
}

// Task mirrors the tasks table. Nil pointers mean "not set", which is
// semantically different from an empty string.
type Task struct {
	ID                 string     `json:"id"`
	ProjectID          *string    `json:"project_id"`
	MilestoneID        *string    `json:"milestone_id"`
	ParentTaskID       *string    `json:"parent_task_id"`
	Title              string     `json:"title"`
	Detail             *string    `json:"detail"`
	CompletionCriteria *string    `json:"completion_criteria"`
	Status             TaskStatus `json:"status"`
	Importance         Importance `json:"importance"`
	HardDueAt          *string    `json:"hard_due_at"`
	EarliestStartAt    *string    `json:"earliest_start_at"`
	NextReviewAt       *string    `json:"next_review_at"`
	EstimateMinutes    *int       `json:"estimate_minutes"`
	MetricName         *string    `json:"metric_name"`
	MetricUnit         *string    `json:"metric_unit"`
	TargetValue        *float64   `json:"target_value"`
	CurrentValue       *float64   `json:"current_value"`
	WaitingFor         *string    `json:"waiting_for"`
	LegacyRef          *string    `json:"legacy_ref"`
	LegacyDueDate      *string    `json:"legacy_due_date"`
	Version            int64      `json:"version"`
	CreatedAt          string     `json:"created_at"`
	UpdatedAt          string     `json:"updated_at"`
	CompletedAt        *string    `json:"completed_at"`

	// Schedule is the single active plan, when there is one.
	Schedule *Schedule `json:"schedule,omitempty"`
}

// Schedule is one row of task_schedules: an intention to work on a day.
type Schedule struct {
	ID             string  `json:"id"`
	TaskID         string  `json:"task_id"`
	PlannedDate    string  `json:"planned_date"`
	TimeSlot       *string `json:"time_slot"`
	PlannedMinutes *int    `json:"planned_minutes"`
	Status         string  `json:"status"`
	SupersededBy   *string `json:"superseded_by"`
	CreatedBy      string  `json:"created_by"`
	Note           *string `json:"note"`
	CreatedAt      string  `json:"created_at"`
}

type Project struct {
	ID                 string        `json:"id"`
	InitiativeID       *string       `json:"initiative_id"`
	Name               string        `json:"name"`
	Description        *string       `json:"description"`
	Status             ProjectStatus `json:"status"`
	ParentProjectID    *string       `json:"parent_project_id"`
	Kind               string        `json:"kind"`
	Stage              *string       `json:"stage"`
	Importance         Importance    `json:"importance"`
	TargetDate         *string       `json:"target_date"`
	StartDate          *string       `json:"start_date"`
	EndDate            *string       `json:"end_date"`
	HardDueAt          *string       `json:"hard_due_at"`
	NextReviewAt       *string       `json:"next_review_at"`
	Outcome            *string       `json:"outcome"`
	CompletionCriteria *string       `json:"completion_criteria"`
	MetricName         *string       `json:"metric_name"`
	MetricUnit         *string       `json:"metric_unit"`
	TargetValue        *float64      `json:"target_value"`
	CurrentValue       *float64      `json:"current_value"`
	LegacyRef          *string       `json:"legacy_ref"`
	Version            int64         `json:"version"`
	CreatedAt          string        `json:"created_at"`
	UpdatedAt          string        `json:"updated_at"`
	CompletedAt        *string       `json:"completed_at"`
}

type Area struct {
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	Status    string  `json:"status"`
	SortOrder int     `json:"sort_order"`
	Note      *string `json:"note"`
	Version   int64   `json:"version"`
	CreatedAt string  `json:"created_at"`
	UpdatedAt string  `json:"updated_at"`
}

type Initiative struct {
	ID          string  `json:"id"`
	AreaID      string  `json:"area_id"`
	Name        string  `json:"name"`
	Status      string  `json:"status"`
	StartDate   *string `json:"start_date"`
	ReviewDate  *string `json:"review_date"`
	Description *string `json:"description"`
	SortOrder   int     `json:"sort_order"`
	Version     int64   `json:"version"`
	CreatedAt   string  `json:"created_at"`
	UpdatedAt   string  `json:"updated_at"`
}

// Objective is a direction with a horizon. It is never a task tree parent -
// projects hang off initiatives, and the outcome system links to them
// sideways through key results.
type Objective struct {
	ID          string  `json:"id"`
	AreaID      *string `json:"area_id"`
	Name        string  `json:"name"`
	Description *string `json:"description"`
	Horizon     *string `json:"horizon"`
	Status      string  `json:"status"`
	Version     int64   `json:"version"`
	CreatedAt   string  `json:"created_at"`
	UpdatedAt   string  `json:"updated_at"`
}

// KeyResult carries exactly one measurement definition, plus its weight
// inside the objective it belongs to.
type KeyResult struct {
	ID           string   `json:"id"`
	ObjectiveID  string   `json:"objective_id"`
	Name         string   `json:"name"`
	MetricName   string   `json:"metric_name"`
	MetricUnit   *string  `json:"metric_unit"`
	TargetValue  *float64 `json:"target_value"`
	CurrentValue *float64 `json:"current_value"`
	Weight       *float64 `json:"weight"`
	Horizon      *string  `json:"horizon"`
	Status       string   `json:"status"`
	Version      int64    `json:"version"`
	CreatedAt    string   `json:"created_at"`
	UpdatedAt    string   `json:"updated_at"`
}

// Milestone is the dated point a body of work is aiming at. It is not a task
// - it is not executable and consumes no capacity - and not a project - it
// has no lifecycle of its own. Tasks point at it; it owns none of them
// exclusively.
type Milestone struct {
	ID           string     `json:"id"`
	ProjectID    *string    `json:"project_id"`
	KeyResultID  *string    `json:"key_result_id"`
	Name         string     `json:"name"`
	Description  *string    `json:"description"`
	TargetDate   string     `json:"target_date"`
	Status       string     `json:"status"`
	Importance   Importance `json:"importance"`
	MetricName   *string    `json:"metric_name"`
	MetricUnit   *string    `json:"metric_unit"`
	TargetValue  *float64   `json:"target_value"`
	CurrentValue *float64   `json:"current_value"`
	Note         *string    `json:"note"`
	LegacyRef    *string    `json:"legacy_ref"`
	SortOrder    int        `json:"sort_order"`
	Version      int64      `json:"version"`
	CreatedAt    string     `json:"created_at"`
	UpdatedAt    string     `json:"updated_at"`
	ReachedAt    *string    `json:"reached_at"`
}

// MilestoneProgress is a milestone plus the state of the work aimed at it.
type MilestoneProgress struct {
	MilestoneID   string   `json:"milestone_id"`
	Name          string   `json:"name"`
	Status        string   `json:"status"`
	Importance    string   `json:"importance"`
	TargetDate    string   `json:"target_date"`
	ReachedAt     *string  `json:"reached_at"`
	MetricName    *string  `json:"metric_name"`
	MetricUnit    *string  `json:"metric_unit"`
	TargetValue   *float64 `json:"target_value"`
	CurrentValue  *float64 `json:"current_value"`
	ProjectID     *string  `json:"project_id"`
	ProjectName   *string  `json:"project_name"`
	AreaName      *string  `json:"area_name"`
	KeyResultID   *string  `json:"key_result_id"`
	KeyResultName *string  `json:"key_result_name"`
	DaysLeft      *int     `json:"days_left"`
	TaskCount     int      `json:"task_count"`
	DoneCount     int      `json:"done_count"`
	OpenTasks     int      `json:"open_tasks"`
	OpenMinutes   int      `json:"open_minutes"`
}

// Dependency is one edge of the graph. Both ends name their entity type, so
// an edge can cross levels - a KR can support another KR, a task can block a
// cycle.
type Dependency struct {
	ID             string  `json:"id"`
	FromType       string  `json:"from_type"`
	FromID         string  `json:"from_id"`
	ToType         string  `json:"to_type"`
	ToID           string  `json:"to_id"`
	DependencyType string  `json:"dependency_type"`
	LagDays        *int    `json:"lag_days"`
	Note           *string `json:"note"`
	CreatedAt      string  `json:"created_at"`
}

// Event is one audit record.
type Event struct {
	ID            string  `json:"id"`
	EntityType    string  `json:"entity_type"`
	EntityID      string  `json:"entity_id"`
	EventType     string  `json:"event_type"`
	BeforeJSON    *string `json:"before_json,omitempty"`
	AfterJSON     *string `json:"after_json,omitempty"`
	ActorType     string  `json:"actor_type"`
	ActorID       *string `json:"actor_id,omitempty"`
	EntryPoint    string  `json:"entry_point"`
	Reason        *string `json:"reason,omitempty"`
	Confirmed     bool    `json:"confirmed"`
	RequestID     *string `json:"request_id,omitempty"`
	CorrelationID *string `json:"correlation_id,omitempty"`
	OccurredAt    string  `json:"occurred_at"`
}

// ---------------------------------------------------------------------------
// Value validation. These run before any SQL so the caller gets BAD_INPUT
// rather than a constraint error leaking from the driver.
// ---------------------------------------------------------------------------

// ValidateDate accepts a YYYY-MM-DD calendar day.
func ValidateDate(field, value string) error {
	if _, err := time.Parse(system.DateLayout, value); err != nil {
		return protocol.BadInput("%s must be a YYYY-MM-DD date, got %q", field, value)
	}
	return nil
}

// ValidateTimestamp accepts an RFC 3339 timestamp that carries a timezone.
func ValidateTimestamp(field, value string) error {
	if _, err := time.Parse(time.RFC3339, value); err != nil {
		return protocol.BadInput("%s must be an RFC 3339 timestamp with a timezone, got %q", field, value)
	}
	return nil
}

func validateImportance(value string) error {
	if !validImportance[value] {
		return protocol.BadInput("importance must be one of P0/P1/P2/P3, got %q", value)
	}
	return nil
}

func validateTaskStatus(value string) error {
	if !validTaskStatus[value] {
		return protocol.BadInput("task status %q is not valid", value)
	}
	return nil
}

func validateProjectStatus(value string) error {
	if !validProjectStatus[value] {
		return protocol.BadInput("project status %q is not valid", value)
	}
	return nil
}
