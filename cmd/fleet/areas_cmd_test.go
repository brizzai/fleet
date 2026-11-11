package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/brizzai/fleet/internal/config"
)

func TestWriteAreasListsEachAreaWithItsSessions(t *testing.T) {
	areas := []config.Area{
		{Name: "personal", Paths: []string{"~/P", "~/S"}, Account: "me@gmail.com", Default: true},
		{Name: "work", Paths: []string{"~/W"}, Account: "me@company.com"},
	}
	sessions := []string{"/h/P/a", "/h/P/Youtube/b", "/h/W/c", "/h/elsewhere"}
	var buf bytes.Buffer
	writeAreas(&buf, areas, sessions, "/h", "/h/.config/fleet/config.json", nil)
	out := buf.String()

	for _, want := range []string{
		"personal", "default", "me@gmail.com", "~/P, ~/S",
		"work", "me@company.com", "~/W",
		"/h/.config/fleet/config.json", "fleet --area <name>",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	// personal holds its two sessions plus the unmatched one (default area).
	if !lineHas(out, "personal", "3") || !lineHas(out, "work", "1") {
		t.Errorf("session counts wrong:\n%s", out)
	}
}

func TestWriteAreasWithoutConfigExplainsHowToAdd(t *testing.T) {
	var buf bytes.Buffer
	writeAreas(&buf, nil, nil, "/h", "/h/.config/fleet/config.json", nil)
	out := buf.String()
	for _, want := range []string{"No areas configured", "/h/.config/fleet/config.json", `"areas"`, `"paths"`} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

func lineHas(out, name, count string) bool {
	for _, l := range strings.Split(out, "\n") {
		f := strings.Fields(l)
		if len(f) > 1 && f[0] == name {
			for _, x := range f[1:] {
				if x == count {
					return true
				}
			}
		}
	}
	return false
}
