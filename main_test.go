package main

import (
	"strings"
	"testing"
)

// -mode picks how worker.php runs the entry and whether a master forks the workers. The names
// before -mode worker|prefork|legacy (callable, script) are refused, not read as something else.
func TestParseMode(t *testing.T) {
	for mode, want := range map[string]struct {
		entry   string
		prefork bool
	}{
		"worker":  {"callable", false},
		"prefork": {"callable", true},
		"legacy":  {"script", false},
	} {
		// A config that had prefork set must lose it for worker and legacy
		cfg := config{prefork: true}
		if err := parseMode(mode, &cfg); err != nil {
			t.Fatalf("-mode %s: %v", mode, err)
		}
		if cfg.mode != want.entry || cfg.prefork != want.prefork {
			t.Errorf("-mode %s: got entry %q prefork %v, want %q %v", mode, cfg.mode, cfg.prefork, want.entry, want.prefork)
		}
	}

	for _, mode := range []string{"callable", "script", "", "Worker"} {
		err := parseMode(mode, &config{})
		if err == nil || !strings.Contains(err.Error(), "-mode must be worker, prefork or legacy") {
			t.Errorf("-mode %q: expected it to be refused, got %v", mode, err)
		}
	}
}
