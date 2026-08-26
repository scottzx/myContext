package ops_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/scottzx/mycontext/internal/ops"
)

// contractDTOs pairs a TS interface name (in web/src/types.ts) with a filled
// Go value that marshals every field the wire actually sends. This is the
// tracked follow-up named in types.ts: until schemas/catalog.json describes
// response shapes, this test is what stops a Go json tag from drifting away
// from the hand-written TypeScript silently (the hard_due_at/entity_id/
// entity_type drift that used to blank the dashboard - see OverdueTable.tsx).
var contractDTOs = map[string]any{
	"AgendaEntry":       ops.AgendaEntry{},
	"DayLoad":           ops.DayLoad{},
	"OverdueEntry":      ops.OverdueEntry{},
	"QualityIssue":      ops.QualityIssue{},
	"Totals":            ops.Totals{},
	"Status":            ops.Status{},
	"MilestoneProgress": ops.MilestoneProgress{},
	"Area":              ops.Area{},
	"Initiative":        ops.Initiative{},
	// ProjectSummary embeds *Project; a nil embedded pointer makes
	// encoding/json drop the promoted fields entirely, so it must be given a
	// real Project to marshal its full wire shape.
	"ProjectSummary": ops.ProjectSummary{Project: &ops.Project{}},
	"TreeInitiative": ops.TreeInitiative{},
	"TreeArea":       ops.TreeArea{},
}

// TestTypesTSMatchesWireShape fails if any field the TypeScript in
// web/src/types.ts declares for one of the DTOs above is absent from what
// the Go struct actually puts on the wire - the exact shape of bug that
// leaves a table reading `undefined` off a renamed field with no compiler
// error to catch it.
//
// It is intentionally one-directional: a Go DTO is allowed to carry fields
// the frontend does not need (several already do, e.g. ProjectSummary),
// but every field the frontend *does* declare must exist on the wire.
func TestTypesTSMatchesWireShape(t *testing.T) {
	tsFields := parseTSInterfaceFields(t, typesTSPath(t))

	for name, value := range contractDTOs {
		wireFields := jsonFieldNames(t, name, value)
		declared, ok := tsFields[name]
		if !ok {
			t.Errorf("web/src/types.ts: no `export interface %s` found - update contractDTOs or the TS file", name)
			continue
		}
		var missing []string
		for _, field := range declared {
			if !wireFields[field] {
				missing = append(missing, field)
			}
		}
		if len(missing) > 0 {
			sort.Strings(missing)
			t.Errorf("web/src/types.ts interface %s declares field(s) %v, but the Go DTO (internal/ops) "+
				"does not send them on the wire - the JSON tag was likely renamed", name, missing)
		}
	}
}

// jsonFieldNames marshals value and returns the set of top-level JSON keys
// it produces.
func jsonFieldNames(t *testing.T, name string, value any) map[string]bool {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal %s: %v", name, err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal %s: %v", name, err)
	}
	out := make(map[string]bool, len(m))
	for k := range m {
		out[k] = true
	}
	return out
}

// typesTSPath finds web/src/types.ts relative to this test file, so the test
// works regardless of the directory `go test` is invoked from.
func typesTSPath(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not determine test file location")
	}
	return filepath.Join(filepath.Dir(thisFile), "..", "..", "web", "src", "types.ts")
}

var (
	tsInterfaceRE = regexp.MustCompile(`^export interface (\w+)`)
	tsFieldRE     = regexp.MustCompile(`^\s*([A-Za-z_][A-Za-z0-9_]*)\??:\s*.+;\s*$`)
)

// parseTSInterfaceFields extracts, for every `export interface Name { ... }`
// block in src, the field names declared at the top level of that interface.
// It is a plain line scanner, not a TypeScript parser: it tracks brace depth
// only well enough to ignore fields of a nested inline object type (like
// Envelope.meta), which is all these DTOs need.
func parseTSInterfaceFields(t *testing.T, path string) map[string][]string {
	t.Helper()
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}

	out := map[string][]string{}
	current := ""
	depth := 0
	for _, line := range strings.Split(string(src), "\n") {
		if m := tsInterfaceRE.FindStringSubmatch(line); m != nil {
			current = m[1]
			out[current] = nil
			depth = 0
		}
		if current == "" {
			continue
		}
		depth += strings.Count(line, "{") - strings.Count(line, "}")
		if depth == 1 {
			if m := tsFieldRE.FindStringSubmatch(line); m != nil {
				out[current] = append(out[current], m[1])
			}
		}
		if depth == 0 {
			current = ""
		}
	}
	return out
}
