package utils

import (
	"io"
	"testing"

	"github.com/rs/zerolog"
)

func testLogger() zerolog.Logger {
	return zerolog.New(io.Discard)
}

func TestParseAllowedHosts(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    []string
		wantErr bool
	}{
		{name: "empty string disables check", raw: "", want: nil},
		{name: "bare host", raw: "platform.go", want: []string{"platform.go"}},
		{name: "host with port", raw: "localhost:3000", want: []string{"localhost:3000"}},
		{name: "url with scheme strips scheme", raw: "http://localhost:3000", want: []string{"localhost:3000"}},
		{name: "multiple mixed", raw: "localhost:3000,http://example.com,platform.go",
			want: []string{"localhost:3000", "example.com", "platform.go"}},
		{name: "whitespace trimmed", raw: "  platform.go  ,  example.com  ",
			want: []string{"platform.go", "example.com"}},
		{name: "empty entries skipped", raw: "platform.go,,example.com",
			want: []string{"platform.go", "example.com"}},
		{name: "duplicate hosts dedup", raw: "platform.go,platform.go", want: []string{"platform.go"}},
		{name: "invalid url errors", raw: "http://%zz", wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseAllowedHosts(testLogger(), tc.raw)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("set size = %d, want %d (got %v)", len(got), len(tc.want), got)
			}
			for _, h := range tc.want {
				if _, ok := got[h]; !ok {
					t.Errorf("host %q missing from set %v", h, got)
				}
			}
		})
	}
}

func TestAllowedHostSet_Allows(t *testing.T) {
	set, err := ParseAllowedHosts(testLogger(), "platform.go,localhost:3000")
	if err != nil {
		t.Fatalf("setup: %v", err)
	}

	tests := []struct {
		name   string
		origin string
		want   bool
	}{
		{name: "empty origin allowed (non-browser)", origin: "", want: true},
		{name: "bare host in set", origin: "platform.go", want: true},
		{name: "host with port in set", origin: "localhost:3000", want: true},
		{name: "full origin url with scheme", origin: "https://platform.go", want: true},
		{name: "host not in set", origin: "evil.com", want: false},
		{name: "invalid origin", origin: "http://%zz", want: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := set.Allows(testLogger(), tc.origin)
			if got != tc.want {
				t.Errorf("Allows(%q) = %v, want %v", tc.origin, got, tc.want)
			}
		})
	}

	t.Run("empty set allows everything", func(t *testing.T) {
		empty := AllowedHostSet{}
		if !empty.Allows(testLogger(), "anything.com") {
			t.Errorf("empty set should allow any origin")
		}
	})
}
