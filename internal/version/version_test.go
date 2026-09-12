package version

import (
	"strings"
	"testing"
	"time"
)

func TestInfo_Defaults(t *testing.T) {
	// With default global variables
	info := Info()

	lines := strings.Split(info, "\n")
	if len(lines) != 3 {
		t.Fatalf("expected 3 lines in version info, got %d:\n%s", len(lines), info)
	}

	if lines[0] != "gemsub dev" {
		t.Errorf("expected line 0 to be 'gemsub dev', got %q", lines[0])
	}
	if lines[1] != "commit: none" {
		t.Errorf("expected line 1 to be 'commit: none', got %q", lines[1])
	}
	if lines[2] != "built: unknown" {
		t.Errorf("expected line 2 to be 'built: unknown', got %q", lines[2])
	}
}

func TestFormatInfo(t *testing.T) {
	tests := []struct {
		name      string
		ver       string
		commit    string
		date      string
		wantLine0 string
		wantLine1 string
		wantLine2 string
	}{
		{
			name:      "semantic version with v prefix",
			ver:       "v1.2.3",
			commit:    "abcdef12",
			date:      "2026-09-10T12:00:00Z",
			wantLine0: "gemsub v1.2.3",
			wantLine1: "commit: abcdef12",
			wantLine2: "built: 2026-09-10T12:00:00Z",
		},
		{
			name:      "semantic version without v prefix gets normalized",
			ver:       "1.2.3",
			commit:    "abcdef12",
			date:      "2026-09-10T12:00:00Z",
			wantLine0: "gemsub v1.2.3",
			wantLine1: "commit: abcdef12",
			wantLine2: "built: 2026-09-10T12:00:00Z",
		},
		{
			name:      "dev version keeps dev without v prefix",
			ver:       "dev",
			commit:    "none",
			date:      "unknown",
			wantLine0: "gemsub dev",
			wantLine1: "commit: none",
			wantLine2: "built: unknown",
		},
		{
			name:      "non-UTC RFC3339 date normalized to UTC",
			ver:       "v0.5.0",
			commit:    "12345678",
			date:      "2026-09-10T15:30:00+03:30",
			wantLine0: "gemsub v0.5.0",
			wantLine1: "commit: 12345678",
			wantLine2: "built: 2026-09-10T12:00:00Z",
		},
		{
			name:      "empty fields fall back to sensible defaults",
			ver:       "",
			commit:    "",
			date:      "",
			wantLine0: "gemsub dev",
			wantLine1: "commit: none",
			wantLine2: "built: unknown",
		},
		{
			name:      "unparseable date preserved as-is",
			ver:       "v2.0.0",
			commit:    "deadbeef",
			date:      "not-a-date",
			wantLine0: "gemsub v2.0.0",
			wantLine1: "commit: deadbeef",
			wantLine2: "built: not-a-date",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := formatInfo(AppName, tt.ver, tt.commit, tt.date)
			lines := strings.Split(res, "\n")
			if len(lines) != 3 {
				t.Fatalf("expected 3 lines, got %d:\n%s", len(lines), res)
			}
			if lines[0] != tt.wantLine0 {
				t.Errorf("line 0 mismatch: got %q, want %q", lines[0], tt.wantLine0)
			}
			if lines[1] != tt.wantLine1 {
				t.Errorf("line 1 mismatch: got %q, want %q", lines[1], tt.wantLine1)
			}
			if lines[2] != tt.wantLine2 {
				t.Errorf("line 2 mismatch: got %q, want %q", lines[2], tt.wantLine2)
			}
		})
	}
}

func TestTimeIsUTC(t *testing.T) {
	dateStr := "2026-09-10T17:45:00+04:00"
	res := formatInfo(AppName, "v1.0.0", "abc", dateStr)
	lines := strings.Split(res, "\n")
	builtLine := lines[2]
	parts := strings.SplitN(builtLine, ": ", 2)
	if len(parts) != 2 {
		t.Fatalf("expected 'built: <timestamp>', got %q", builtLine)
	}

	parsed, err := time.Parse(time.RFC3339, parts[1])
	if err != nil {
		t.Fatalf("failed to parse built timestamp: %v", err)
	}

	if parsed.Location() != time.UTC {
		t.Errorf("expected UTC location, got %v", parsed.Location())
	}
	if !strings.HasSuffix(parts[1], "Z") {
		t.Errorf("expected UTC 'Z' suffix, got %q", parts[1])
	}
}
