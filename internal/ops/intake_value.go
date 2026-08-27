package ops

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/scottzx/mycontext/internal/protocol"
)

// The V1 value and locator schemas (design §4 "V1 Source Locator 与 Value Schema").
//
// A candidate value is TYPED before it is ever stored: the extractor states
// what kind of thing it found, and the registry states what kind that field
// accepts. Without that, "20000" is indistinguishable from "20,000 CNY,
// approximately", and the difference is the whole reason a human is reviewing.

// LocatorSchemaVersion is the only locator shape V1 accepts. Bumping it is a
// breaking change to every stored attribution, so it is a constant, not a range.
const LocatorSchemaVersion = 1

// SourceLocator points at the exact bytes a claim came from.
//
// Offsets are into the sealed original's UTF-8 bytes, and quote_sha256 pins
// what was actually there. Re-hashing before confirm is what turns "the model
// said the customer is X" into "these 48 bytes of the document say X" - and if
// the bytes ever fail to match, the honest answer is to refuse, not to write a
// fact whose evidence cannot be reproduced.
type SourceLocator struct {
	Schema      int    `json:"schema"`
	Type        string `json:"type"`
	StartByte   int64  `json:"start_byte"`
	EndByte     int64  `json:"end_byte"`
	QuoteSHA256 string `json:"quote_sha256"`
}

func (l SourceLocator) validate(path string) error {
	if l.Schema != LocatorSchemaVersion {
		return protocol.Review(protocol.CodeBadInput,
			fmt.Sprintf("locator schema must be %d", LocatorSchemaVersion),
			map[string]any{"path": path})
	}
	if l.Type != "text" {
		return protocol.Review(protocol.CodeBadInput,
			"V1 only supports text locators", map[string]any{"path": path})
	}
	if l.StartByte < 0 || l.EndByte <= l.StartByte {
		return protocol.Review(protocol.CodeBadInput,
			"locator end_byte must be greater than start_byte",
			map[string]any{"path": path})
	}
	if len(l.QuoteSHA256) != 64 || l.QuoteSHA256 != strings.ToLower(l.QuoteSHA256) {
		return protocol.Review(protocol.CodeBadInput,
			"locator quote_sha256 must be a lowercase sha-256 hex digest",
			map[string]any{"path": path})
	}
	return nil
}

// verifyAgainst re-reads the quoted range and compares its digest. A mismatch
// is SOURCE_CHANGED rather than a generic failure, because the caller's correct
// response is to re-extract, not to retry.
func (l SourceLocator) verifyAgainst(original []byte, path string) error {
	if l.EndByte > int64(len(original)) {
		return protocol.Review(protocol.CodeSourceChanged,
			"locator points past the end of the original",
			map[string]any{"path": path, "original_bytes": len(original)})
	}
	sum := sha256.Sum256(original[l.StartByte:l.EndByte])
	if got := hex.EncodeToString(sum[:]); got != l.QuoteSHA256 {
		return protocol.Review(protocol.CodeSourceChanged,
			"the quoted range no longer hashes to the recorded value",
			map[string]any{"path": path, "expected": l.QuoteSHA256, "actual": got})
	}
	return nil
}

// CandidateValue is one proposed field value in its declared type. The zero
// value is not meaningful; always build it through parseCandidateValue.
type CandidateValue struct {
	Type      string   `json:"type"`
	Text      string   `json:"text,omitempty"`
	Number    *float64 `json:"number,omitempty"`
	Qualifier string   `json:"qualifier,omitempty"`
	ISO       string   `json:"iso,omitempty"`
	Precision string   `json:"precision,omitempty"`
	RFC3339   string   `json:"rfc3339,omitempty"`
	Boolean   *bool    `json:"boolean,omitempty"`
	Amount    *float64 `json:"amount,omitempty"`
	Currency  string   `json:"currency,omitempty"`
}

var validValueQualifier = map[string]bool{"exact": true, "approx": true}

// parse checks the payload matches its own declared type. It is deliberately
// strict about the fields it does NOT expect: a value carrying both `text` and
// `amount` means the extractor is unsure what it found, and guessing which one
// the user meant is exactly the class of error this layer exists to prevent.
func (v *CandidateValue) parse(path string) error {
	bad := func(msg string) error {
		return protocol.Review(protocol.CodeBadInput, msg, map[string]any{"path": path})
	}
	switch v.Type {
	case "text":
		if strings.TrimSpace(v.Text) == "" {
			return bad("text value requires a non-empty text")
		}
		v.Text = strings.TrimSpace(v.Text)
	case "number":
		if v.Number == nil {
			return bad("number value requires number")
		}
		if v.Qualifier == "" {
			v.Qualifier = "exact"
		}
		if !validValueQualifier[v.Qualifier] {
			return bad("qualifier must be exact or approx")
		}
	case "date":
		if err := ValidateDate("iso", v.ISO); err != nil {
			return bad("date value requires iso as YYYY-MM-DD")
		}
		if v.Precision == "" {
			v.Precision = "day"
		}
		if v.Precision != "day" {
			return bad("V1 date precision must be day")
		}
	case "timestamp":
		t, err := time.Parse(time.RFC3339, v.RFC3339)
		if err != nil {
			return bad("timestamp value requires rfc3339")
		}
		v.RFC3339 = t.Format(time.RFC3339)
	case "boolean":
		if v.Boolean == nil {
			return bad("boolean value requires boolean")
		}
	case "money":
		if v.Amount == nil {
			return bad("money value requires amount")
		}
		if v.Currency == "" {
			v.Currency = "CNY"
		}
		// The amount columns this maps onto carry no currency of their own, so
		// storing a EUR figure there would silently claim it was CNY.
		if v.Currency != "CNY" {
			return protocol.Review(protocol.CodeUnsupportedValue,
				"V1 only supports CNY amounts", map[string]any{"path": path, "currency": v.Currency})
		}
		if v.Qualifier == "" {
			v.Qualifier = "exact"
		}
		if !validValueQualifier[v.Qualifier] {
			return bad("qualifier must be exact or approx")
		}
	default:
		return bad("value type must be text|number|date|timestamp|boolean|money")
	}
	return nil
}

// normalized is the canonical string form used for the attribution hash. It is
// what makes "is this attribution still current" answerable: the same value
// written twice must hash the same, and a different value must not.
func (v CandidateValue) normalized() string {
	switch v.Type {
	case "text":
		return v.Text
	case "number":
		return strconv.FormatFloat(*v.Number, 'g', -1, 64)
	case "date":
		return v.ISO
	case "timestamp":
		return v.RFC3339
	case "boolean":
		if *v.Boolean {
			return "true"
		}
		return "false"
	case "money":
		return strconv.FormatFloat(*v.Amount, 'g', -1, 64) + " " + v.Currency
	}
	return ""
}

func (v CandidateValue) hash() string { return hashString(v.normalized()) }

func hashString(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func mustJSON(v any) string {
	raw, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(raw)
}
