package lx

import "testing"

func TestHasShebang(t *testing.T) {
	cases := []struct {
		script string
		want   bool
	}{
		{"#!/bin/bash\napt-get update", true},
		{"  #!/usr/bin/env bash\nx", true},
		{"#!/bin/sh\nx", true},
		{"echo hi", false},
		{"", false},
		{"\n#!\n", false}, // first line is blank, not a shebang
		{"# not a shebang\nx", false},
	}
	for _, c := range cases {
		if got := hasShebang(c.script); got != c.want {
			t.Errorf("hasShebang(%q) = %v, want %v", c.script, got, c.want)
		}
	}
}
