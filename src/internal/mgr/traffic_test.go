package mgr

import (
	"regexp"
	"testing"
)

func TestRandomHostname(t *testing.T) {
	re := regexp.MustCompile(`^vps-[0-9a-f]{8}$`)
	a, b := randomHostname(), randomHostname()
	if !re.MatchString(a) || !re.MatchString(b) {
		t.Errorf("unexpected hostnames: %q, %q", a, b)
	}
	if a == b {
		t.Errorf("expected distinct random hostnames, got %q twice", a)
	}
}

func TestFormatGB(t *testing.T) {
	cases := []struct {
		bytes uint64
		want  string
	}{
		{0, "0.0"},
		{1 << 30, "1.0"},
		{2<<30 + 1<<29, "2.5"},
		{100 * 1e6, "0.1"},
		{1536 * 1e6, "1.4"},
	}
	for _, c := range cases {
		if got := FormatGB(c.bytes); got != c.want {
			t.Errorf("FormatGB(%d) = %q, want %q", c.bytes, got, c.want)
		}
	}
}
