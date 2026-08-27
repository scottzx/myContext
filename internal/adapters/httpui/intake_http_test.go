package httpui_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"testing/fstest"

	mycontext "github.com/scottzx/mycontext"
	"github.com/scottzx/mycontext/internal/adapters/httpui"
	"github.com/scottzx/mycontext/internal/adapters/sqlite"
	"github.com/scottzx/mycontext/internal/ops"
	"github.com/scottzx/mycontext/internal/protocol"
	"github.com/scottzx/mycontext/internal/system"
)

// The HTTP boundary of the intake flow. What is being tested here is not the
// domain logic - internal/ops covers that - but the two things only this
// adapter can get wrong: that a read-only instance refuses to write at all, and
// that confirming is bound to THIS server's session rather than to anything the
// request body claims.

const httpEvidence = "某科技公司的张老师想做企业内训。\n预算大约两万。\n"

func newWritableServer(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	root := t.TempDir()
	layout := system.NewLayout(root)
	for _, dir := range layout.Dirs() {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("layout: %v", err)
		}
	}
	db, err := sqlite.Open(layout.OpsDB(), sqlite.Options{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	migrations, err := sqlite.LoadMigrations(mycontext.Migrations, mycontext.MigrationsDirOps)
	if err != nil {
		t.Fatalf("load migrations: %v", err)
	}
	if _, err := sqlite.Migrate(context.Background(), db, migrations, "test"); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	server, err := httpui.New(ops.NewStore(db, system.NewClock()),
		fstest.MapFS{"index.html": {Data: []byte("<html></html>")}},
		httpui.Options{CLIVersion: "test", Root: root, Layout: layout, Write: true})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	ts := httptest.NewServer(server.Handler())
	t.Cleanup(ts.Close)
	return ts, server.Token()
}

func post(t *testing.T, ts *httptest.Server, token, path string, body any) (int, protocol.Envelope) {
	t.Helper()
	raw, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", ts.URL+path, bytes.NewReader(raw))
	req.Header.Set("X-Mycontext-Token", token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post %s: %v", path, err)
	}
	defer resp.Body.Close()
	var env protocol.Envelope
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	return resp.StatusCode, env
}

func invoke(t *testing.T, ts *httptest.Server, token, operation, requestID string, input any) (int, protocol.Envelope) {
	t.Helper()
	return post(t, ts, token, "/api/v1/invoke", map[string]any{
		"protocol": protocol.Version, "operation": operation,
		"request_id": requestID, "actor": "user:scott", "input": input,
	})
}

func TestReadOnlyInstanceHidesAndRefusesWrites(t *testing.T) {
	ts, token := newTestServer(t) // built without Write

	req, _ := http.NewRequest("GET", ts.URL+"/api/v1/capabilities", nil)
	req.Header.Set("X-Mycontext-Token", token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("capabilities: %v", err)
	}
	defer resp.Body.Close()
	var caps struct {
		Write      bool     `json:"write"`
		Operations []string `json:"operations"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&caps); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if caps.Write {
		t.Fatal("a read-only instance advertised write")
	}
	for _, op := range caps.Operations {
		if strings.HasPrefix(op, "inbox.capture") || op == "inbox.confirm" {
			t.Fatalf("a read-only instance advertised %q", op)
		}
	}

	status, env := invoke(t, ts, token, "inbox.capture-text", "req_1",
		map[string]any{"schema_version": 1, "text": httpEvidence})
	if status != http.StatusForbidden || env.Error == nil {
		t.Fatalf("a read-only instance accepted a write: %d %+v", status, env)
	}

	status, env = post(t, ts, token, "/api/v1/confirmation-grant", map[string]any{})
	if status != http.StatusForbidden {
		t.Fatalf("a read-only instance issued a grant: %d %+v", status, env)
	}
}

func TestConfirmRequiresThisServersGrant(t *testing.T) {
	ts, token := newWritableServer(t)

	_, env := invoke(t, ts, token, "inbox.capture-text", "req_capture",
		map[string]any{"schema_version": 1, "title": "群聊线索", "text": httpEvidence})
	if !env.OK {
		t.Fatalf("capture: %+v", env.Error)
	}
	captured := env.Data.(map[string]any)
	inboxID := captured["inbox_id"].(string)
	docID := captured["document_id"].(string)

	_, env = invoke(t, ts, token, "inbox.propose", "req_propose", httpProposal(t, inboxID, docID))
	if !env.OK {
		t.Fatalf("propose: %+v", env.Error)
	}
	proposed := env.Data.(map[string]any)
	runID := proposed["run_id"].(string)
	version := int64(proposed["inbox_version"].(float64))

	_, env = invoke(t, ts, token, "inbox.get", "", map[string]any{"id": inboxID})
	if !env.OK {
		t.Fatalf("inbox.get: %+v", env.Error)
	}
	decisions := acceptEverything(t, env.Data)

	// Without a grant, confirm must refuse - the actor string alone proves
	// nothing about whether a person clicked anything.
	status, env := invoke(t, ts, token, "inbox.confirm", "req_nogrant", map[string]any{
		"schema_version": 1, "inbox_id": inboxID, "expected_version": version,
		"active_run_id": runID, "decisions": decisions,
	})
	if status != http.StatusForbidden || env.Error.Code != protocol.CodeGrantInvalid {
		t.Fatalf("confirm without a grant was not refused: %d %+v", status, env.Error)
	}

	// A body that names its own session must not help either: the server uses
	// its own token regardless of what the caller sent.
	_, env = post(t, ts, token, "/api/v1/confirmation-grant", map[string]any{
		"inbox_id": inboxID, "active_run_id": runID,
		"expected_version": version, "decisions": decisions,
	})
	if !env.OK {
		t.Fatalf("grant: %+v", env.Error)
	}
	nonce := env.Data.(map[string]any)["confirmation_nonce"].(string)

	_, env = invoke(t, ts, token, "inbox.confirm", "req_confirm", map[string]any{
		"schema_version": 1, "inbox_id": inboxID, "expected_version": version,
		"active_run_id": runID, "confirmation_nonce": nonce, "decisions": decisions,
		"session_id": "attacker-supplied",
	})
	if !env.OK {
		t.Fatalf("confirm: %+v", env.Error)
	}
	confirmed := env.Data.(map[string]any)
	if confirmed["root_type"] != "opportunity" {
		t.Fatalf("confirm did not route to a case root: %+v", confirmed)
	}

	// The same nonce under a fresh request id must not work twice.
	status, env = invoke(t, ts, token, "inbox.confirm", "req_replay", map[string]any{
		"schema_version": 1, "inbox_id": inboxID, "expected_version": version,
		"active_run_id": runID, "confirmation_nonce": nonce, "decisions": decisions,
	})
	if env.OK {
		t.Fatal("a spent confirmation nonce was accepted again")
	}
	if status != http.StatusForbidden && status != http.StatusConflict {
		t.Fatalf("unexpected status for a reused nonce: %d %+v", status, env.Error)
	}

	// And the workspace can now read the case back through the same API.
	_, env = invoke(t, ts, token, "case.list", "", map[string]any{})
	if !env.OK {
		t.Fatalf("case.list: %+v", env.Error)
	}
	if len(env.Data.([]any)) != 1 {
		t.Fatalf("case.list did not surface the confirmed case: %+v", env.Data)
	}
}

func acceptEverything(t *testing.T, data any) []map[string]any {
	t.Helper()
	detail := data.(map[string]any)
	var out []map[string]any
	for _, key := range []struct{ field, kind string }{
		{"entities", "entity"}, {"facts", "fact"}, {"relations", "relation"}, {"actions", "action"},
	} {
		items, _ := detail[key.field].([]any)
		for _, raw := range items {
			out = append(out, map[string]any{
				"candidate_type": key.kind,
				"candidate_id":   raw.(map[string]any)["candidate_id"],
				"decision":       "accept",
			})
		}
	}
	if len(out) == 0 {
		t.Fatal("the review returned no candidates to decide on")
	}
	return out
}

// httpProposal is the smallest proposal that produces a case root: an account,
// an opportunity that belongs to it, and the project that advances it.
func httpProposal(t *testing.T, inboxID, docID string) map[string]any {
	t.Helper()
	return map[string]any{
		"schema_version": 1, "inbox_id": inboxID, "expected_version": 1,
		"logical_run_key": "proposal-v1", "document_id": docID, "extractor": "test",
		"entities": []any{
			map[string]any{"candidate_id": "entitycand_account", "group_id": "entitygroup_account",
				"entity_type": "account", "intent": "create"},
			map[string]any{"candidate_id": "entitycand_opp", "group_id": "entitygroup_opp",
				"entity_type": "opportunity", "intent": "create"},
		},
		"facts": []any{
			map[string]any{"candidate_id": "fact_account_name", "entity_group_id": "entitygroup_account",
				"field_name": "name", "value": map[string]any{"type": "text", "text": "某科技公司"},
				"source": httpSource(t, docID, "某科技公司")},
			map[string]any{"candidate_id": "fact_opp_name", "entity_group_id": "entitygroup_opp",
				"field_name": "name", "value": map[string]any{"type": "text", "text": "AI 培训"},
				"source": httpSource(t, docID, "企业内训")},
		},
		"relations": []any{
			map[string]any{"candidate_id": "rel_opp_account",
				"from":          map[string]any{"ref": "entity_group", "type": "opportunity", "group_id": "entitygroup_opp"},
				"relation_type": "belongs_to",
				"to":            map[string]any{"ref": "entity_group", "type": "account", "group_id": "entitygroup_account"},
				"source":        httpSource(t, docID, "某科技公司")},
			map[string]any{"candidate_id": "rel_project_opp",
				"from":          map[string]any{"ref": "action_group", "type": "project", "group_id": "actiongroup_project"},
				"relation_type": "advances",
				"to":            map[string]any{"ref": "entity_group", "type": "opportunity", "group_id": "entitygroup_opp"},
				"source":        httpSource(t, docID, "预算大约两万")},
		},
		"actions": []any{
			map[string]any{"candidate_id": "action_project", "group_id": "actiongroup_project",
				"action_type": "project",
				"draft":       map[string]any{"name": "AI 培训售前推进", "importance": "P1"},
				"source":      httpSource(t, docID, "预算大约两万")},
		},
	}
}

func httpSource(t *testing.T, docID, needle string) map[string]any {
	t.Helper()
	start := strings.Index(httpEvidence, needle)
	if start < 0 {
		t.Fatalf("evidence does not contain %q", needle)
	}
	end := start + len(needle)
	sum := sha256.Sum256([]byte(httpEvidence[start:end]))
	return map[string]any{"document_id": docID, "locator": map[string]any{
		"schema": 1, "type": "text", "start_byte": start, "end_byte": end,
		"quote_sha256": hex.EncodeToString(sum[:]),
	}}
}
