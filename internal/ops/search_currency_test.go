package ops_test

import (
	"context"
	"testing"
	"time"

	"github.com/scottzx/mycontext/internal/ops"
	"github.com/scottzx/mycontext/internal/system"
)

// steppingClock advances one minute per reading. Document versions are ordered
// by created_at, so a frozen clock would make two versions of one lineage tie -
// a situation real use does not produce and a test should not depend on the
// resolution of.
type steppingClock struct {
	at   time.Time
	step time.Duration
}

func (c *steppingClock) Now() time.Time {
	c.at = c.at.Add(c.step)
	return c.at
}

func (c *steppingClock) Today() string { return c.at.Format(system.DateLayout) }

const (
	shaOriginal = "1111111111111111111111111111111111111111111111111111111111111111"
	shaReplaced = "2222222222222222222222222222222222222222222222222222222222222222"
)

func mustAddDocument(t *testing.T, store *ops.Store, in ops.AddDocumentInput) string {
	t.Helper()
	res, err := store.AddDocument(context.Background(), writeCtx(system.NewID("req")), in)
	if err != nil {
		t.Fatalf("add document %q: %v", in.Title, err)
	}
	return res.Data.(*ops.Document).ID
}

func mustIndex(t *testing.T, store *ops.Store, docID, body string) {
	t.Helper()
	if _, err := store.IndexDocument(context.Background(), writeCtx(system.NewID("req")),
		ops.IndexDocumentInput{DocumentID: docID, Body: body}); err != nil {
		t.Fatalf("index %s: %v", docID, err)
	}
}

func hitByID(t *testing.T, res *ops.DocumentSearchResult, docID string) ops.DocumentHit {
	t.Helper()
	for _, h := range res.Hits {
		if h.DocID == docID {
			return h
		}
	}
	t.Fatalf("search for %q returned no hit for %s (%d hits)", res.Query, docID, len(res.Hits))
	return ops.DocumentHit{}
}

// A lineage keeps every version, and every version matches the same words. The
// whole point of the search result carrying currency is that an agent reading
// the hits can tell the replaced proposal from the one that replaced it -
// without which it will happily answer from a conclusion that was overturned.
//
// Both versions are still returned: hiding the old one would be the silent
// overwrite this system refuses everywhere else, and a superseded document is
// still evidence of what was decided when.
func TestSearchMarksSupersededVersions(t *testing.T) {
	store := newTestStoreWithClock(t, &steppingClock{at: fixedNow, step: time.Minute})
	ctx := context.Background()

	v1 := mustAddDocument(t, store, ops.AddDocumentInput{
		Kind: "proposal", Title: "云深处报价方案 v1",
		RelPath: "2026/08/18/cap_a/original/v1.md", FileRole: "original", SHA256: shaOriginal,
	})
	mustIndex(t, store, v1, "云深处数据标注项目报价：按条计费，单价 1.2 元。")

	v2 := mustAddDocument(t, store, ops.AddDocumentInput{
		Kind: "proposal", Title: "云深处报价方案 v2",
		RelPath: "2026/08/20/cap_b/original/v2.md", FileRole: "original", SHA256: shaReplaced,
		SupersedesID: v1, ChangeNote: "改为按人天计费",
	})
	mustIndex(t, store, v2, "云深处数据标注项目报价：按人天计费，单价 800 元。")

	res, err := store.SearchDocuments(ctx, "数据标注", 0)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(res.Hits) != 2 {
		t.Fatalf("both versions should match, got %d hits", len(res.Hits))
	}
	if res.SupersededHits != 1 {
		t.Fatalf("superseded_hits = %d, want 1", res.SupersededHits)
	}

	old := hitByID(t, res, v1)
	if old.IsCurrent {
		t.Error("the superseded version is reported as current")
	}
	if old.SupersededBy != v2 {
		t.Errorf("superseded_by = %q, want %q: a replaced hit has to say what to read instead",
			old.SupersededBy, v2)
	}

	current := hitByID(t, res, v2)
	if !current.IsCurrent {
		t.Error("the newest version of the lineage is not reported as current")
	}
	if current.SupersededBy != "" {
		t.Errorf("superseded_by = %q on the current version, want empty", current.SupersededBy)
	}
}

// A document that was never superseded can still be out of date - review_at is
// the document saying so itself. Being current and being due for review are
// independent, so they are reported independently.
func TestSearchMarksDocumentsDueForReview(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	due := mustAddDocument(t, store, ops.AddDocumentInput{
		Kind: "decision", Title: "定价口径（去年定的）",
		ReviewAt: "2026-07-01",
		RelPath:  "2026/07/01/cap_c/original/d.md", FileRole: "original", SHA256: shaOriginal,
	})
	mustIndex(t, store, due, "定价口径：数据标注按条计费。")

	later := mustAddDocument(t, store, ops.AddDocumentInput{
		Kind: "decision", Title: "交付口径（明年再看）",
		ReviewAt: "2026-12-01",
		RelPath:  "2026/08/01/cap_d/original/d.md", FileRole: "original", SHA256: shaReplaced,
	})
	mustIndex(t, store, later, "交付口径：数据标注分批验收。")

	res, err := store.SearchDocuments(ctx, "数据标注", 0)
	if err != nil {
		t.Fatalf("search: %v", err)
	}

	past := hitByID(t, res, due)
	if !past.ReviewDue {
		t.Errorf("review_at %s is before today %s but review_due is false", past.ReviewAt, today)
	}
	if past.ReviewAt != "2026-07-01" {
		t.Errorf("review_at = %q, want 2026-07-01", past.ReviewAt)
	}
	if !past.IsCurrent {
		t.Error("a document due for review is still the current version of its lineage")
	}

	future := hitByID(t, res, later)
	if future.ReviewDue {
		t.Errorf("review_at %s is after today %s but review_due is true", future.ReviewAt, today)
	}
}

// The index owns a private copy of the text, so nothing about the file itself
// tells you the copy is still right. Once the original changes, search keeps
// serving text that is provably not the document's - silently, until this
// queue says so.
func TestReindexQueueDetectsChangedOriginal(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	docID := mustAddDocument(t, store, ops.AddDocumentInput{
		Kind: "report", Title: "月度经营报告",
		RelPath: "2026/08/01/cap_e/original/report.md", FileRole: "original", SHA256: shaOriginal,
	})
	mustIndex(t, store, docID, "八月经营报告：数据标注收入环比上升。")

	queue, err := store.DocumentsNeedingIndex(ctx)
	if err != nil {
		t.Fatalf("index queue: %v", err)
	}
	if len(queue) != 0 {
		t.Fatalf("a freshly indexed document should not be queued, got %+v", queue)
	}

	// Stands in for the original being replaced out of band - a re-capture, or
	// the file changing under the library. There is no API for it precisely
	// because versions are meant to be immutable; that is exactly why the
	// index cannot rely on being told.
	if _, err := store.DB().SQL().ExecContext(ctx,
		`UPDATE document_files SET sha256 = ? WHERE doc_id = ? AND role = 'original'`,
		shaReplaced, docID); err != nil {
		t.Fatalf("replace original hash: %v", err)
	}

	queue, err = store.DocumentsNeedingIndex(ctx)
	if err != nil {
		t.Fatalf("index queue after change: %v", err)
	}
	if len(queue) != 1 || queue[0].DocID != docID {
		t.Fatalf("the changed document should be queued, got %+v", queue)
	}
	if queue[0].Reason != ops.IndexReasonContentChanged {
		t.Fatalf("reason = %q, want %q", queue[0].Reason, ops.IndexReasonContentChanged)
	}
	if queue[0].IndexedSHA256 != shaOriginal || queue[0].CurrentSHA256 != shaReplaced {
		t.Errorf("hashes = (%s, %s), want (%s, %s): the report has to be checkable "+
			"against the files rather than believed",
			queue[0].IndexedSHA256, queue[0].CurrentSHA256, shaOriginal, shaReplaced)
	}

	// Re-indexing is the fix, so it must clear the row.
	mustIndex(t, store, docID, "八月经营报告（修订）：数据标注收入环比持平。")
	queue, err = store.DocumentsNeedingIndex(ctx)
	if err != nil {
		t.Fatalf("index queue after reindex: %v", err)
	}
	if len(queue) != 0 {
		t.Fatalf("re-indexing should clear the queue, got %+v", queue)
	}
}

// Bodies indexed before this table existed have no hash behind them. They are
// reported as unverifiable rather than assumed fine - and separately from a
// document whose file demonstrably changed, because the two justify different
// levels of doubt.
func TestReindexQueueFlagsUnknownProvenance(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	docID := mustAddDocument(t, store, ops.AddDocumentInput{
		Kind: "meeting_note", Title: "8/18 沟通纪要",
		RelPath: "2026/08/18/cap_f/original/notes.md", FileRole: "original", SHA256: shaOriginal,
	})
	mustIndex(t, store, docID, "会议纪要：确认数据标注的下一步。")

	// What a database migrated from before 011 looks like: indexed text, no
	// record of where it came from.
	if _, err := store.DB().SQL().ExecContext(ctx,
		`DELETE FROM document_index_state WHERE doc_id = ?`, docID); err != nil {
		t.Fatalf("drop index state: %v", err)
	}

	queue, err := store.DocumentsNeedingIndex(ctx)
	if err != nil {
		t.Fatalf("index queue: %v", err)
	}
	if len(queue) != 1 || queue[0].DocID != docID {
		t.Fatalf("indexed text with no recorded provenance should be queued, got %+v", queue)
	}
	if queue[0].Reason != ops.IndexReasonProvenanceUnknown {
		t.Fatalf("reason = %q, want %q", queue[0].Reason, ops.IndexReasonProvenanceUnknown)
	}
}

// A queue entry no action can clear is worse than silence: it trains the
// reader to ignore the queue. An original file registered without a hash of
// its own can never be compared, so it is a hashing gap and does not belong
// here - re-indexing it would only record another null.
func TestReindexQueueIgnoresOriginalsWithoutHash(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	docID := mustAddDocument(t, store, ops.AddDocumentInput{
		Kind: "other", Title: "手工登记的旧文件",
		RelPath: "2026/08/18/cap_g/original/legacy.md", FileRole: "original",
	})
	mustIndex(t, store, docID, "旧文件正文：数据标注历史记录。")

	queue, err := store.DocumentsNeedingIndex(ctx)
	if err != nil {
		t.Fatalf("index queue: %v", err)
	}
	if len(queue) != 0 {
		t.Fatalf("an original with no hash cannot be checked and must not be queued, got %+v", queue)
	}
}
