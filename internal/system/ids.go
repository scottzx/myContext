package system

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"
)

// NewID returns a prefixed, time-sortable identifier such as
// "task_01J8ZQ...". Prefixes make IDs self-describing in logs and events.
func NewID(prefix string) string {
	id := ulid.MustNew(ulid.Timestamp(time.Now()), rand.Reader)
	if prefix == "" {
		return id.String()
	}
	return prefix + "_" + id.String()
}

// IDPrefix returns the type prefix of an ID, or "" when it has none.
func IDPrefix(id string) string {
	if i := strings.Index(id, "_"); i > 0 {
		return id[:i]
	}
	return ""
}

// PayloadHash fingerprints a command payload so an idempotent retry can be
// told apart from a different request reusing the same request_id.
func PayloadHash(payload any) (string, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// Clock is the time port. Tests inject a fixed clock so golden output is
// reproducible.
type Clock interface {
	Now() time.Time
	Today() string
}

type systemClock struct{}

// NewClock returns the real clock. Today() uses the process local timezone,
// which the CLI sets from the instance config.
func NewClock() Clock { return systemClock{} }

func (systemClock) Now() time.Time { return time.Now() }
func (systemClock) Today() string  { return time.Now().Format(DateLayout) }

// FixedClock is a deterministic clock for tests.
type FixedClock struct{ At time.Time }

func (c FixedClock) Now() time.Time { return c.At }
func (c FixedClock) Today() string  { return c.At.Format(DateLayout) }

// DateLayout is the calendar-day format used everywhere in the protocol.
const DateLayout = "2006-01-02"

// FormatTimestamp renders a timestamp in the protocol's RFC 3339 form.
func FormatTimestamp(t time.Time) string { return t.UTC().Format(time.RFC3339) }
