package mgr

import "testing"

func TestServicePorts(t *testing.T) {
	cases := []struct {
		base, perUser int
		want          string
	}{
		{10000, 50, "10001-10049"},
		{10050, 50, "10051-10099"},
		{10000, 2, "10001"},
		{10000, 1, ""},
	}
	for _, c := range cases {
		if got := ServicePorts(c.base, c.perUser); got != c.want {
			t.Errorf("ServicePorts(%d, %d) = %q, want %q", c.base, c.perUser, got, c.want)
		}
	}
}
