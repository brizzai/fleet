package main

import (
	"strings"
	"testing"

	"github.com/brizzai/fleet/internal/config"
)

func TestParseAreaFlag(t *testing.T) {
	withAreas := &config.Config{Areas: []config.Area{{Name: "personal"}, {Name: "work"}}}
	tests := []struct {
		name    string
		args    []string
		cfg     *config.Config
		want    string
		wantErr string
	}{
		{"no flag", nil, withAreas, "", ""},
		{"separate value", []string{"--area", "work"}, withAreas, "work", ""},
		{"equals value", []string{"--area=personal"}, withAreas, "personal", ""},
		{"case-insensitive", []string{"--area", "WORK"}, withAreas, "work", ""},
		{"unknown lists the areas", []string{"--area", "nope"}, withAreas, "", "available: personal, work"},
		{"missing value", []string{"--area"}, withAreas, "", "needs a name"},
		{"no areas configured", []string{"--area", "work"}, &config.Config{}, "", "no areas configured"},
		{"stray argument", []string{"--bogus"}, withAreas, "", "unknown argument"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseAreaFlag(tc.args, tc.cfg)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("got %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}
