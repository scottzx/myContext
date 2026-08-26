package httpui_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"testing/fstest"

	mycontext "github.com/scottzx/mycontext"
	"github.com/scottzx/mycontext/internal/adapters/httpui"
	"github.com/scottzx/mycontext/internal/adapters/sqlite"
	"github.com/scottzx/mycontext/internal/ops"
	"github.com/scottzx/mycontext/internal/protocol"
	"github.com/scottzx/mycontext/internal/system"
)

// newTestServer boots a real httpui.Server against a migrated, seeded ops.db
// and returns a handler you can hit with httptest — no network socket needed
// for these tests, which keeps them fast and avoids port collisions.
func newTestServer(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ops.db")
	db, err := sqlite.Open(path, sqlite.Options{})
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
	store := ops.NewStore(db, system.NewClock())

	// A minimal in-memory filesystem stands in for the embedded frontend;
	// these tests exercise the API surface, not the static file serving.
	assets := fstest.MapFS{"index.html": {Data: []byte("<html></html>")}}

	server, err := httpui.New(store, assets, httpui.Options{
		CLIVersion: "test", Root: "/test/root",
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	// Serve() owns the listener lifecycle for real usage; for tests we drive
	// the same mux directly via httptest so we can assert on responses
	// without a background goroutine or a real TCP port.
	ts := httptest.NewServer(server.Handler())
	t.Cleanup(ts.Close)
	return ts, server.Token()
}

func TestCapabilitiesRequiresToken(t *testing.T) {
	ts, token := newTestServer(t)

	resp, err := http.Get(ts.URL + "/api/v1/capabilities")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no token: status %d, want 401", resp.StatusCode)
	}

	req, _ := http.NewRequest("GET", ts.URL+"/api/v1/capabilities", nil)
	req.Header.Set("X-Mycontext-Token", token)
	resp2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("get with token: %v", err)
	}
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("with token: status %d, want 200", resp2.StatusCode)
	}

	var caps struct {
		Read       bool     `json:"read"`
		Write      bool     `json:"write"`
		Operations []string `json:"operations"`
	}
	json.NewDecoder(resp2.Body).Decode(&caps)
	if !caps.Read || caps.Write {
		t.Fatalf("expected read-only capabilities, got %+v", caps)
	}
	if len(caps.Operations) == 0 {
		t.Fatal("capabilities lists no operations")
	}
}

func TestCapabilitiesRejectsWrongToken(t *testing.T) {
	ts, _ := newTestServer(t)
	req, _ := http.NewRequest("GET", ts.URL+"/api/v1/capabilities", nil)
	req.Header.Set("X-Mycontext-Token", "not-the-real-token")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status %d, want 401", resp.StatusCode)
	}
}

func TestInvokeRejectsForeignOrigin(t *testing.T) {
	ts, token := newTestServer(t)
	body, _ := json.Marshal(map[string]any{"operation": "ops.status", "input": map[string]any{}})
	req, _ := http.NewRequest("POST", ts.URL+"/api/v1/invoke", bytes.NewReader(body))
	req.Header.Set("X-Mycontext-Token", token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "http://evil.example.com")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status %d, want 403", resp.StatusCode)
	}
}

func TestInvokeOpsStatus(t *testing.T) {
	ts, token := newTestServer(t)
	body, _ := json.Marshal(map[string]any{
		"protocol": "mycontext-invoke/v1", "operation": "ops.status",
		"request_id": "req_1", "actor": "ui", "input": map[string]any{},
	})
	req, _ := http.NewRequest("POST", ts.URL+"/api/v1/invoke", bytes.NewReader(body))
	req.Header.Set("X-Mycontext-Token", token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d, want 200", resp.StatusCode)
	}
	var env protocol.Envelope
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !env.OK || env.Command != "ops.status" {
		t.Fatalf("unexpected envelope: %+v", env)
	}
	// The response must actually contain the Status shape, not just succeed.
	data, ok := env.Data.(map[string]any)
	if !ok {
		t.Fatalf("data is %T, want an object", env.Data)
	}
	for _, field := range []string{"today", "today_load", "today_agenda", "week", "totals"} {
		if _, ok := data[field]; !ok {
			t.Fatalf("ops.status response is missing %q: %v", field, data)
		}
	}
}

// TestInvokeEveryWhitelistedOperation asserts every operation the
// capabilities endpoint advertises is actually reachable through invoke and
// returns ok:true against a freshly migrated (empty) ops.db - an empty list
// is a correct read-only answer, a 500 is not.
func TestInvokeEveryWhitelistedOperation(t *testing.T) {
	ts, token := newTestServer(t)

	capReq, _ := http.NewRequest("GET", ts.URL+"/api/v1/capabilities", nil)
	capReq.Header.Set("X-Mycontext-Token", token)
	capResp, err := http.DefaultClient.Do(capReq)
	if err != nil {
		t.Fatalf("capabilities: %v", err)
	}
	var caps struct {
		Operations []string `json:"operations"`
	}
	if err := json.NewDecoder(capResp.Body).Decode(&caps); err != nil {
		t.Fatalf("decode capabilities: %v", err)
	}
	if len(caps.Operations) == 0 {
		t.Fatal("capabilities lists no operations")
	}

	for _, op := range caps.Operations {
		t.Run(op, func(t *testing.T) {
			body, _ := json.Marshal(map[string]any{"operation": op, "input": map[string]any{}})
			req, _ := http.NewRequest("POST", ts.URL+"/api/v1/invoke", bytes.NewReader(body))
			req.Header.Set("X-Mycontext-Token", token)
			req.Header.Set("Content-Type", "application/json")

			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("post: %v", err)
			}
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status %d, want 200", resp.StatusCode)
			}
			var env protocol.Envelope
			if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if !env.OK {
				t.Fatalf("operation %q did not return ok:true: %+v", op, env.Error)
			}
		})
	}
}

func TestInvokeRejectsUnknownOperation(t *testing.T) {
	ts, token := newTestServer(t)
	body, _ := json.Marshal(map[string]any{"operation": "task.create", "input": map[string]any{}})
	req, _ := http.NewRequest("POST", ts.URL+"/api/v1/invoke", bytes.NewReader(body))
	req.Header.Set("X-Mycontext-Token", token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", resp.StatusCode)
	}
	var env protocol.Envelope
	json.NewDecoder(resp.Body).Decode(&env)
	if env.OK || env.Error == nil || env.Error.Code != protocol.CodeBadInput {
		t.Fatalf("unexpected envelope: %+v", env)
	}
}

func TestStaticAssetsAreServed(t *testing.T) {
	ts, _ := newTestServer(t)
	resp, err := http.Get(ts.URL + "/index.html")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d, want 200 (static assets need no token)", resp.StatusCode)
	}
}
