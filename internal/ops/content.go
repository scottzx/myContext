package ops

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"github.com/scottzx/mycontext/internal/adapters/sqlite"
	"github.com/scottzx/mycontext/internal/protocol"
	"github.com/scottzx/mycontext/internal/system"
)

// This file carries the use cases 006_content_product.sql has no code for:
// mode B (content, on our own channels) and mode C (product releases and the
// campaigns that promote either line). Releases live here rather than in
// product.go because a release is its own lifecycle object with a status and
// a ship date, not a product field - the same reasoning that keeps contracts
// out of accounts.go.

// ---------------------------------------------------------------------------
// channels: our own publishing accounts.
// ---------------------------------------------------------------------------

// Channel is one of our own publishing accounts - a 小红书 handle, a 公众号, an
// offline meetup series. There is no follower_count field: that number moves
// weekly and belongs in metric_samples(subject_type='channel'), read through
// v_channel_growth, never as a column that can only ever show "how many".
type Channel struct {
	ID        string  `json:"id"`
	Platform  string  `json:"platform"`
	Name      string  `json:"name"`
	Handle    *string `json:"handle"`
	Status    string  `json:"status"`
	Owner     *string `json:"owner"`
	Note      *string `json:"note"`
	LegacyRef *string `json:"legacy_ref"`
	Version   int64   `json:"version"`
	CreatedAt string  `json:"created_at"`
	UpdatedAt string  `json:"updated_at"`
}

// CreateChannelInput is the payload of `channel.create`.
type CreateChannelInput struct {
	Platform  string `json:"platform"`
	Name      string `json:"name"`
	Handle    string `json:"handle,omitempty"`
	Status    string `json:"status,omitempty"`
	Owner     string `json:"owner,omitempty"`
	Note      string `json:"note,omitempty"`
	LegacyRef string `json:"legacy_ref,omitempty"`
}

func (in *CreateChannelInput) normalize() error {
	if in.Platform == "" {
		return protocol.BadInput("platform is required")
	}
	if !validChannelPlatform[in.Platform] {
		return protocol.BadInput("platform must be xiaohongshu|wechat|douyin|bilibili|x|offline")
	}
	if in.Name == "" {
		return protocol.BadInput("name is required")
	}
	if in.Status == "" {
		in.Status = "active"
	}
	if !validChannelStatus[in.Status] {
		return protocol.BadInput("status must be active|paused|archived")
	}
	return nil
}

// CreateChannel registers one of our own publishing accounts. Two rows for
// the same (platform, handle) are a duplicate, not two channels - the
// ux_channels_handle index is what actually refuses that, not this code.
func (s *Store) CreateChannel(ctx context.Context, wc WriteContext, in CreateChannelInput) (*Result, error) {
	if err := in.normalize(); err != nil {
		return nil, err
	}
	return s.execute(ctx, "channel.create", wc, in, func(ctx context.Context, tx *sql.Tx, now time.Time) (*Result, error) {
		id := system.NewID("chan")
		ts := system.FormatTimestamp(now)
		if _, err := tx.ExecContext(ctx, `
            INSERT INTO channels (id, platform, name, handle, status, owner, note, legacy_ref,
                                  version, created_at, updated_at)
            VALUES (?,?,?,?,?,?,?,?,1,?,?)`,
			id, in.Platform, in.Name, nullString(in.Handle), in.Status, nullString(in.Owner),
			nullString(in.Note), nullString(in.LegacyRef), ts, ts); err != nil {
			return nil, err
		}
		if err := recordEvent(ctx, tx, wc, now, "channel", id, "created", nil, in); err != nil {
			return nil, err
		}
		c, err := loadChannel(ctx, tx, id)
		if err != nil {
			return nil, err
		}
		return &Result{
			Data: c,
			Changes: []protocol.Change{{EntityType: "channel", EntityID: id, EventType: "created",
				Version: 1, ProjectionKeys: []string{"channels"}}},
		}, nil
	})
}

// UpdateChannelInput patches a channel under optimistic concurrency.
type UpdateChannelInput struct {
	ChannelID       string  `json:"channel_id"`
	ExpectedVersion int64   `json:"expected_version"`
	Platform        *string `json:"platform,omitempty"`
	Name            *string `json:"name,omitempty"`
	Handle          *string `json:"handle,omitempty"`
	Status          *string `json:"status,omitempty"`
	Owner           *string `json:"owner,omitempty"`
	Note            *string `json:"note,omitempty"`
}

// UpdateChannel applies a patch under optimistic concurrency control.
func (s *Store) UpdateChannel(ctx context.Context, wc WriteContext, in UpdateChannelInput) (*Result, error) {
	if in.ChannelID == "" {
		return nil, protocol.BadInput("channel_id is required")
	}
	if in.ExpectedVersion <= 0 {
		return nil, protocol.BadInput("expected_version is required; read the channel first")
	}
	if in.Platform != nil && !validChannelPlatform[*in.Platform] {
		return nil, protocol.BadInput("platform must be xiaohongshu|wechat|douyin|bilibili|x|offline")
	}
	if in.Status != nil && !validChannelStatus[*in.Status] {
		return nil, protocol.BadInput("status must be active|paused|archived")
	}
	return s.execute(ctx, "channel.update", wc, in, func(ctx context.Context, tx *sql.Tx, now time.Time) (*Result, error) {
		before, err := loadChannel(ctx, tx, in.ChannelID)
		if err != nil {
			return nil, err
		}
		if before.Version != in.ExpectedVersion {
			return nil, protocol.VersionConflict("channel", in.ExpectedVersion, before.Version)
		}
		set := newPatch()
		set.str("platform", in.Platform)
		set.str("name", in.Name)
		set.str("handle", in.Handle)
		set.str("owner", in.Owner)
		set.str("note", in.Note)
		eventType := "updated"
		if in.Status != nil {
			set.raw("status", *in.Status)
			eventType = "status_changed"
		}
		if set.empty() {
			return nil, protocol.BadInput("no fields to update")
		}
		if err := set.apply(ctx, tx, "channels", "channel", in.ChannelID, in.ExpectedVersion, now); err != nil {
			return nil, err
		}
		after, err := loadChannel(ctx, tx, in.ChannelID)
		if err != nil {
			return nil, err
		}
		if err := recordEvent(ctx, tx, wc, now, "channel", in.ChannelID, eventType, before, after); err != nil {
			return nil, err
		}
		return &Result{
			Data: after,
			Changes: []protocol.Change{{EntityType: "channel", EntityID: in.ChannelID,
				EventType: eventType, Version: after.Version, ProjectionKeys: []string{"channels"}}},
		}, nil
	})
}

const channelColumns = `
    id, platform, name, handle, status, owner, note, legacy_ref, version, created_at, updated_at`

func scanChannel(row interface{ Scan(...any) error }) (*Channel, error) {
	var c Channel
	err := row.Scan(&c.ID, &c.Platform, &c.Name, &c.Handle, &c.Status, &c.Owner, &c.Note,
		&c.LegacyRef, &c.Version, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &c, nil
}

func loadChannel(ctx context.Context, tx *sql.Tx, id string) (*Channel, error) {
	row := tx.QueryRowContext(ctx, `SELECT `+channelColumns+` FROM channels WHERE id = ?`, id)
	c, err := scanChannel(row)
	if err == sql.ErrNoRows {
		return nil, protocol.NotFound("channel %s does not exist", id)
	}
	return c, err
}

// GetChannel loads one channel by id.
func (s *Store) GetChannel(ctx context.Context, id string) (*Channel, error) {
	row := s.db.SQL().QueryRowContext(ctx, `SELECT `+channelColumns+` FROM channels WHERE id = ?`, id)
	c, err := scanChannel(row)
	if err == sql.ErrNoRows {
		return nil, protocol.NotFound("channel %s does not exist", id)
	}
	if err != nil {
		return nil, sqlite.Classify(err)
	}
	return c, nil
}

// ChannelFilter is the query surface of `mycontext channel list`.
type ChannelFilter struct {
	Platform string
	Status   string
	Search   string
	Limit    int
}

// ListChannels returns channels, most recently updated first.
func (s *Store) ListChannels(ctx context.Context, f ChannelFilter) ([]*Channel, error) {
	query := `SELECT ` + channelColumns + ` FROM channels WHERE 1=1`
	var args []any
	if f.Platform != "" {
		query += " AND platform = ?"
		args = append(args, f.Platform)
	}
	if f.Status != "" {
		query += " AND status = ?"
		args = append(args, f.Status)
	}
	if f.Search != "" {
		query += " AND name LIKE ? ESCAPE '\\'"
		args = append(args, "%"+escapeLike(f.Search)+"%")
	}
	limit := f.Limit
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	query += " ORDER BY updated_at DESC LIMIT ?"
	args = append(args, limit)

	rows, err := s.db.SQL().QueryContext(ctx, query, args...)
	if err != nil {
		return nil, sqlite.Classify(err)
	}
	defer rows.Close()

	out := []*Channel{}
	for rows.Next() {
		c, err := scanChannel(rows)
		if err != nil {
			return nil, sqlite.Classify(err)
		}
		out = append(out, c)
	}
	return out, sqlite.Classify(rows.Err())
}

// ---------------------------------------------------------------------------
// content_pieces: one post, from 选题 to published, in one row.
// ---------------------------------------------------------------------------

// ContentPiece mirrors the content_pieces table. The body is not here: it is
// documents(kind='content_draft'), reached through DraftDocID.
type ContentPiece struct {
	ID           string  `json:"id"`
	ChannelID    *string `json:"channel_id"`
	Title        string  `json:"title"`
	Topic        *string `json:"topic"`
	ProductID    *string `json:"product_id"`
	CampaignID   *string `json:"campaign_id"`
	Status       string  `json:"status"`
	ScheduledFor *string `json:"scheduled_for"`
	PublishedAt  *string `json:"published_at"`
	URL          *string `json:"url"`
	DraftDocID   *string `json:"draft_doc_id"`
	Owner        *string `json:"owner"`
	Note         *string `json:"note"`
	LegacyRef    *string `json:"legacy_ref"`
	Version      int64   `json:"version"`
	CreatedAt    string  `json:"created_at"`
	UpdatedAt    string  `json:"updated_at"`
}

// CreateContentPieceInput is the payload of `content.create`. There is no
// status field: create IS how a 选题 is recorded, so every piece this use
// case produces starts at status='idea'. Reaching any later status is a
// separate write against the same row - see UpdateContentPiece and
// PublishContentPiece.
type CreateContentPieceInput struct {
	ChannelID  string `json:"channel_id,omitempty"`
	Title      string `json:"title"`
	Topic      string `json:"topic,omitempty"`
	ProductID  string `json:"product_id,omitempty"`
	CampaignID string `json:"campaign_id,omitempty"`
	Owner      string `json:"owner,omitempty"`
	Note       string `json:"note,omitempty"`
	LegacyRef  string `json:"legacy_ref,omitempty"`
}

func (in *CreateContentPieceInput) normalize() error {
	if in.Title == "" {
		return protocol.BadInput("title is required")
	}
	return nil
}

// CreateContentPiece records a 选题: an idea, possibly with nowhere yet to
// publish it. channel_id, product_id and campaign_id are all optional and
// independently checked, because an idea legitimately precedes any of those
// decisions.
func (s *Store) CreateContentPiece(ctx context.Context, wc WriteContext, in CreateContentPieceInput) (*Result, error) {
	if err := in.normalize(); err != nil {
		return nil, err
	}
	return s.execute(ctx, "content.create", wc, in, func(ctx context.Context, tx *sql.Tx, now time.Time) (*Result, error) {
		if in.ChannelID != "" {
			if err := requireExists(ctx, tx, "channels", in.ChannelID, "channel"); err != nil {
				return nil, err
			}
		}
		if in.ProductID != "" {
			if err := requireExists(ctx, tx, "products", in.ProductID, "product"); err != nil {
				return nil, err
			}
		}
		if in.CampaignID != "" {
			if err := requireExists(ctx, tx, "campaigns", in.CampaignID, "campaign"); err != nil {
				return nil, err
			}
		}
		id := system.NewID("cp")
		ts := system.FormatTimestamp(now)
		if _, err := tx.ExecContext(ctx, `
            INSERT INTO content_pieces (id, channel_id, title, topic, product_id, campaign_id,
                                        status, owner, note, legacy_ref, version, created_at, updated_at)
            VALUES (?,?,?,?,?,?,'idea',?,?,?,1,?,?)`,
			id, nullString(in.ChannelID), in.Title, nullString(in.Topic), nullString(in.ProductID),
			nullString(in.CampaignID), nullString(in.Owner), nullString(in.Note),
			nullString(in.LegacyRef), ts, ts); err != nil {
			return nil, err
		}
		if err := recordEvent(ctx, tx, wc, now, "content_piece", id, "created", nil, in); err != nil {
			return nil, err
		}
		cp, err := loadContentPiece(ctx, tx, id)
		if err != nil {
			return nil, err
		}
		keys := []string{"content_pieces"}
		if in.ChannelID != "" {
			keys = append(keys, "channel:"+in.ChannelID)
		}
		return &Result{
			Data: cp,
			Changes: []protocol.Change{{EntityType: "content_piece", EntityID: id, EventType: "created",
				Version: 1, ProjectionKeys: keys}},
		}, nil
	})
}

// UpdateContentPieceInput patches a content piece under optimistic
// concurrency. published_at and url are deliberately absent here: they are
// only ever written by PublishContentPiece, so a piece cannot be pushed to
// status='published' through this path at all.
type UpdateContentPieceInput struct {
	ContentPieceID  string  `json:"content_piece_id"`
	ExpectedVersion int64   `json:"expected_version"`
	ChannelID       *string `json:"channel_id,omitempty"`
	Title           *string `json:"title,omitempty"`
	Topic           *string `json:"topic,omitempty"`
	ProductID       *string `json:"product_id,omitempty"`
	CampaignID      *string `json:"campaign_id,omitempty"`
	Status          *string `json:"status,omitempty"`
	ScheduledFor    *string `json:"scheduled_for,omitempty"`
	Owner           *string `json:"owner,omitempty"`
	Note            *string `json:"note,omitempty"`
}

// UpdateContentPiece applies a patch under optimistic concurrency control.
// Two rules SQL cannot express are enforced here: a piece already published
// may not be moved back to drafting - the fact that it went out does not
// un-happen - and moving TO status='published' is refused outright, because
// that transition also needs a published_at and this input has nowhere to
// put one; use `content publish` instead.
func (s *Store) UpdateContentPiece(ctx context.Context, wc WriteContext, in UpdateContentPieceInput) (*Result, error) {
	if in.ContentPieceID == "" {
		return nil, protocol.BadInput("content_piece_id is required")
	}
	if in.ExpectedVersion <= 0 {
		return nil, protocol.BadInput("expected_version is required; read the content piece first")
	}
	if in.Status != nil && !validContentPieceStatus[*in.Status] {
		return nil, protocol.BadInput("status must be idea|drafting|review|scheduled|published|archived")
	}
	if in.ScheduledFor != nil && *in.ScheduledFor != "" {
		if err := ValidateTimestamp("scheduled_for", *in.ScheduledFor); err != nil {
			return nil, err
		}
	}
	return s.execute(ctx, "content.update", wc, in, func(ctx context.Context, tx *sql.Tx, now time.Time) (*Result, error) {
		before, err := loadContentPiece(ctx, tx, in.ContentPieceID)
		if err != nil {
			return nil, err
		}
		if before.Version != in.ExpectedVersion {
			return nil, protocol.VersionConflict("content_piece", in.ExpectedVersion, before.Version)
		}
		if in.ChannelID != nil && *in.ChannelID != "" {
			if err := requireExists(ctx, tx, "channels", *in.ChannelID, "channel"); err != nil {
				return nil, err
			}
		}
		if in.ProductID != nil && *in.ProductID != "" {
			if err := requireExists(ctx, tx, "products", *in.ProductID, "product"); err != nil {
				return nil, err
			}
		}
		if in.CampaignID != nil && *in.CampaignID != "" {
			if err := requireExists(ctx, tx, "campaigns", *in.CampaignID, "campaign"); err != nil {
				return nil, err
			}
		}

		set := newPatch()
		set.str("channel_id", in.ChannelID)
		set.str("title", in.Title)
		set.str("topic", in.Topic)
		set.str("product_id", in.ProductID)
		set.str("campaign_id", in.CampaignID)
		set.str("owner", in.Owner)
		set.str("note", in.Note)
		set.str("scheduled_for", in.ScheduledFor)

		eventType := "updated"
		if in.Status != nil && *in.Status != before.Status {
			switch *in.Status {
			case "published":
				return nil, protocol.BadInput("use `content publish` to move a piece to published; it also records published_at")
			case "drafting":
				if before.Status == "published" {
					return nil, protocol.BadInput("content piece %s has already been published; it may not move back to drafting", before.ID)
				}
			case "scheduled":
				scheduledFor := before.ScheduledFor
				if in.ScheduledFor != nil {
					scheduledFor = in.ScheduledFor
				}
				if scheduledFor == nil || *scheduledFor == "" {
					return nil, protocol.BadInput("status scheduled requires scheduled_for")
				}
			}
			set.raw("status", *in.Status)
			eventType = "stage_changed"
		}
		if set.empty() {
			return nil, protocol.BadInput("no fields to update")
		}
		if err := set.apply(ctx, tx, "content_pieces", "content_piece", in.ContentPieceID, in.ExpectedVersion, now); err != nil {
			return nil, err
		}
		after, err := loadContentPiece(ctx, tx, in.ContentPieceID)
		if err != nil {
			return nil, err
		}
		if err := recordEvent(ctx, tx, wc, now, "content_piece", in.ContentPieceID, eventType, before, after); err != nil {
			return nil, err
		}
		return &Result{
			Data: after,
			Changes: []protocol.Change{{EntityType: "content_piece", EntityID: in.ContentPieceID,
				EventType: eventType, Version: after.Version, ProjectionKeys: []string{"content_pieces"}}},
		}, nil
	})
}

// PublishContentPieceInput is the payload of `content.publish`. published_at
// defaults to now, matching how milestone.go stamps reached_at.
type PublishContentPieceInput struct {
	ContentPieceID  string `json:"content_piece_id"`
	ExpectedVersion int64  `json:"expected_version"`
	URL             string `json:"url,omitempty"`
	PublishedAt     string `json:"published_at,omitempty"`
}

func (in *PublishContentPieceInput) normalize() error {
	if in.ContentPieceID == "" {
		return protocol.BadInput("content_piece_id is required")
	}
	if in.ExpectedVersion <= 0 {
		return protocol.BadInput("expected_version is required; read the content piece first")
	}
	if in.PublishedAt != "" {
		if err := ValidateTimestamp("published_at", in.PublishedAt); err != nil {
			return err
		}
	}
	return nil
}

// PublishContentPiece is the one path that can move a piece to
// status='published': it is also the one path that writes published_at,
// which is exactly why the two facts can never disagree.
func (s *Store) PublishContentPiece(ctx context.Context, wc WriteContext, in PublishContentPieceInput) (*Result, error) {
	if err := in.normalize(); err != nil {
		return nil, err
	}
	return s.execute(ctx, "content.publish", wc, in, func(ctx context.Context, tx *sql.Tx, now time.Time) (*Result, error) {
		before, err := loadContentPiece(ctx, tx, in.ContentPieceID)
		if err != nil {
			return nil, err
		}
		if before.Version != in.ExpectedVersion {
			return nil, protocol.VersionConflict("content_piece", in.ExpectedVersion, before.Version)
		}
		publishedAt := system.FormatTimestamp(now)
		if in.PublishedAt != "" {
			publishedAt = in.PublishedAt
		}
		set := newPatch()
		set.raw("status", "published")
		set.raw("published_at", publishedAt)
		if in.URL != "" {
			set.raw("url", in.URL)
		}
		if err := set.apply(ctx, tx, "content_pieces", "content_piece", in.ContentPieceID, in.ExpectedVersion, now); err != nil {
			return nil, err
		}
		after, err := loadContentPiece(ctx, tx, in.ContentPieceID)
		if err != nil {
			return nil, err
		}
		if err := recordEvent(ctx, tx, wc, now, "content_piece", in.ContentPieceID, "published", before, after); err != nil {
			return nil, err
		}
		return &Result{
			Data: after,
			Changes: []protocol.Change{{EntityType: "content_piece", EntityID: in.ContentPieceID,
				EventType: "published", Version: after.Version, ProjectionKeys: []string{"content_pieces"}}},
		}, nil
	})
}

const contentPieceColumns = `
    id, channel_id, title, topic, product_id, campaign_id, status, scheduled_for,
    published_at, url, draft_doc_id, owner, note, legacy_ref, version, created_at, updated_at`

func scanContentPiece(row interface{ Scan(...any) error }) (*ContentPiece, error) {
	var c ContentPiece
	err := row.Scan(&c.ID, &c.ChannelID, &c.Title, &c.Topic, &c.ProductID, &c.CampaignID,
		&c.Status, &c.ScheduledFor, &c.PublishedAt, &c.URL, &c.DraftDocID, &c.Owner, &c.Note,
		&c.LegacyRef, &c.Version, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &c, nil
}

func loadContentPiece(ctx context.Context, tx *sql.Tx, id string) (*ContentPiece, error) {
	row := tx.QueryRowContext(ctx, `SELECT `+contentPieceColumns+` FROM content_pieces WHERE id = ?`, id)
	c, err := scanContentPiece(row)
	if err == sql.ErrNoRows {
		return nil, protocol.NotFound("content piece %s does not exist", id)
	}
	return c, err
}

// GetContentPiece loads one content piece by id.
func (s *Store) GetContentPiece(ctx context.Context, id string) (*ContentPiece, error) {
	row := s.db.SQL().QueryRowContext(ctx, `SELECT `+contentPieceColumns+` FROM content_pieces WHERE id = ?`, id)
	c, err := scanContentPiece(row)
	if err == sql.ErrNoRows {
		return nil, protocol.NotFound("content piece %s does not exist", id)
	}
	if err != nil {
		return nil, sqlite.Classify(err)
	}
	return c, nil
}

// ContentPieceFilter is the query surface of `mycontext content list`.
type ContentPieceFilter struct {
	ChannelID  string
	Status     string
	ProductID  string
	CampaignID string
	Search     string
	Limit      int
}

// ListContentPieces returns content pieces, most recently updated first.
func (s *Store) ListContentPieces(ctx context.Context, f ContentPieceFilter) ([]*ContentPiece, error) {
	var where []string
	var args []any
	if f.ChannelID != "" {
		where = append(where, "channel_id = ?")
		args = append(args, f.ChannelID)
	}
	if f.Status != "" {
		where = append(where, "status = ?")
		args = append(args, f.Status)
	}
	if f.ProductID != "" {
		where = append(where, "product_id = ?")
		args = append(args, f.ProductID)
	}
	if f.CampaignID != "" {
		where = append(where, "campaign_id = ?")
		args = append(args, f.CampaignID)
	}
	if f.Search != "" {
		where = append(where, "title LIKE ? ESCAPE '\\'")
		args = append(args, "%"+escapeLike(f.Search)+"%")
	}

	query := `SELECT ` + contentPieceColumns + ` FROM content_pieces`
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	limit := f.Limit
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	query += " ORDER BY updated_at DESC LIMIT ?"
	args = append(args, limit)

	rows, err := s.db.SQL().QueryContext(ctx, query, args...)
	if err != nil {
		return nil, sqlite.Classify(err)
	}
	defer rows.Close()

	out := []*ContentPiece{}
	for rows.Next() {
		c, err := scanContentPiece(rows)
		if err != nil {
			return nil, sqlite.Classify(err)
		}
		out = append(out, c)
	}
	return out, sqlite.Classify(rows.Err())
}

// ---------------------------------------------------------------------------
// releases: one shipped version of a product.
// ---------------------------------------------------------------------------

// Release is one shipped version. The engineering that produced it is a
// project with tasks and milestones; this row is what came out of it.
type Release struct {
	ID           string  `json:"id"`
	ProductID    string  `json:"product_id"`
	VersionLabel string  `json:"version_label"`
	Status       string  `json:"status"`
	ReleasedAt   *string `json:"released_at"`
	NotesDocID   *string `json:"notes_doc_id"`
	Note         *string `json:"note"`
	LegacyRef    *string `json:"legacy_ref"`
	Version      int64   `json:"version"`
	CreatedAt    string  `json:"created_at"`
	UpdatedAt    string  `json:"updated_at"`
}

// CreateReleaseInput is the payload of `release.create`. Status accepts only
// the two pre-release values: reaching 'released' or 'rolled_back' also
// requires released_at, and the only place that ever sets that field is
// ShipRelease.
type CreateReleaseInput struct {
	ProductID    string `json:"product_id"`
	VersionLabel string `json:"version_label"`
	Status       string `json:"status,omitempty"`
	Note         string `json:"note,omitempty"`
	LegacyRef    string `json:"legacy_ref,omitempty"`
}

func (in *CreateReleaseInput) normalize() error {
	if in.ProductID == "" {
		return protocol.BadInput("product_id is required")
	}
	if in.VersionLabel == "" || in.VersionLabel != strings.TrimSpace(in.VersionLabel) {
		return protocol.BadInput("version_label must be non-empty and trimmed")
	}
	if in.Status == "" {
		in.Status = "planned"
	}
	if !validReleaseStatus[in.Status] {
		return protocol.BadInput("status must be planned|developing|released|rolled_back")
	}
	if in.Status != "planned" && in.Status != "developing" {
		return protocol.BadInput("status must be planned or developing at creation; use `release ship` to mark it released")
	}
	return nil
}

// CreateRelease records a planned or in-progress version. v0.3.0 of 听记
// exists once per product - the ux_releases_version index is what actually
// refuses a duplicate, not this code.
func (s *Store) CreateRelease(ctx context.Context, wc WriteContext, in CreateReleaseInput) (*Result, error) {
	if err := in.normalize(); err != nil {
		return nil, err
	}
	return s.execute(ctx, "release.create", wc, in, func(ctx context.Context, tx *sql.Tx, now time.Time) (*Result, error) {
		if err := requireExists(ctx, tx, "products", in.ProductID, "product"); err != nil {
			return nil, err
		}
		id := system.NewID("rel")
		ts := system.FormatTimestamp(now)
		if _, err := tx.ExecContext(ctx, `
            INSERT INTO releases (id, product_id, version_label, status, note, legacy_ref,
                                  version, created_at, updated_at)
            VALUES (?,?,?,?,?,?,1,?,?)`,
			id, in.ProductID, in.VersionLabel, in.Status, nullString(in.Note),
			nullString(in.LegacyRef), ts, ts); err != nil {
			return nil, err
		}
		if err := recordEvent(ctx, tx, wc, now, "release", id, "created", nil, in); err != nil {
			return nil, err
		}
		r, err := loadRelease(ctx, tx, id)
		if err != nil {
			return nil, err
		}
		return &Result{
			Data: r,
			Changes: []protocol.Change{{EntityType: "release", EntityID: id, EventType: "created",
				Version: 1, ProjectionKeys: []string{"releases", "product:" + in.ProductID}}},
		}, nil
	})
}

// ShipReleaseInput is the payload of `release.ship`.
type ShipReleaseInput struct {
	ReleaseID       string `json:"release_id"`
	ExpectedVersion int64  `json:"expected_version"`
	ReleasedAt      string `json:"released_at,omitempty"`
}

func (in *ShipReleaseInput) normalize() error {
	if in.ReleaseID == "" {
		return protocol.BadInput("release_id is required")
	}
	if in.ExpectedVersion <= 0 {
		return protocol.BadInput("expected_version is required; read the release first")
	}
	if in.ReleasedAt != "" {
		if err := ValidateTimestamp("released_at", in.ReleasedAt); err != nil {
			return err
		}
	}
	return nil
}

// ShipRelease marks a release shipped. It is the only path that sets
// status='released' and the only path that sets released_at, so the CHECK
// requiring one whenever the other is true can never be at odds with a
// pending write.
func (s *Store) ShipRelease(ctx context.Context, wc WriteContext, in ShipReleaseInput) (*Result, error) {
	if err := in.normalize(); err != nil {
		return nil, err
	}
	return s.execute(ctx, "release.ship", wc, in, func(ctx context.Context, tx *sql.Tx, now time.Time) (*Result, error) {
		before, err := loadRelease(ctx, tx, in.ReleaseID)
		if err != nil {
			return nil, err
		}
		if before.Version != in.ExpectedVersion {
			return nil, protocol.VersionConflict("release", in.ExpectedVersion, before.Version)
		}
		releasedAt := system.FormatTimestamp(now)
		if in.ReleasedAt != "" {
			releasedAt = in.ReleasedAt
		}
		set := newPatch()
		set.raw("status", "released")
		set.raw("released_at", releasedAt)
		if err := set.apply(ctx, tx, "releases", "release", in.ReleaseID, in.ExpectedVersion, now); err != nil {
			return nil, err
		}
		after, err := loadRelease(ctx, tx, in.ReleaseID)
		if err != nil {
			return nil, err
		}
		if err := recordEvent(ctx, tx, wc, now, "release", in.ReleaseID, "released", before, after); err != nil {
			return nil, err
		}
		return &Result{
			Data: after,
			Changes: []protocol.Change{{EntityType: "release", EntityID: in.ReleaseID,
				EventType: "released", Version: after.Version,
				ProjectionKeys: []string{"releases", "product:" + before.ProductID}}},
		}, nil
	})
}

const releaseColumns = `
    id, product_id, version_label, status, released_at, notes_doc_id, note, legacy_ref,
    version, created_at, updated_at`

func scanRelease(row interface{ Scan(...any) error }) (*Release, error) {
	var r Release
	err := row.Scan(&r.ID, &r.ProductID, &r.VersionLabel, &r.Status, &r.ReleasedAt,
		&r.NotesDocID, &r.Note, &r.LegacyRef, &r.Version, &r.CreatedAt, &r.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &r, nil
}

func loadRelease(ctx context.Context, tx *sql.Tx, id string) (*Release, error) {
	row := tx.QueryRowContext(ctx, `SELECT `+releaseColumns+` FROM releases WHERE id = ?`, id)
	r, err := scanRelease(row)
	if err == sql.ErrNoRows {
		return nil, protocol.NotFound("release %s does not exist", id)
	}
	return r, err
}

// GetRelease loads one release by id.
func (s *Store) GetRelease(ctx context.Context, id string) (*Release, error) {
	row := s.db.SQL().QueryRowContext(ctx, `SELECT `+releaseColumns+` FROM releases WHERE id = ?`, id)
	r, err := scanRelease(row)
	if err == sql.ErrNoRows {
		return nil, protocol.NotFound("release %s does not exist", id)
	}
	if err != nil {
		return nil, sqlite.Classify(err)
	}
	return r, nil
}

// ReleaseFilter is the query surface of `mycontext release list`.
type ReleaseFilter struct {
	ProductID string
	Status    string
	Limit     int
}

// ListReleases returns releases, newest first.
func (s *Store) ListReleases(ctx context.Context, f ReleaseFilter) ([]*Release, error) {
	query := `SELECT ` + releaseColumns + ` FROM releases WHERE 1=1`
	var args []any
	if f.ProductID != "" {
		query += " AND product_id = ?"
		args = append(args, f.ProductID)
	}
	if f.Status != "" {
		query += " AND status = ?"
		args = append(args, f.Status)
	}
	limit := f.Limit
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	query += " ORDER BY created_at DESC LIMIT ?"
	args = append(args, limit)

	rows, err := s.db.SQL().QueryContext(ctx, query, args...)
	if err != nil {
		return nil, sqlite.Classify(err)
	}
	defer rows.Close()

	out := []*Release{}
	for rows.Next() {
		r, err := scanRelease(rows)
		if err != nil {
			return nil, sqlite.Classify(err)
		}
		out = append(out, r)
	}
	return out, sqlite.Classify(rows.Err())
}

// ---------------------------------------------------------------------------
// campaigns: one promotion push, online or offline.
// ---------------------------------------------------------------------------

// Campaign is one promotion push. It serves mode C (a product launch) and
// mode B (a content push) with the same shape - a 杭州 meetup and a paid
// 小红书 slot differ by ChannelType and nothing else structurally.
type Campaign struct {
	ID          string   `json:"id"`
	ProductID   *string  `json:"product_id"`
	Name        string   `json:"name"`
	ChannelType string   `json:"channel_type"`
	StartDate   *string  `json:"start_date"`
	EndDate     *string  `json:"end_date"`
	Budget      *float64 `json:"budget"`
	Owner       *string  `json:"owner"`
	Status      string   `json:"status"`
	Note        *string  `json:"note"`
	LegacyRef   *string  `json:"legacy_ref"`
	Version     int64    `json:"version"`
	CreatedAt   string   `json:"created_at"`
	UpdatedAt   string   `json:"updated_at"`
}

// CreateCampaignInput is the payload of `campaign.create`. There is no status
// field: every campaign this use case produces starts at status='planned',
// the one status the CHECK on start_date never requires a date for.
type CreateCampaignInput struct {
	ProductID   string   `json:"product_id,omitempty"`
	Name        string   `json:"name"`
	ChannelType string   `json:"channel_type,omitempty"`
	StartDate   string   `json:"start_date,omitempty"`
	EndDate     string   `json:"end_date,omitempty"`
	Budget      *float64 `json:"budget,omitempty"`
	Owner       string   `json:"owner,omitempty"`
	Note        string   `json:"note,omitempty"`
	LegacyRef   string   `json:"legacy_ref,omitempty"`
}

func (in *CreateCampaignInput) normalize() error {
	if in.Name == "" {
		return protocol.BadInput("name is required")
	}
	if in.ChannelType == "" {
		in.ChannelType = "online"
	}
	if !validCampaignChannelType[in.ChannelType] {
		return protocol.BadInput("channel_type must be online|offline")
	}
	if in.Budget != nil && *in.Budget < 0 {
		return protocol.BadInput("budget cannot be negative")
	}
	for field, value := range map[string]string{"start_date": in.StartDate, "end_date": in.EndDate} {
		if value != "" {
			if err := ValidateDate(field, value); err != nil {
				return err
			}
		}
	}
	if in.StartDate != "" && in.EndDate != "" && in.EndDate < in.StartDate {
		return protocol.BadInput("end_date must not be before start_date")
	}
	return nil
}

// CreateCampaign records a promotion push. product_id is optional - a
// brand-level content push promotes no single product.
func (s *Store) CreateCampaign(ctx context.Context, wc WriteContext, in CreateCampaignInput) (*Result, error) {
	if err := in.normalize(); err != nil {
		return nil, err
	}
	return s.execute(ctx, "campaign.create", wc, in, func(ctx context.Context, tx *sql.Tx, now time.Time) (*Result, error) {
		if in.ProductID != "" {
			if err := requireExists(ctx, tx, "products", in.ProductID, "product"); err != nil {
				return nil, err
			}
		}
		id := system.NewID("camp")
		ts := system.FormatTimestamp(now)
		if _, err := tx.ExecContext(ctx, `
            INSERT INTO campaigns (id, product_id, name, channel_type, start_date, end_date,
                                   budget, owner, status, note, legacy_ref, version, created_at, updated_at)
            VALUES (?,?,?,?,?,?,?,?,'planned',?,?,1,?,?)`,
			id, nullString(in.ProductID), in.Name, in.ChannelType, nullString(in.StartDate),
			nullString(in.EndDate), nullFloat(in.Budget), nullString(in.Owner),
			nullString(in.Note), nullString(in.LegacyRef), ts, ts); err != nil {
			return nil, err
		}
		if err := recordEvent(ctx, tx, wc, now, "campaign", id, "created", nil, in); err != nil {
			return nil, err
		}
		c, err := loadCampaign(ctx, tx, id)
		if err != nil {
			return nil, err
		}
		keys := []string{"campaigns"}
		if in.ProductID != "" {
			keys = append(keys, "product:"+in.ProductID)
		}
		return &Result{
			Data: c,
			Changes: []protocol.Change{{EntityType: "campaign", EntityID: id, EventType: "created",
				Version: 1, ProjectionKeys: keys}},
		}, nil
	})
}

// CloseCampaignInput is the payload of `campaign.close`.
type CloseCampaignInput struct {
	CampaignID      string `json:"campaign_id"`
	ExpectedVersion int64  `json:"expected_version"`
	EndDate         string `json:"end_date,omitempty"`
}

func (in *CloseCampaignInput) normalize() error {
	if in.CampaignID == "" {
		return protocol.BadInput("campaign_id is required")
	}
	if in.ExpectedVersion <= 0 {
		return protocol.BadInput("expected_version is required; read the campaign first")
	}
	if in.EndDate != "" {
		if err := ValidateDate("end_date", in.EndDate); err != nil {
			return err
		}
	}
	return nil
}

// CloseCampaign marks a campaign ended. Two rules SQL states as a CHECK but
// this still validates ahead of the write, the same way contract.create
// checks sign_date before insert rather than letting the CHECK reject it:
// an ended campaign needs a start_date to have a window at all, and its
// end_date may not precede that start_date.
func (s *Store) CloseCampaign(ctx context.Context, wc WriteContext, in CloseCampaignInput) (*Result, error) {
	if err := in.normalize(); err != nil {
		return nil, err
	}
	return s.execute(ctx, "campaign.close", wc, in, func(ctx context.Context, tx *sql.Tx, now time.Time) (*Result, error) {
		before, err := loadCampaign(ctx, tx, in.CampaignID)
		if err != nil {
			return nil, err
		}
		if before.Version != in.ExpectedVersion {
			return nil, protocol.VersionConflict("campaign", in.ExpectedVersion, before.Version)
		}
		if before.StartDate == nil || *before.StartDate == "" {
			return nil, protocol.BadInput("campaign %s has no start_date; set one before closing it", before.ID)
		}
		endDate := now.UTC().Format(system.DateLayout)
		if before.EndDate != nil && *before.EndDate != "" {
			endDate = *before.EndDate
		}
		if in.EndDate != "" {
			endDate = in.EndDate
		}
		if endDate < *before.StartDate {
			return nil, protocol.BadInput("end_date %s must not be before start_date %s", endDate, *before.StartDate)
		}
		set := newPatch()
		set.raw("status", "ended")
		set.raw("end_date", endDate)
		if err := set.apply(ctx, tx, "campaigns", "campaign", in.CampaignID, in.ExpectedVersion, now); err != nil {
			return nil, err
		}
		after, err := loadCampaign(ctx, tx, in.CampaignID)
		if err != nil {
			return nil, err
		}
		// events.event_type carries a fixed CHECK vocabulary from
		// 001/005_business_core.sql that 006 does not extend; 'completed' is
		// the closest existing verb to "this run is over" (the same one
		// milestone.go uses for reaching 'hit'), so campaign close reuses it
		// rather than writing a value the CHECK would reject.
		if err := recordEvent(ctx, tx, wc, now, "campaign", in.CampaignID, "completed", before, after); err != nil {
			return nil, err
		}
		keys := []string{"campaigns"}
		if after.ProductID != nil {
			keys = append(keys, "product:"+*after.ProductID)
		}
		return &Result{
			Data: after,
			Changes: []protocol.Change{{EntityType: "campaign", EntityID: in.CampaignID,
				EventType: "completed", Version: after.Version, ProjectionKeys: keys}},
		}, nil
	})
}

const campaignColumns = `
    id, product_id, name, channel_type, start_date, end_date, budget, owner, status,
    note, legacy_ref, version, created_at, updated_at`

func scanCampaign(row interface{ Scan(...any) error }) (*Campaign, error) {
	var c Campaign
	err := row.Scan(&c.ID, &c.ProductID, &c.Name, &c.ChannelType, &c.StartDate, &c.EndDate,
		&c.Budget, &c.Owner, &c.Status, &c.Note, &c.LegacyRef, &c.Version, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &c, nil
}

func loadCampaign(ctx context.Context, tx *sql.Tx, id string) (*Campaign, error) {
	row := tx.QueryRowContext(ctx, `SELECT `+campaignColumns+` FROM campaigns WHERE id = ?`, id)
	c, err := scanCampaign(row)
	if err == sql.ErrNoRows {
		return nil, protocol.NotFound("campaign %s does not exist", id)
	}
	return c, err
}

// GetCampaign loads one campaign by id.
func (s *Store) GetCampaign(ctx context.Context, id string) (*Campaign, error) {
	row := s.db.SQL().QueryRowContext(ctx, `SELECT `+campaignColumns+` FROM campaigns WHERE id = ?`, id)
	c, err := scanCampaign(row)
	if err == sql.ErrNoRows {
		return nil, protocol.NotFound("campaign %s does not exist", id)
	}
	if err != nil {
		return nil, sqlite.Classify(err)
	}
	return c, nil
}

// CampaignFilter is the query surface of `mycontext campaign list`.
type CampaignFilter struct {
	ProductID   string
	Status      string
	ChannelType string
	Search      string
	Limit       int
}

// ListCampaigns returns campaigns, most recently updated first.
func (s *Store) ListCampaigns(ctx context.Context, f CampaignFilter) ([]*Campaign, error) {
	var where []string
	var args []any
	if f.ProductID != "" {
		where = append(where, "product_id = ?")
		args = append(args, f.ProductID)
	}
	if f.Status != "" {
		where = append(where, "status = ?")
		args = append(args, f.Status)
	}
	if f.ChannelType != "" {
		where = append(where, "channel_type = ?")
		args = append(args, f.ChannelType)
	}
	if f.Search != "" {
		where = append(where, "name LIKE ? ESCAPE '\\'")
		args = append(args, "%"+escapeLike(f.Search)+"%")
	}

	query := `SELECT ` + campaignColumns + ` FROM campaigns`
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	limit := f.Limit
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	query += " ORDER BY updated_at DESC LIMIT ?"
	args = append(args, limit)

	rows, err := s.db.SQL().QueryContext(ctx, query, args...)
	if err != nil {
		return nil, sqlite.Classify(err)
	}
	defer rows.Close()

	out := []*Campaign{}
	for rows.Next() {
		c, err := scanCampaign(rows)
		if err != nil {
			return nil, sqlite.Classify(err)
		}
		out = append(out, c)
	}
	return out, sqlite.Classify(rows.Err())
}
