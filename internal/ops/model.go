// Package ops implements the business execution core: areas, initiatives,
// projects, tasks, schedules, capacity and the audit trail (B+ design §7).
package ops

import (
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
	validImportance    = set("P0", "P1", "P2", "P3")
	validTaskStatus    = set("inbox", "todo", "doing", "waiting", "done", "cancelled", "archived")
	validProjectStatus = set("planned", "active", "waiting", "paused", "done", "cancelled", "archived")
	validStage         = set("discover", "plan", "build", "deliver", "operate", "close")
	validTimeSlot      = set("morning", "afternoon", "evening")
	validActorType     = set("user", "agent", "ui", "migration", "system")
	validEntryPoint    = set("cli", "bridge", "http", "import")
)

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
	Stage              *string       `json:"stage"`
	Importance         Importance    `json:"importance"`
	TargetDate         *string       `json:"target_date"`
	HardDueAt          *string       `json:"hard_due_at"`
	NextReviewAt       *string       `json:"next_review_at"`
	Outcome            *string       `json:"outcome"`
	CompletionCriteria *string       `json:"completion_criteria"`
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
