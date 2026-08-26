package ops_test

import (
	"context"
	"testing"

	"github.com/scottzx/mycontext/internal/ops"
	"github.com/scottzx/mycontext/internal/protocol"
	"github.com/scottzx/mycontext/internal/system"
)

// ---------------------------------------------------------------------------
// Fixtures
// ---------------------------------------------------------------------------

func mustCreateChannel(t *testing.T, store *ops.Store, in ops.CreateChannelInput) *ops.Channel {
	t.Helper()
	result, err := store.CreateChannel(context.Background(), writeCtx(system.NewID("req")), in)
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	c, ok := result.Data.(*ops.Channel)
	if !ok {
		t.Fatalf("create channel returned %T", result.Data)
	}
	return c
}

func mustCreateProduct(t *testing.T, store *ops.Store, in ops.CreateProductInput) *ops.Product {
	t.Helper()
	result, err := store.CreateProduct(context.Background(), writeCtx(system.NewID("req")), in)
	if err != nil {
		t.Fatalf("create product: %v", err)
	}
	p, ok := result.Data.(*ops.Product)
	if !ok {
		t.Fatalf("create product returned %T", result.Data)
	}
	return p
}

func mustCreateContentPiece(t *testing.T, store *ops.Store, in ops.CreateContentPieceInput) *ops.ContentPiece {
	t.Helper()
	result, err := store.CreateContentPiece(context.Background(), writeCtx(system.NewID("req")), in)
	if err != nil {
		t.Fatalf("create content piece: %v", err)
	}
	cp, ok := result.Data.(*ops.ContentPiece)
	if !ok {
		t.Fatalf("create content piece returned %T", result.Data)
	}
	return cp
}

func mustCreateRelease(t *testing.T, store *ops.Store, in ops.CreateReleaseInput) *ops.Release {
	t.Helper()
	result, err := store.CreateRelease(context.Background(), writeCtx(system.NewID("req")), in)
	if err != nil {
		t.Fatalf("create release: %v", err)
	}
	r, ok := result.Data.(*ops.Release)
	if !ok {
		t.Fatalf("create release returned %T", result.Data)
	}
	return r
}

func mustCreateCampaign(t *testing.T, store *ops.Store, in ops.CreateCampaignInput) *ops.Campaign {
	t.Helper()
	result, err := store.CreateCampaign(context.Background(), writeCtx(system.NewID("req")), in)
	if err != nil {
		t.Fatalf("create campaign: %v", err)
	}
	c, ok := result.Data.(*ops.Campaign)
	if !ok {
		t.Fatalf("create campaign returned %T", result.Data)
	}
	return c
}

// ---------------------------------------------------------------------------
// channel
// ---------------------------------------------------------------------------

func TestChannelCreateListUpdate(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	c := mustCreateChannel(t, store, ops.CreateChannelInput{Platform: "xiaohongshu", Name: "听记 官方号"})
	if c.Status != "active" {
		t.Fatalf("expected default status active, got %q", c.Status)
	}

	list, err := store.ListChannels(ctx, ops.ChannelFilter{Platform: "xiaohongshu"})
	if err != nil {
		t.Fatalf("list channels: %v", err)
	}
	if len(list) != 1 || list[0].ID != c.ID {
		t.Fatalf("expected 1 channel back, got %d", len(list))
	}

	paused := "paused"
	result, err := store.UpdateChannel(ctx, writeCtx("req_update"), ops.UpdateChannelInput{
		ChannelID: c.ID, ExpectedVersion: c.Version, Status: &paused,
	})
	if err != nil {
		t.Fatalf("update channel: %v", err)
	}
	updated := result.Data.(*ops.Channel)
	if updated.Status != "paused" {
		t.Fatalf("expected status paused, got %q", updated.Status)
	}
}

func TestChannelInvalidPlatformRejected(t *testing.T) {
	store := newTestStore(t)
	_, err := store.CreateChannel(context.Background(), writeCtx("req_bad_platform"),
		ops.CreateChannelInput{Platform: "instagram", Name: "not a real platform here"})
	if code := appErrCode(t, err); code != protocol.CodeBadInput {
		t.Fatalf("expected BAD_INPUT for an unlisted platform, got %s", code)
	}
}

// ---------------------------------------------------------------------------
// content_piece: create always starts at status=idea
// ---------------------------------------------------------------------------

func TestContentCreateStartsAtIdea(t *testing.T) {
	store := newTestStore(t)
	cp := mustCreateContentPiece(t, store, ops.CreateContentPieceInput{Title: "为什么听记要做本地转写"})
	if cp.Status != "idea" {
		t.Fatalf("expected a new content piece to start at status=idea, got %q", cp.Status)
	}
	if cp.PublishedAt != nil {
		t.Fatalf("a freshly created piece must not have published_at set")
	}
}

// TestContentPublishSetsPublishedAtAndURL proves the rule: "publish requires
// a published_at" is upheld by the one path that can reach status=published,
// and that it is also the path that records the url.
func TestContentPublishSetsPublishedAtAndURL(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	cp := mustCreateContentPiece(t, store, ops.CreateContentPieceInput{Title: "听记 v0.3 上线笔记"})

	result, err := store.PublishContentPiece(ctx, writeCtx("req_publish"), ops.PublishContentPieceInput{
		ContentPieceID: cp.ID, ExpectedVersion: cp.Version, URL: "https://xiaohongshu.com/explore/abc123",
	})
	if err != nil {
		t.Fatalf("publish content piece: %v", err)
	}
	published := result.Data.(*ops.ContentPiece)
	if published.Status != "published" {
		t.Fatalf("expected status published, got %q", published.Status)
	}
	if published.PublishedAt == nil || *published.PublishedAt == "" {
		t.Fatalf("publish must set published_at")
	}
	if published.URL == nil || *published.URL != "https://xiaohongshu.com/explore/abc123" {
		t.Fatalf("publish must record the url, got %v", published.URL)
	}
}

// TestContentUpdateCannotReachPublished proves that the generic update path
// cannot move a piece to status=published at all - that would let
// published_at go unset, which is exactly the state SQL's own CHECK forbids.
func TestContentUpdateCannotReachPublished(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	cp := mustCreateContentPiece(t, store, ops.CreateContentPieceInput{Title: "草稿中的选题"})

	published := "published"
	_, err := store.UpdateContentPiece(ctx, writeCtx("req_bad_publish"), ops.UpdateContentPieceInput{
		ContentPieceID: cp.ID, ExpectedVersion: cp.Version, Status: &published,
	})
	if code := appErrCode(t, err); code != protocol.CodeBadInput {
		t.Fatalf("expected BAD_INPUT when updating status straight to published, got %s", code)
	}
}

// TestContentCannotMoveBackwardsFromPublishedToDrafting proves the one rule
// SQL truly cannot express: once a piece has gone out, it may not be
// pretended back into drafting.
func TestContentCannotMoveBackwardsFromPublishedToDrafting(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	cp := mustCreateContentPiece(t, store, ops.CreateContentPieceInput{Title: "已发布的复盘文章"})

	result, err := store.PublishContentPiece(ctx, writeCtx("req_publish2"), ops.PublishContentPieceInput{
		ContentPieceID: cp.ID, ExpectedVersion: cp.Version,
	})
	if err != nil {
		t.Fatalf("publish content piece: %v", err)
	}
	published := result.Data.(*ops.ContentPiece)

	drafting := "drafting"
	_, err = store.UpdateContentPiece(ctx, writeCtx("req_backwards"), ops.UpdateContentPieceInput{
		ContentPieceID: cp.ID, ExpectedVersion: published.Version, Status: &drafting,
	})
	if code := appErrCode(t, err); code != protocol.CodeBadInput {
		t.Fatalf("expected BAD_INPUT moving a published piece back to drafting, got %s", code)
	}
}

// TestContentScheduledRequiresScheduledFor proves the piece cannot enter
// status=scheduled without a scheduled_for, matching content_pieces' own
// CHECK - validated here ahead of the write for a clean error.
func TestContentScheduledRequiresScheduledFor(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	cp := mustCreateContentPiece(t, store, ops.CreateContentPieceInput{Title: "计划中的推文"})

	scheduled := "scheduled"
	_, err := store.UpdateContentPiece(ctx, writeCtx("req_no_schedule"), ops.UpdateContentPieceInput{
		ContentPieceID: cp.ID, ExpectedVersion: cp.Version, Status: &scheduled,
	})
	if code := appErrCode(t, err); code != protocol.CodeBadInput {
		t.Fatalf("expected BAD_INPUT for status=scheduled with no scheduled_for, got %s", code)
	}

	when := "2026-09-01T09:00:00Z"
	result, err := store.UpdateContentPiece(ctx, writeCtx("req_with_schedule"), ops.UpdateContentPieceInput{
		ContentPieceID: cp.ID, ExpectedVersion: cp.Version, Status: &scheduled, ScheduledFor: &when,
	})
	if err != nil {
		t.Fatalf("expected scheduling with scheduled_for to succeed: %v", err)
	}
	if got := result.Data.(*ops.ContentPiece).Status; got != "scheduled" {
		t.Fatalf("expected status scheduled, got %q", got)
	}
}

// ---------------------------------------------------------------------------
// release: create restricted to pre-release states; ship requires released_at
// ---------------------------------------------------------------------------

func TestReleaseCreateRejectsReleasedStatus(t *testing.T) {
	store := newTestStore(t)
	product := mustCreateProduct(t, store, ops.CreateProductInput{Name: "听记"})

	_, err := store.CreateRelease(context.Background(), writeCtx("req_bad_release_status"), ops.CreateReleaseInput{
		ProductID: product.ID, VersionLabel: "v0.3.0", Status: "released",
	})
	if code := appErrCode(t, err); code != protocol.CodeBadInput {
		t.Fatalf("expected BAD_INPUT creating a release directly as released, got %s", code)
	}
}

// TestReleaseShipSetsReleasedAt proves the rule "release ship requires
// released_at": ShipRelease is the only path to status=released and it
// always stamps released_at in the same write.
func TestReleaseShipSetsReleasedAt(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	product := mustCreateProduct(t, store, ops.CreateProductInput{Name: "听记"})
	release := mustCreateRelease(t, store, ops.CreateReleaseInput{ProductID: product.ID, VersionLabel: "v0.3.0"})

	result, err := store.ShipRelease(ctx, writeCtx("req_ship"), ops.ShipReleaseInput{
		ReleaseID: release.ID, ExpectedVersion: release.Version,
	})
	if err != nil {
		t.Fatalf("ship release: %v", err)
	}
	shipped := result.Data.(*ops.Release)
	if shipped.Status != "released" {
		t.Fatalf("expected status released, got %q", shipped.Status)
	}
	if shipped.ReleasedAt == nil || *shipped.ReleasedAt == "" {
		t.Fatalf("ship must set released_at")
	}
}

// ---------------------------------------------------------------------------
// campaign: end_date must not precede start_date
// ---------------------------------------------------------------------------

func TestCampaignCloseRequiresStartDate(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	campaign := mustCreateCampaign(t, store, ops.CreateCampaignInput{Name: "杭州 developer meetup", ChannelType: "offline"})

	_, err := store.CloseCampaign(ctx, writeCtx("req_close_no_start"), ops.CloseCampaignInput{
		CampaignID: campaign.ID, ExpectedVersion: campaign.Version,
	})
	if code := appErrCode(t, err); code != protocol.CodeBadInput {
		t.Fatalf("expected BAD_INPUT closing a campaign with no start_date, got %s", code)
	}
}

// TestCampaignEndDateMayNotPrecedeStartDate proves the rule directly: closing
// with an end_date before the campaign's own start_date is refused.
func TestCampaignEndDateMayNotPrecedeStartDate(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	campaign := mustCreateCampaign(t, store, ops.CreateCampaignInput{
		Name: "杭州 developer meetup", ChannelType: "offline", StartDate: "2026-09-10",
	})

	_, err := store.CloseCampaign(ctx, writeCtx("req_close_backwards"), ops.CloseCampaignInput{
		CampaignID: campaign.ID, ExpectedVersion: campaign.Version, EndDate: "2026-09-01",
	})
	if code := appErrCode(t, err); code != protocol.CodeBadInput {
		t.Fatalf("expected BAD_INPUT for end_date before start_date, got %s", code)
	}

	result, err := store.CloseCampaign(ctx, writeCtx("req_close_ok"), ops.CloseCampaignInput{
		CampaignID: campaign.ID, ExpectedVersion: campaign.Version, EndDate: "2026-09-12",
	})
	if err != nil {
		t.Fatalf("expected closing with a valid end_date to succeed: %v", err)
	}
	closed := result.Data.(*ops.Campaign)
	if closed.Status != "ended" {
		t.Fatalf("expected status ended, got %q", closed.Status)
	}
}

// TestCampaignCreateEndBeforeStartRejected proves the same rule holds at
// creation time too, before any close is attempted.
func TestCampaignCreateEndBeforeStartRejected(t *testing.T) {
	store := newTestStore(t)
	_, err := store.CreateCampaign(context.Background(), writeCtx("req_create_backwards"), ops.CreateCampaignInput{
		Name: "线下活动", ChannelType: "offline", StartDate: "2026-09-10", EndDate: "2026-09-01",
	})
	if code := appErrCode(t, err); code != protocol.CodeBadInput {
		t.Fatalf("expected BAD_INPUT for end_date before start_date at creation, got %s", code)
	}
}
