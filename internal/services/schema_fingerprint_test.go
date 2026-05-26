package services

import (
	"testing"
)

func TestComputeSchemaHash_Stable(t *testing.T) {
	h1 := ComputeSchemaHash()
	h2 := ComputeSchemaHash()
	if h1 != h2 {
		t.Errorf("ComputeSchemaHash not stable: %s vs %s", h1, h2)
	}
	if len(h1) != 12 {
		t.Errorf("hash should be 12 chars (6 bytes hex), got %d (%s)", len(h1), h1)
	}
}

func TestExtractMajor(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"v2.4.10", 2},
		{"2.4.10", 2},
		{"v3.0.0", 3},
		{"v10.5.2", 10},
		{"v2", 2},
		{"abc", -1},
		{"", -1},
	}
	for _, tc := range cases {
		if got := ExtractMajor(tc.in); got != tc.want {
			t.Errorf("ExtractMajor(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}
