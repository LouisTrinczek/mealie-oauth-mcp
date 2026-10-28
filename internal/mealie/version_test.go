package mealie

import "testing"

func TestCompareVersions(t *testing.T) {
	tests := []struct {
		name string
		a, b string
		want int
	}{
		{"older", "v3.23.0", "v3.28.0", -1},
		{"equal", "v3.23.0", "v3.23.0", 0},
		{"no v prefix", "3.29.1", "v3.23.0", 1},
		{"numeric not lexical", "v3.9.0", "v3.10.0", -1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := CompareVersions(tt.a, tt.b)
			if err != nil {
				t.Fatalf("CompareVersions: %v", err)
			}
			if got != tt.want {
				t.Errorf("CompareVersions(%q, %q) = %d, want %d", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

func TestCompareVersions_Invalid(t *testing.T) {
	for _, bad := range []string{"", "v3.23", "v3.x.0", "nightly", "v3.23.0.1", "v-1.0.0"} {
		t.Run(bad, func(t *testing.T) {
			if _, err := CompareVersions(bad, MinVersion); err == nil {
				t.Errorf("CompareVersions(%q, ...) returned no error", bad)
			}
			if _, err := CompareVersions(MinVersion, bad); err == nil {
				t.Errorf("CompareVersions(..., %q) returned no error", bad)
			}
		})
	}
}
