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

	// Business core vocabularies (005_business_core.sql). Each set is written
	// in the same order as the SQL CHECK it mirrors, so the two can be
	// eye-diffed.
	validAccountType          = set("customer", "prospect", "partner", "vendor", "organizer", "media", "community", "individual")
	validAccountStatus        = set("active", "dormant", "archived")
	validDealRole             = set("decider", "influencer", "user", "gatekeeper")
	validContactStatus        = set("active", "inactive", "left", "archived")
	validOpportunityStage     = set("lead", "qualified", "proposal", "negotiation", "won", "lost")
	validApplicationKind      = set("competition", "program", "job", "listing", "partnership")
	validApplicationStage     = set("discovered", "preparing", "submitted", "under_review", "shortlisted", "won", "rejected", "withdrawn")
	validContractKind         = set("sales", "prize", "sponsorship", "grant", "piecework", "other")
	validContractStatus       = set("draft", "signed", "active", "completed", "terminated")
	validReceivablePlanStatus = set("planned", "invoiced", "received", "waived")
	validTicketKind           = set("question", "incident", "change_request", "training", "other")
	validTicketSeverity       = set("S1", "S2", "S3", "S4")
	validTicketStatus         = set("open", "in_progress", "waiting", "resolved", "closed")
	validProductKind          = set("product", "service", "solution")
	validProductStatus        = set("concept", "developing", "released", "maintained", "sunset")
	validDocumentKind         = set("dossier", "meeting_note", "contract_doc", "proposal", "content_draft", "release_note", "decision", "report", "other")
	validDocLinkType          = set("dossier", "minutes", "evidence", "attachment", "deliverable")
	validDocumentFileRole     = set("original", "rendition", "attachment")
	validContextEdgeType      = set("referred_by", "derived_from", "references", "relates_to", "inspired_by")
	validInteractionChannel   = set("meeting", "call", "im", "email", "visit")

	// Content/product vocabularies (006_content_product.sql). Each set is
	// written in the same order as the SQL CHECK it mirrors.
	validChannelPlatform     = set("xiaohongshu", "wechat", "douyin", "bilibili", "x", "offline")
	validChannelStatus       = set("active", "paused", "archived")
	validReleaseStatus       = set("planned", "developing", "released", "rolled_back")
	validCampaignChannelType = set("online", "offline")
	validCampaignStatus      = set("planned", "running", "ended", "cancelled")
	validContentPieceStatus  = set("idea", "drafting", "review", "scheduled", "published", "archived")
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

	// 005_business_core.sql
	"account":     "accounts",
	"contact":     "contacts",
	"opportunity": "opportunities",
	"application": "applications",
	"contract":    "contracts",
	"ticket":      "service_tickets",
	"document":    "documents",
	"product":     "products",

	// 006_content_product.sql
	"channel":       "channels",
	"content_piece": "content_pieces",
	"release":       "releases",
	"campaign":      "campaigns",
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
// Business core (005_business_core.sql). Pointer fields mean "not set" the
// same way Task and Project already use them; a zero value written on
// purpose (e.g. current_value = 0) is never confused with "no value".
// ---------------------------------------------------------------------------

// Account is any external party we deal with - customer, prospect, partner,
// vendor, competition organiser, media outlet, community or individual buyer.
type Account struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	ShortName   *string `json:"short_name"`
	AccountType string  `json:"account_type"`
	Industry    *string `json:"industry"`
	Region      *string `json:"region"`
	Status      string  `json:"status"`
	Owner       *string `json:"owner"`
	Note        *string `json:"note"`
	LegacyRef   *string `json:"legacy_ref"`
	Version     int64   `json:"version"`
	CreatedAt   string  `json:"created_at"`
	UpdatedAt   string  `json:"updated_at"`
}

// Contact is a person at an account. It never needs an opportunity - most
// people worth remembering never appear in a deal.
type Contact struct {
	ID        string  `json:"id"`
	AccountID string  `json:"account_id"`
	Name      string  `json:"name"`
	Title     *string `json:"title"`
	DealRole  *string `json:"deal_role"`
	Phone     *string `json:"phone"`
	Email     *string `json:"email"`
	Wechat    *string `json:"wechat"`
	Status    string  `json:"status"`
	Note      *string `json:"note"`
	LegacyRef *string `json:"legacy_ref"`
	Version   int64   `json:"version"`
	CreatedAt string  `json:"created_at"`
	UpdatedAt string  `json:"updated_at"`
}

// Opportunity is a possible deal.
type Opportunity struct {
	ID               string   `json:"id"`
	AccountID        string   `json:"account_id"`
	AreaID           *string  `json:"area_id"`
	PrimaryContactID *string  `json:"primary_contact_id"`
	Name             string   `json:"name"`
	Source           *string  `json:"source"`
	SourceBatch      *string  `json:"source_batch"`
	Stage            string   `json:"stage"`
	EstAmount        *float64 `json:"est_amount"`
	WinProbability   *float64 `json:"win_probability"`
	ExpectedSignDate *string  `json:"expected_sign_date"`
	Owner            *string  `json:"owner"`
	NextStep         *string  `json:"next_step"`
	LostReason       *string  `json:"lost_reason"`
	ClosedAt         *string  `json:"closed_at"`
	Note             *string  `json:"note"`
	LegacyRef        *string  `json:"legacy_ref"`
	Version          int64    `json:"version"`
	CreatedAt        string   `json:"created_at"`
	UpdatedAt        string   `json:"updated_at"`
}

// Application is something we apply to and someone else decides: a
// competition, an ecosystem programme, a job, a directory listing.
type Application struct {
	ID           string   `json:"id"`
	AreaID       *string  `json:"area_id"`
	AccountID    *string  `json:"account_id"`
	ProjectID    *string  `json:"project_id"`
	Name         string   `json:"name"`
	Kind         string   `json:"kind"`
	Stage        string   `json:"stage"`
	SubmittedAt  *string  `json:"submitted_at"`
	DecidedAt    *string  `json:"decided_at"`
	PrizeAmount  *float64 `json:"prize_amount"`
	OutcomeNote  *string  `json:"outcome_note"`
	RejectReason *string  `json:"reject_reason"`
	Owner        *string  `json:"owner"`
	NextStep     *string  `json:"next_step"`
	LegacyRef    *string  `json:"legacy_ref"`
	Version      int64    `json:"version"`
	CreatedAt    string   `json:"created_at"`
	UpdatedAt    string   `json:"updated_at"`
}

// Contract is contractual income of every kind - a sale, a prize, a
// sponsored post, a grant, piecework. Amount is always the authoritative
// figure; it is never adjusted to match receivable_plans or receipts.
type Contract struct {
	ID            string   `json:"id"`
	AccountID     string   `json:"account_id"`
	OpportunityID *string  `json:"opportunity_id"`
	ApplicationID *string  `json:"application_id"`
	Kind          string   `json:"kind"`
	ContractNo    *string  `json:"contract_no"`
	Name          string   `json:"name"`
	SignDate      *string  `json:"sign_date"`
	StartDate     *string  `json:"start_date"`
	EndDate       *string  `json:"end_date"`
	Amount        float64  `json:"amount"`
	UnitPrice     *float64 `json:"unit_price"`
	Quantity      *float64 `json:"quantity"`
	Currency      string   `json:"currency"`
	Status        string   `json:"status"`
	PaymentTerms  *string  `json:"payment_terms"`
	Note          *string  `json:"note"`
	LegacyRef     *string  `json:"legacy_ref"`
	Version       int64    `json:"version"`
	CreatedAt     string   `json:"created_at"`
	UpdatedAt     string   `json:"updated_at"`
}

// ReceivablePlan is when a slice of a contract's money is supposed to
// arrive. Its sum is deliberately not constrained to equal contracts.amount.
type ReceivablePlan struct {
	ID            string  `json:"id"`
	ContractID    string  `json:"contract_id"`
	Seq           int     `json:"seq"`
	DueDate       string  `json:"due_date"`
	Amount        float64 `json:"amount"`
	ConditionNote *string `json:"condition_note"`
	Status        string  `json:"status"`
	Version       int64   `json:"version"`
	CreatedAt     string  `json:"created_at"`
	UpdatedAt     string  `json:"updated_at"`
}

// Receipt is money that actually arrived. PlanID is nullable on purpose: an
// unplanned payment is still a payment.
type Receipt struct {
	ID         string  `json:"id"`
	ContractID string  `json:"contract_id"`
	PlanID     *string `json:"plan_id"`
	ReceivedAt string  `json:"received_at"`
	Amount     float64 `json:"amount"`
	Method     *string `json:"method"`
	Note       *string `json:"note"`
	CreatedAt  string  `json:"created_at"`
}

// ServiceTicket is what happens after delivery.
type ServiceTicket struct {
	ID         string  `json:"id"`
	AccountID  string  `json:"account_id"`
	ContractID *string `json:"contract_id"`
	ProjectID  *string `json:"project_id"`
	Title      string  `json:"title"`
	OpenedAt   string  `json:"opened_at"`
	Kind       string  `json:"kind"`
	Severity   string  `json:"severity"`
	Status     string  `json:"status"`
	Assignee   *string `json:"assignee"`
	ClosedAt   *string `json:"closed_at"`
	Resolution *string `json:"resolution"`
	Version    int64   `json:"version"`
	CreatedAt  string  `json:"created_at"`
	UpdatedAt  string  `json:"updated_at"`
}

// Interaction is a conversation that happened, hanging off whatever it was
// about through subject_type/subject_id.
type Interaction struct {
	ID           string  `json:"id"`
	SubjectType  string  `json:"subject_type"`
	SubjectID    string  `json:"subject_id"`
	OccurredAt   string  `json:"occurred_at"`
	Channel      string  `json:"channel"`
	Summary      *string `json:"summary"`
	Participants *string `json:"participants"`
	Owner        *string `json:"owner"`
	CreatedAt    string  `json:"created_at"`
}

// Product is the hub of all three business lines: a contract sells one, a
// content piece promotes one, a release iterates one.
type Product struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Kind        string  `json:"kind"`
	Status      string  `json:"status"`
	Positioning *string `json:"positioning"`
	RepoURL     *string `json:"repo_url"`
	Owner       *string `json:"owner"`
	// Added by 006: the release currently shipping, and the day the product
	// first reached users. Both stay nil until there is a release to point at.
	CurrentReleaseID *string `json:"current_release_id"`
	LaunchDate       *string `json:"launch_date"`
	Version          int64   `json:"version"`
	CreatedAt        string  `json:"created_at"`
	UpdatedAt        string  `json:"updated_at"`
}

// Document is one version of one artefact. It stores no path - the bytes are
// DocumentFile rows, because one deliverable routinely has several files.
type Document struct {
	ID           string  `json:"id"`
	Kind         string  `json:"kind"`
	Title        string  `json:"title"`
	OccurredAt   *string `json:"occurred_at"`
	CapturedAt   *string `json:"captured_at"`
	ReviewAt     *string `json:"review_at"`
	LineageID    string  `json:"lineage_id"`
	SupersedesID *string `json:"supersedes_id"`
	ChangeNote   *string `json:"change_note"`
	Source       *string `json:"source"`
	AuthorName   *string `json:"author_name"`
	CanonicalURL *string `json:"canonical_url"`
	UserNote     *string `json:"user_note"`
	LegacyRef    *string `json:"legacy_ref"`
	Version      int64   `json:"version"`
	CreatedAt    string  `json:"created_at"`
	UpdatedAt    string  `json:"updated_at"`
}

// DocumentFile is one file on disk belonging to a document. Exactly one file
// per document may carry role 'original'.
type DocumentFile struct {
	ID        string  `json:"id"`
	DocID     string  `json:"doc_id"`
	RelPath   string  `json:"rel_path"`
	Mime      *string `json:"mime"`
	SizeBytes *int64  `json:"size_bytes"`
	SHA256    *string `json:"sha256"`
	Role      string  `json:"role"`
	SortOrder int     `json:"sort_order"`
	CreatedAt string  `json:"created_at"`
}

// DocLink hangs a document off any business object.
type DocLink struct {
	ID         string `json:"id"`
	DocID      string `json:"doc_id"`
	EntityType string `json:"entity_type"`
	EntityID   string `json:"entity_id"`
	LinkType   string `json:"link_type"`
	CreatedAt  string `json:"created_at"`
}

// MetricSample is any number, about anything, over time.
type MetricSample struct {
	ID          string  `json:"id"`
	SubjectType string  `json:"subject_type"`
	SubjectID   string  `json:"subject_id"`
	MetricName  string  `json:"metric_name"`
	SampledAt   string  `json:"sampled_at"`
	Value       float64 `json:"value"`
	Unit        *string `json:"unit"`
	Source      *string `json:"source"`
	Note        *string `json:"note"`
	CreatedAt   string  `json:"created_at"`
}

// ContextEdge is a SOFT relation between two business objects - a referral, a
// fork, a citation. The main chain stays on real foreign keys; this is for
// what has nowhere else to live.
type ContextEdge struct {
	ID        string  `json:"id"`
	FromType  string  `json:"from_type"`
	FromID    string  `json:"from_id"`
	ToType    string  `json:"to_type"`
	ToID      string  `json:"to_id"`
	EdgeType  string  `json:"edge_type"`
	Note      *string `json:"note"`
	CreatedAt string  `json:"created_at"`
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
