package cmd

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/asymptote"
	endpointconfig "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/config"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/edition"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/lifecycle"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/service"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/handoff"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/localonly"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/onboarding"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/testenv"
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
		{"endpoint repair", map[string]string{"harness": "all"}, true},
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
		{"mcp serve", map[string]string{"transport": "sse"}, true},
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
		{"ci exec", map[string]string{"upload": "s3"}, true},
		// The macOS package's postinstall runs this in both editions; under the lock it removes an
		// updater an earlier install left, so a refusal would fail every nuro package install.
		{"endpoint update install-daemon", nil, false},
		{"endpoint integrations vscode setup", nil, false},
		{"endpoint integrations vscode setup", map[string]string{"endpoint": "http://127.0.0.1:4318"}, false},
		{"endpoint integrations vscode setup", map[string]string{"endpoint": "http://localhost:4318"}, false},
		{"endpoint integrations vscode setup", map[string]string{"endpoint": "http://[::1]:4318"}, false},
		{"endpoint integrations vscode setup", map[string]string{"endpoint": "http://192.0.2.10:4318"}, true},
		{"endpoint integrations vscode setup", map[string]string{"endpoint": "https://otel.example.invalid"}, true},
		{"endpoint integrations vscode setup", map[string]string{"endpoint": "127.0.0.1:4318"}, true},
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

// Every unconditional rule's command is named here, not read from the table, so deleting a rule
// fails a test rather than quietly reopening the path.
func TestLocalOnlyRefusesTheKnownEgressCommands(t *testing.T) {
	lockNuroEdition(t)
	for _, path := range []string{
		"login", "logout", "share", "endpoint connect", "endpoint onboarding", "endpoint enforcement",
		"endpoint update", "endpoint update enable", "endpoint cursor usage sync", "endpoint grok sync",
		"endpoint asymptote", "endpoint cloudwatch", "endpoint datadog", "endpoint elastic",
		"endpoint falcon", "endpoint gcs", "endpoint rapid7", "endpoint s3", "endpoint sentinel",
		"endpoint sumo", "endpoint wazuh", "cloud", "mcp connect", "mcp token", "rules pull",
		"version check", "version refresh", "git notes push", "git hook pre-push", "skills install",
		"memory skills install",
	} {
		c := firstRunnable(findCommand(t, path))
		if c == nil {
			t.Errorf("`beacon %s` has no runnable command", path)
			continue
		}
		if err := preRun(c); !isRefusal(err) {
			t.Errorf("`beacon %s` ran under the lock (pre-run returned %v)", commandPathBelowRoot(c), err)
		}
	}
}

// The whole table is pinned, conditional rules included: a rule that one above shadows (such as
// "endpoint update enable" under "endpoint update") or one that only fires on a flag still fails
// here when it is deleted, so every change to the lock is also a deliberate edit of this list.
func TestLocalOnlyRuleTableIsPinned(t *testing.T) {
	want := []string{
		"login", "logout", "share", "endpoint connect", "endpoint onboarding", "endpoint enforcement",
		"endpoint update (exact)", "endpoint update enable", "endpoint cursor usage sync", "endpoint grok sync",
		"endpoint asymptote", "endpoint cloudwatch", "endpoint datadog", "endpoint elastic",
		"endpoint falcon", "endpoint gcs", "endpoint rapid7", "endpoint s3", "endpoint sentinel",
		"endpoint sumo", "endpoint wazuh",
		"endpoint install (when)", "endpoint install (when)", "endpoint install (when)", "endpoint install (when)",
		"endpoint repair (when)", "endpoint repair (when)", "endpoint repair (when)",
		"endpoint hooks install (when)", "endpoint hooks install (when)",
		"endpoint integrations vscode setup (when)", "endpoint integrations claude-cowork setup (when)",
		"cloud", "ci (when)", "mcp connect", "mcp token", "mcp serve (when)", "rules pull",
		"version check (exact)", "version refresh (exact)", "memory evaluations run (when)", "handoff resume (when)",
		"git notes push", "git hook pre-push", "git setup (when)", "skills install", "memory skills install",
	}
	got := make([]string, 0, len(localOnlyRules))
	for _, rule := range localOnlyRules {
		entry := rule.path
		if rule.exact {
			entry += " (exact)"
		}
		if rule.when != nil {
			entry += " (when)"
		}
		got = append(got, entry)
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("rule table changed (%d rules, want %d):\n%s", len(got), len(want), strings.Join(got, "\n"))
	}
}

// firstRunnable returns c, or its first runnable descendant when c only groups commands.
func firstRunnable(c *cobra.Command) *cobra.Command {
	if c.Runnable() {
		return c
	}
	for _, child := range c.Commands() {
		if r := firstRunnable(child); r != nil {
			return r
		}
	}
	return nil
}

func TestLoopbackDialAddrNeverResolvesLocalhost(t *testing.T) {
	cases := []struct {
		addr, want string
		ok         bool
	}{
		{"localhost:4318", "127.0.0.1:4318", true},
		{"LOCALHOST:80", "127.0.0.1:80", true},
		{"127.0.0.1:4318", "127.0.0.1:4318", true},
		{"[::1]:4318", "[::1]:4318", true},
		{"10.99.0.1:80", "", false},
		{"example.invalid:443", "", false},
		{"localhost", "", false},
	}
	for _, tc := range cases {
		got, ok := loopbackDialAddr(tc.addr)
		if ok != tc.ok || (ok && got != tc.want) {
			t.Errorf("loopbackDialAddr(%q) = %q, %v; want %q, %v", tc.addr, got, ok, tc.want, tc.ok)
		}
	}
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

// A failed `endpoint doctor --fix` puts the old collector config back. Under the lock it starts the
// collector on that config only if it exports to the local log alone; with an earlier build's
// Splunk exporter it does not restart the collector, and says so.
func TestRepairRollbackUnderTheLockReloadsOnlyALocalCollector(t *testing.T) {
	t.Setenv(localonly.Env, "1")
	const local = "exporters:\n  beaconjson:\n    path: /tmp/runtime.jsonl\n"
	for name, tc := range map[string]struct {
		before    string // "" means no collector config before the repair
		wantLoads int
		wantError string
	}{
		"local log only":            {local, 2, ""},
		"an earlier build's Splunk": {local + "  splunk_hec:\n    endpoint: https://splunk.example:8088/services/collector\n", 1, "which exports beyond the local log"},
		"no config before":          {"", 1, "no collector config is left"},
	} {
		t.Run(name, func(t *testing.T) {
			prevOpts, prevLoad, prevSave := endpointOpts, repairLoadEndpointConfig, repairSaveEndpointConfig
			prevBinary, prevWrite, prevWait, prevManager := repairResolveCollectorBinary, repairWriteCollectorConfig, repairWaitCollectorReady, newRepairServiceManager
			t.Cleanup(func() {
				endpointOpts, repairLoadEndpointConfig, repairSaveEndpointConfig = prevOpts, prevLoad, prevSave
				repairResolveCollectorBinary, repairWriteCollectorConfig, repairWaitCollectorReady, newRepairServiceManager = prevBinary, prevWrite, prevWait, prevManager
			})
			home := t.TempDir()
			testenv.SetHome(t, home)
			collectorConfig := filepath.Join(home, "otelcol.yaml")
			cfg := endpointconfig.Config{UserMode: true, LogPath: filepath.Join(home, "runtime.jsonl"),
				Collector: endpointconfig.Collector{ConfigPath: collectorConfig, SpoolPath: filepath.Join(home, "spool", "otlp.jsonl"),
					GRPCPort: endpointconfig.DefaultGRPCPort, HTTPPort: endpointconfig.DefaultHTTPPort}}
			if _, err := endpointconfig.Save(cfg); err != nil {
				t.Fatal(err)
			}
			if tc.before != "" {
				if err := os.WriteFile(collectorConfig, []byte(tc.before), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			manager := &fakeRepairServiceManager{plistPath: filepath.Join(home, "agent.plist")}
			newRepairServiceManager = func(bool) repairServiceManager { return manager }
			repairResolveCollectorBinary = func(string) (string, error) { return filepath.Join(home, "beacon-otelcol"), nil }
			repairWriteCollectorConfig = func(c endpointconfig.Config) error {
				return os.WriteFile(c.Collector.ConfigPath, []byte(local), 0o600)
			}
			repairWaitCollectorReady = func(endpointconfig.Config, time.Duration) error { return context.DeadlineExceeded }
			endpointOpts.logPath = ""

			err := repairCollectorServiceFromStatus(lifecycle.Status{
				RuntimeLog: lifecycle.RuntimeLogSource{EffectiveUserMode: true},
				Service:    service.Status{Loaded: true},
			})

			if manager.loads != tc.wantLoads {
				t.Fatalf("Load calls = %d, want %d", manager.loads, tc.wantLoads)
			}
			notReloaded := err != nil && strings.Contains(err.Error(), "did not reload the collector service")
			if notReloaded != (tc.wantError != "") || (tc.wantError != "" && !strings.Contains(err.Error(), tc.wantError)) {
				t.Fatalf("repair error = %v", err)
			}
			if tc.before == "" {
				if _, statErr := os.Stat(collectorConfig); !os.IsNotExist(statErr) {
					t.Fatalf("collector config left behind: %v", statErr)
				}
				return
			}
			assertFileContent(t, collectorConfig, tc.before)
		})
	}
}

// VS Code resolves the endpoint name itself, so under the lock a localhost endpoint is written as
// 127.0.0.1 and a hosts file cannot point Copilot's telemetry elsewhere.
func TestVSCodeSetupWritesLocalhostAs127001UnderTheLock(t *testing.T) {
	t.Setenv(localonly.Env, "1")
	testenv.SetHome(t, t.TempDir())
	workspace := t.TempDir()
	prev := endpointOpts
	t.Cleanup(func() { endpointOpts = prev })
	endpointOpts.vscodeEndpoint = "HTTP://LOCALHOST:4318"
	endpointOpts.vscodeWorkspace = workspace
	endpointOpts.dryRun = false

	if err := runEndpointVSCodeSetup(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(workspace, ".vscode", "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "127.0.0.1:4318") || strings.Contains(strings.ToLower(string(data)), "localhost") {
		t.Fatalf("settings = %s, want the endpoint written as 127.0.0.1", data)
	}
	for raw, want := range map[string]string{
		"http://localhost:4318/v1": "http://127.0.0.1:4318/v1",
		"http://localhost":         "http://127.0.0.1",
		"http://[::1]:4318":        "http://[::1]:4318",
		"http://127.0.0.1:4318":    "http://127.0.0.1:4318",
	} {
		if got := pinLoopbackHost(raw); got != want {
			t.Errorf("pinLoopbackHost(%q) = %q, want %q", raw, got, want)
		}
	}
}
