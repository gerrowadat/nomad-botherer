package gitwatch

import "testing"

func TestNormalizeHCLDir(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"", ""},
		{".", ""},
		{"/", ""},
		{"jobs", "jobs/"},
		{"/jobs", "jobs/"},
		{"jobs/", "jobs/"},
		{"/jobs/", "jobs/"},
		{"a/b/c", "a/b/c/"},
	}
	for _, tc := range cases {
		if got := normalizeHCLDir(tc.in); got != tc.want {
			t.Errorf("normalizeHCLDir(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
