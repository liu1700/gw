package branchinfo

import (
	"strings"
	"testing"
)

func TestSlugify(t *testing.T) {
	cases := map[string]string{
		"main":              "main",
		"feature/auth":      "feature-auth",
		"Fix/UI_Glitch":     "fix-ui-glitch",
		"hotfix/CVE-2026-1": "hotfix-cve-2026-1",
		"":                  "detached",
		"///":               "detached",
	}
	for in, want := range cases {
		if got := Slugify(in); got != want {
			t.Errorf("Slugify(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSlugifyLongNamesFitDNSLabel(t *testing.T) {
	long := "feature/" + strings.Repeat("very-long-segment-", 8)
	got := Slugify(long)
	if len(got) > 63 {
		t.Errorf("slug %q is %d chars, exceeds DNS label limit of 63", got, len(got))
	}
	// Distinct long branches must not collide after truncation.
	other := Slugify(long + "x")
	if got == other {
		t.Errorf("distinct branches slugified to the same value %q", got)
	}
}

func TestPortForDeterministicAndInRange(t *testing.T) {
	a := PortFor("feature/auth", "web")
	b := PortFor("feature/auth", "web")
	if a != b {
		t.Errorf("PortFor not deterministic: %d vs %d", a, b)
	}
	if a < portBase || a >= portBase+portRange {
		t.Errorf("port %d outside [%d, %d)", a, portBase, portBase+portRange)
	}
	if PortFor("feature/auth", "web") == PortFor("feature/auth", "api") {
		t.Error("different services on the same branch hashed to the same base port")
	}
}
