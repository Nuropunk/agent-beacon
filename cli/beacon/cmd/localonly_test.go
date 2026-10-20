package cmd

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/asymptote"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/edition"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/handoff"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/localonly"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/onboarding"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/updatecheck"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/version"
)

// lockNuroEdition builds this test binary as the nuro edition for one test, and wires the guards
// the way Execute does.
func lockNuroEdition(t *testing.T) {
	t.Helper()
	t.Setenv(localonly.Env, "")
	prev := version.Edition
	version.Edition = localonly.Edition
	t.Cleanup(func() { version.Edition = prev })
	guardLocalOnlyCommands(rootCmd)
}

func findCommand(t *testing.T, path string) *cobra.Command {
	t.Helper()
	c, rest, err := rootCmd.Find(strings.Fields(path))
	if err != nil || len(rest) != 0 || commandPathBelowRoot(c) != path {
		t.Fatalf("`beacon %s` resolves to %q (rest %v, err %v)", path, commandPathBelowRoot(c), rest, err)
	}
	return c
}

// setFlags sets flags on c for one test and puts every one back afterwards, so a later test sees
// the command as a fresh process would.
func setFlags(t *testing.T, c *cobra.Command, flags map[string]string) {
	t.Helper()
	for name, value := range flags {
		f := c.Flags().Lookup(name)
		if f == nil {
			t.Fatalf("`beacon %s` has no --%s flag", commandPathBelowRoot(c), name)
		}
		if err := c.Flags().Set(name, value); err != nil {
			t.Fatalf("--%s=%s: %v", name, value, err)
		}
		t.Cleanup(func() {
			_ = f.Value.Set(f.DefValue)
			f.Changed = false
		})
	}
}

func preRun(c *cobra.Command) error {
	if c.PreRunE == nil {
		return nil
	}
	return c.PreRunE(c, nil)
}

func isRefusal(err error) bool {
	var refused *localonly.ErrRefused
	return errors.As(err, &refused)
}

// A rule naming a command that does not exist guards nothing, and the egress it was written for
// runs. Every rule has to resolve to a real command.
func TestLocalOnlyRulesNameRealCommands(t *testing.T) {
	for _, rule := range localOnlyRules {
		findCommand(t, rule.path)
	}
}

// Under the nuro edition, every command a rule refuses outright has to refuse before it runs,
// including the commands below it.
func TestLocalOnlyRefusesEgressCommandsInTheNuroEdition(t *testing.T) {
	lockNuroEdition(t)
	for _, rule := range localOnlyRules {
		if rule.when != nil {
			continue
		}
		top := findCommand(t, rule.path)
		var runnable []*cobra.Command
		var walk func(c *cobra.Command)
		walk = func(c *cobra.Command) {
			if c.Runnable() {
				runnable = append(runnable, c)
			}
			if rule.exact {
				return
			}
			for _, child := range c.Commands() {
				walk(child)
			}
		}
		walk(top)
		if len(runnable) == 0 {
			t.Errorf("`beacon %s` has no runnable command for its rule to guard", rule.path)
		}
		for _, c := range runnable {
			if err := preRun(c); !isRefusal(err) {
				t.Errorf("`beacon %s` ran under the lock (pre-run returned %v)", commandPathBelowRoot(c), err)
			}
		}
	}
}

// The flag rules refuse only the invocations that send, configure the held engine or hand over a
// brief; the same commands stay usable without them.
func TestLocalOnlyFlagRules(t *testing.T) {
	lockNuroEdition(t)
	cases := []struct {
		path    string
		flags   map[string]string
		refused bool
	}{
		{"endpoint install", nil, false},
		{"endpoint install", map[string]string{"harness": "claude,codex"}, false},
		{"endpoint install", map[string]string{"connect": "true"}, true},
		{"endpoint install", map[string]string{"harness": "all"}, true},
		{"endpoint install", map[string]string{"harness": "claude,grok"}, true},
		{"endpoint repair", map[string]string{"harness": "grok-build"}, true},
		{"endpoint repair", map[string]string{"harness": "cursor"}, false},
		{"endpoint install", map[string]string{"splunk-hec-endpoint": "https://hec.example"}, true},
		{"endpoint install", map[string]string{"falcon-hec-token": "t"}, true},
		{"endpoint repair", map[string]string{"splunk-index": "main"}, true},
		{"endpoint repair", map[string]string{"falcon-ca-file": "/tmp/ca.pem"}, true},
		{"endpoint hooks install", nil, false},
		{"endpoint hooks install", map[string]string{"all": "true"}, true},
		{"endpoint hooks install", map[string]string{"harness": "grok"}, true},
		{"mcp serve", nil, false},
		{"mcp serve", map[string]string{"transport": "stdio"}, false},
		{"mcp serve", map[string]string{"transport": "http"}, true},
		{"handoff resume", nil, false},
		{"handoff resume", map[string]string{"agent": "claude"}, false},
		{"handoff resume", map[string]string{"agent": "codex"}, true},
		{"handoff resume", map[string]string{"agent": "codex", "print": "true"}, false},
		{"memory evaluations run", nil, true},
		{"memory evaluations run", map[string]string{"dry-run": "true"}, false},
		{"git setup", nil, false},
		{"git setup", map[string]string{"share-notes": "false"}, false},
		{"git setup", map[string]string{"share-notes": "true"}, true},
		{"git setup", map[string]string{"share-sessions": "true"}, true},
		{"endpoint integrations claude-cowork setup", nil, false},
		{"endpoint integrations claude-cowork setup", map[string]string{"ngrok": "true"}, true},
		{"ci exec", nil, false},
		{"ci exec", map[string]string{"forward": "splunk"}, true},
		{"ci start", map[string]string{"forward-endpoint": "https://hec.example"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.path+" "+flagsString(tc.flags), func(t *testing.T) {
			c := findCommand(t, tc.path)
			setFlags(t, c, tc.flags)
			err := checkLocalOnly(c, localOnlyRulesFor(tc.path))
			if got := isRefusal(err); got != tc.refused {
				t.Fatalf("refused = %v (%v), want %v", got, err, tc.refused)
			}
		})
	}
}

func flagsString(flags map[string]string) string {
	var parts []string
	for k, v := range flags {
		parts = append(parts, "--"+k+"="+v)
	}
	return strings.Join(parts, " ")
}

// A community build keeps upstream's behavior: the guards are wired but refuse nothing.
func TestLocalOnlyRefusesNothingWithoutTheLock(t *testing.T) {
	t.Setenv(localonly.Env, "")
	prev := version.Edition
	version.Edition = version.DefaultEdition
	t.Cleanup(func() { version.Edition = prev })
	for _, rule := range localOnlyRules {
		c := findCommand(t, rule.path)
		if err := checkLocalOnly(c, []localOnlyRule{rule}); err != nil {
			t.Errorf("`beacon %s` refused without the lock: %v", rule.path, err)
		}
	}
}

func TestLocalOnlyEnvironmentTurnsTheLockOn(t *testing.T) {
	prev := version.Edition
	version.Edition = version.DefaultEdition
	t.Cleanup(func() { version.Edition = prev })
	t.Setenv(localonly.Env, "1")
	c := findCommand(t, "share")
	if err := checkLocalOnly(c, localOnlyRulesFor("share")); !isRefusal(err) {
		t.Fatalf("BEACON_LOCAL_ONLY=1 did not refuse `beacon share`: %v", err)
	}
}

// The seams applyLocalOnly replaces are the egress the command rules cannot see: code that sends
// from inside commands that are otherwise local.
func TestApplyLocalOnlyReplacesTheEgressSeams(t *testing.T) {
	prevPublic := updatecheck.PublicReleases
	prevRefresh := rootStartUpdateRefresh
	prevSend := onboardingSend
	prevConnect := connectManagedEndpoint
	prevLocate := locateEnterpriseRelease
	prevInstall := installEnterpriseRelease
	prevShare := shareAutoCloud
	prevGrok, hadGrok := autoEndpointTargets["grok"]
	prevRunners := endpointBackfillRunners
	prevLaunch := handoffLaunch
	transport := http.DefaultTransport.(*http.Transport)
	prevProxy, prevDial := transport.Proxy, transport.DialContext
	t.Cleanup(func() {
		updatecheck.PublicReleases = prevPublic
		rootStartUpdateRefresh = prevRefresh
		onboardingSend = prevSend
		connectManagedEndpoint = prevConnect
		locateEnterpriseRelease = prevLocate
		installEnterpriseRelease = prevInstall
		shareAutoCloud = prevShare
		if hadGrok {
			autoEndpointTargets["grok"] = prevGrok
		}
		endpointBackfillRunners = prevRunners
		handoffLaunch = prevLaunch
		transport.Proxy, transport.DialContext = prevProxy, prevDial
		transport.CloseIdleConnections()
	})

	refreshed := false
	rootStartUpdateRefresh = func() error { refreshed = true; return nil }
	endpointBackfillRunners = func() []endpointBackfillRunner {
		return []endpointBackfillRunner{{Name: "Grok Build sessions"}, {Name: "Codex sessions"}, {Name: "Claude Code sessions"}}
	}
	var launched []handoff.Plan
	handoffLaunch = func(plan handoff.Plan, _ io.Reader, _, _ io.Writer) (int, error) {
		launched = append(launched, plan)
		return 0, nil
	}

	lockNuroEdition(t)
	applyLocalOnly(rootCmd)

	if updatecheck.PublicReleases {
		t.Error("the GitHub release check is still on")
	}
	if err := rootStartUpdateRefresh(); err != nil || refreshed {
		t.Errorf("the detached update refresh still runs (err %v, ran %v)", err, refreshed)
	}
	if outcome, err := onboardingSend(context.Background(), onboarding.Submission{}); !isRefusal(err) || outcome != onboarding.OutcomePending {
		t.Errorf("onboarding signup = (%q, %v), want a pending refusal", outcome, err)
	}
	if _, err := connectManagedEndpoint(context.Background(), asymptote.ConnectOptions{}); !isRefusal(err) {
		t.Errorf("Beacon Cloud connect = %v, want a refusal", err)
	}
	if _, err := locateEnterpriseRelease(context.Background(), nil, "", "", ""); !isRefusal(err) {
		t.Errorf("enterprise locate = %v, want a refusal", err)
	}
	if _, err := installEnterpriseRelease(context.Background(), edition.Release{}, "", ""); !isRefusal(err) {
		t.Errorf("enterprise install = %v, want a refusal", err)
	}
	if connected, _ := shareAutoCloud(); connected {
		t.Error("automatic sharing still sees Beacon Cloud")
	}
	if grok := autoEndpointTargets["grok"]; grok.Skip == "" || len(grok.Hooks) > 0 || len(grok.OTLP) > 0 {
		t.Errorf("the auto install target still configures Grok: %+v", grok)
	}
	for _, r := range endpointBackfillRunners() {
		if strings.HasPrefix(r.Name, "Grok") {
			t.Errorf("backfill still runs %q", r.Name)
		}
	}
	if n := len(endpointBackfillRunners()); n != 2 {
		t.Errorf("backfill kept %d runners, want the 2 that are not held", n)
	}

	if _, err := handoffLaunch(handoff.Plan{Target: "grok"}, nil, io.Discard, io.Discard); !isRefusal(err) {
		t.Errorf("resuming in Grok = %v, want a refusal", err)
	}
	if _, err := handoffLaunch(handoff.Plan{Target: "codex", BriefPath: "/tmp/brief.md"}, nil, io.Discard, io.Discard); !isRefusal(err) {
		t.Errorf("a brief for Codex = %v, want a refusal", err)
	}
	if _, err := handoffLaunch(handoff.Plan{Target: "codex"}, nil, io.Discard, io.Discard); err != nil {
		t.Errorf("reopening a Codex session in Codex = %v, want it to run", err)
	}
	if _, err := handoffLaunch(handoff.Plan{Target: "claude", BriefPath: "/tmp/brief.md"}, nil, io.Discard, io.Discard); err != nil {
		t.Errorf("a brief for Claude = %v, want it to run", err)
	}
	if len(launched) != 2 {
		t.Errorf("launched %d plans, want the 2 allowed ones", len(launched))
	}

	// 203.0.113.0/24 is TEST-NET-3: never routed, so a refusal here cannot be a network failure in
	// disguise unless it carries the lock's error.
	if _, err := http.Get("http://203.0.113.1/"); !isRefusal(err) {
		t.Errorf("a request off the machine = %v, want the lock's refusal", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "ok") }))
	defer srv.Close()
	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatalf("a loopback request = %v, want it to pass", err)
	}
	_ = resp.Body.Close()
}

func TestOnboardingIsSkippedInTheNuroEdition(t *testing.T) {
	lockNuroEdition(t)
	t.Setenv(onboardingEnvEnabled, "")
	reason, skip := onboardingSkipReason(onboarding.Profile{})
	if !skip || reason != onboardingSkipLocalOnly {
		t.Fatalf("onboardingSkipReason = (%q, %v), want (%q, true)", reason, skip, onboardingSkipLocalOnly)
	}
}
