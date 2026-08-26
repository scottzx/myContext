package sqlite_test

import (
	"path/filepath"
	"testing"

	"github.com/scottzx/mycontext/internal/adapters/sqlite"
)

// These tests answer a blocking prerequisite for full-text search (design
// doc §8.2): does modernc.org/sqlite (our CGo-free driver) support FTS5, and
// does the chosen tokenizer actually find Chinese substrings? The corpus is
// overwhelmingly Chinese, so a tokenizer that cannot do this makes the whole
// feature useless regardless of whether FTS5 itself is present.

// chineseCorpus mirrors real records (客户档案, 会议纪要, 内容稿): long,
// unsegmented runs of Han characters with the target terms embedded
// mid-sentence, never isolated as their own "word".
var chineseCorpus = []string{
	"客户档案：杨总来自云深处科技有限公司，主营AI转型咨询服务。",
	"会议纪要2024年3月：讨论了数据标注流程优化，杨总提出新的质检方案。",
	"内容稿草稿：企业AI转型的三个阶段，从工具化到组织能力重塑。",
	"云深处团队本周完成了客户数据标注任务的交付，进度符合预期。",
	"备忘：与财务部门核对本月报销，与本次全文检索无关的一条记录。",
}

func openTestDB(t *testing.T) *sqlite.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fts.db")
	db, err := sqlite.Open(path, sqlite.Options{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// TestFTS5Available establishes that modernc.org/sqlite, at the version
// pinned in go.mod, was built with FTS5 compiled in and that virtual tables
// can be created through the driver this project actually uses (not just in
// a throwaway probe).
func TestFTS5Available(t *testing.T) {
	db := openTestDB(t)
	if _, err := db.SQL().Exec(`CREATE VIRTUAL TABLE fts5_probe USING fts5(body)`); err != nil {
		t.Fatalf("FTS5 is not available in this driver build: %v", err)
	}
}

// TestFTS5UnicodeTokenizerDoesNotSegmentChinese documents *why* the plain
// unicode61 tokenizer (FTS5's default) is unusable for this corpus: it does
// not word-segment Han text, so it treats a whole run of Chinese characters
// as a single token. A query for a substring that appears in the middle of
// such a run finds nothing, even though the text is plainly present.
func TestFTS5UnicodeTokenizerDoesNotSegmentChinese(t *testing.T) {
	db := openTestDB(t)
	if _, err := db.SQL().Exec(`CREATE VIRTUAL TABLE t_unicode61 USING fts5(body, tokenize='unicode61')`); err != nil {
		t.Fatalf("create fts5 table: %v", err)
	}
	for _, doc := range chineseCorpus {
		if _, err := db.SQL().Exec(`INSERT INTO t_unicode61(body) VALUES (?)`, doc); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}

	for _, term := range []string{"云深处", "杨总", "数据标注", "AI转型"} {
		var n int
		if err := db.SQL().QueryRow(
			`SELECT count(*) FROM t_unicode61 WHERE t_unicode61 MATCH ?`, `"`+term+`"`,
		).Scan(&n); err != nil {
			t.Fatalf("match query for %q: %v", term, err)
		}
		if n != 0 {
			t.Fatalf("unicode61 unexpectedly found %q (%d rows); if this now passes, "+
				"the driver's Chinese handling has changed and §8.2 should be revisited", term, n)
		}
	}
}

// TestFTS5TrigramFindsChineseSubstrings is the actual recommendation: the
// 'trigram' tokenizer indexes overlapping 3-character n-grams regardless of
// script, so it finds Chinese substrings embedded in longer sentences
// without needing a word segmenter. This is a regression test for that
// capability using real corpus-shaped strings.
func TestFTS5TrigramFindsChineseSubstrings(t *testing.T) {
	db := openTestDB(t)
	if _, err := db.SQL().Exec(`CREATE VIRTUAL TABLE documents_fts USING fts5(body, tokenize='trigram')`); err != nil {
		t.Fatalf("create fts5 trigram table: %v", err)
	}
	for _, doc := range chineseCorpus {
		if _, err := db.SQL().Exec(`INSERT INTO documents_fts(body) VALUES (?)`, doc); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}

	cases := []struct {
		term    string
		wantMin int
	}{
		{"云深处", 2},   // appears in corpus[0] and corpus[3]
		{"数据标注", 2},  // appears in corpus[1] and corpus[3]
		{"AI转型", 2},  // appears in corpus[0] and corpus[2]
		{"不存在的词", 0}, // absent term must not match
	}
	for _, tc := range cases {
		var n int
		if err := db.SQL().QueryRow(
			`SELECT count(*) FROM documents_fts WHERE documents_fts MATCH ?`, `"`+tc.term+`"`,
		).Scan(&n); err != nil {
			t.Fatalf("match query for %q: %v", tc.term, err)
		}
		if n < tc.wantMin {
			t.Fatalf("trigram search for %q: got %d matches, want at least %d", tc.term, n, tc.wantMin)
		}
		if tc.wantMin == 0 && n != 0 {
			t.Fatalf("trigram search for %q: expected no matches, got %d", tc.term, n)
		}
	}
}

// TestFTS5TrigramRequiresThreeCharacters documents a sharp edge in the
// trigram tokenizer that a naive implementation would trip over: it cannot
// match a query shorter than three Unicode characters, because it can form
// no complete trigram from it. Two-character Chinese terms are common
// (surnames like 杨总/李总, abbreviations like AI等), so callers must fall
// back to a LIKE scan for short queries rather than relying on MATCH alone.
func TestFTS5TrigramRequiresThreeCharacters(t *testing.T) {
	db := openTestDB(t)
	if _, err := db.SQL().Exec(`CREATE VIRTUAL TABLE documents_fts USING fts5(body, tokenize='trigram')`); err != nil {
		t.Fatalf("create fts5 trigram table: %v", err)
	}
	for _, doc := range chineseCorpus {
		if _, err := db.SQL().Exec(`INSERT INTO documents_fts(body) VALUES (?)`, doc); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}

	// "杨总" is genuinely present (corpus[0], corpus[1]) but is only two
	// characters, so MATCH must report zero rows: this is a known trigram
	// limitation, not a bug in our schema or driver.
	const shortTerm = "杨总"
	var viaMatch int
	if err := db.SQL().QueryRow(
		`SELECT count(*) FROM documents_fts WHERE documents_fts MATCH ?`, `"`+shortTerm+`"`,
	).Scan(&viaMatch); err != nil {
		t.Fatalf("match query for %q: %v", shortTerm, err)
	}
	if viaMatch != 0 {
		t.Fatalf("expected trigram MATCH to miss the two-character term %q (got %d); "+
			"if this now matches, the short-query fallback may no longer be needed", shortTerm, viaMatch)
	}

	// The LIKE fallback the application must use for <3-character queries
	// does find it, confirming the workaround is viable.
	var viaLike int
	if err := db.SQL().QueryRow(
		`SELECT count(*) FROM documents_fts WHERE body LIKE ?`, "%"+shortTerm+"%",
	).Scan(&viaLike); err != nil {
		t.Fatalf("like query for %q: %v", shortTerm, err)
	}
	if viaLike < 2 {
		t.Fatalf("LIKE fallback for %q: got %d matches, want at least 2", shortTerm, viaLike)
	}
}
