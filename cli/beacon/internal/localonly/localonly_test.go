package localonly

import (
	"errors"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/version"
)

func withEdition(t *testing.T, edition string) {
	t.Helper()
	prev := version.Edition
	version.Edition = edition
	t.Cleanup(func() { version.Edition = prev })
}

func TestEnforcedByTheNuroEdition(t *testing.T) {
	withEdition(t, Edition)
	for _, v := range []string{"", "0", "false", "off"} {
		t.Setenv(Env, v)
		if !Enforced() {
			t.Errorf("%s=%q lifted the lock from a nuro build", Env, v)
		}
	}
}

func TestEnforcedByTheEnvironment(t *testing.T) {
	withEdition(t, version.DefaultEdition)
	for v, want := range map[string]bool{"": false, "0": false, "no": false, "1": true, "true": true, " YES ": true, "on": true} {
		t.Setenv(Env, v)
		if got := Enforced(); got != want {
			t.Errorf("%s=%q: Enforced() = %v, want %v", Env, v, got, want)
		}
	}
}

func TestHeld(t *testing.T) {
	for name, want := range map[string]bool{
		"grok": true, "Grok": true, "grok-build": true, "grok_build": true, "grok_bot": true, " grok ": true,
		"claude": false, "codex": false, "grokker": false, "": false,
	} {
		if got := Held(name); got != want {
			t.Errorf("Held(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestRefuseExplainsWhatAndWhy(t *testing.T) {
	err := Refuse("`beacon share`", "it uploads sessions to beacon.sh")
	var refused *ErrRefused
	if !errors.As(err, &refused) {
		t.Fatalf("Refuse returned %T", err)
	}
	for _, want := range []string{"`beacon share`", "uploads sessions", "owner's explicit go"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("%q does not mention %q", err.Error(), want)
		}
	}
}
