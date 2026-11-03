package files

import (
	"strings"
	"testing"
)

// The byte cap can cut the rclone answer anywhere; every cut is a truncated
// listing of the entries read so far, never an error and never "complete".
func TestDecodeListingByteCap(t *testing.T) {
	e1 := `{"Name":"a","IsDir":false,"Size":1}`
	e2 := `{"Name":"b","IsDir":true,"Size":2}`
	body := `{"list":[` + e1 + `,` + e2 + `]}`
	cases := []struct {
		name  string
		cut   string // body prefix that fits the cap
		count int
	}{
		{"mid entry", `{"list":[` + e1 + `,{"Name":"b"`, 1},
		{"entry boundary", `{"list":[` + e1, 1},
		{"after comma", `{"list":[` + e1 + `,`, 1},
		{"before closing bracket", `{"list":[` + e1 + `,` + e2, 2},
		{"before any entry", `{"list":[`, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := decodeListing(strings.NewReader(body), int64(len(tc.cut)))
			if err != nil || !got.Truncated || len(got.Entries) != tc.count {
				t.Fatalf("got %d entries, truncated %v, err %v", len(got.Entries), got.Truncated, err)
			}
		})
	}
	// Once "]" is read the list is complete, even if the cap cuts the "}".
	t.Run("list closed before cap", func(t *testing.T) {
		got, err := decodeListing(strings.NewReader(body), int64(len(body)-1))
		if err != nil || got.Truncated || len(got.Entries) != 2 {
			t.Fatalf("got %d entries, truncated %v, err %v", len(got.Entries), got.Truncated, err)
		}
	})
	t.Run("whole body exactly at cap", func(t *testing.T) {
		got, err := decodeListing(strings.NewReader(body), int64(len(body)))
		if err != nil || got.Truncated || len(got.Entries) != 2 {
			t.Fatalf("got %d entries, truncated %v, err %v", len(got.Entries), got.Truncated, err)
		}
	})
	t.Run("malformed under cap is an error", func(t *testing.T) {
		if _, err := decodeListing(strings.NewReader(`{"list":[`+e1), 1<<20); err == nil {
			t.Fatal("want error for an answer that ends early without hitting the cap")
		}
	}) // Only running out of bytes is a cut; a syntax error that happens to sit
	// at the cap is still a broken answer.
	t.Run("malformed at cap is an error", func(t *testing.T) {
		bad := `{"list":[` + e1 + `x` + e2 + `]}`
		cut := len(`{"list":[` + e1 + `x`)
		if _, err := decodeListing(strings.NewReader(bad), int64(cut)); err == nil {
			t.Fatal("want error for malformed JSON inside the cap")
		}
	})
}
