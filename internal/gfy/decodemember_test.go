package gfy

import (
	"encoding/json"
	"strings"
	"testing"
)

// decodeMember decodes one top-level key, skipping every other member token by
// token. A found key is decoded into the destination; a missing one is not an
// error, and a document that is not an object (or is truncated) is.
func TestDecodeMember(t *testing.T) {
	tests := []struct {
		name    string
		doc     string
		key     string
		found   bool
		wantErr bool
		wantN   int
	}{
		{
			name:  "nested member found",
			doc:   `{"a":{"x":[1,{"y":2}]},"b":"s","want":{"n":7}}`,
			key:   "want",
			found: true,
			wantN: 7,
		},
		{
			name:  "key absent",
			doc:   `{"a":1}`,
			key:   "want",
			found: false,
		},
		{
			name:    "not an object",
			doc:     `[1]`,
			key:     "want",
			wantErr: true,
		},
		{
			name:    "truncated object",
			doc:     `{"a":[1,`,
			key:     "want",
			wantErr: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var got struct {
				N int `json:"n"`
			}
			dec := json.NewDecoder(strings.NewReader(tc.doc))
			found, err := decodeMember(dec, tc.key, &got)
			if found != tc.found {
				t.Errorf("found = %t, want %t", found, tc.found)
			}
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %t", err, tc.wantErr)
			}
			if tc.found && !tc.wantErr && got.N != tc.wantN {
				t.Errorf("decoded N = %d, want %d", got.N, tc.wantN)
			}
		})
	}
}
