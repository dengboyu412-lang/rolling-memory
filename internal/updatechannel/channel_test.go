// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package updatechannel

import "testing"

func TestNormalize(t *testing.T) {
	cases := map[string]string{"": Stable, "stable": Stable, " BETA ": Beta, "dev": Dev}
	for input, want := range cases {
		got, err := Normalize(input)
		if err != nil || got != want {
			t.Fatalf("Normalize(%q) = %q, %v; want %q", input, got, err, want)
		}
	}
	if _, err := Normalize("nightly"); err == nil {
		t.Fatal("unknown channel must be rejected")
	}
}

func TestNormalizeAuto(t *testing.T) {
	cases := map[string]string{"": AutoNotify, "off": AutoOff, " NOTIFY ": AutoNotify, "apply": AutoApply}
	for input, want := range cases {
		got, err := NormalizeAuto(input)
		if err != nil || got != want {
			t.Fatalf("NormalizeAuto(%q) = %q, %v; want %q", input, got, err, want)
		}
	}
	if _, err := NormalizeAuto("always"); err == nil {
		t.Fatal("unknown auto mode must be rejected")
	}
}
