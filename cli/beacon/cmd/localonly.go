package cmd

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/asymptote"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/edition"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/selfupdate"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/handoff"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/localonly"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/onboarding"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/updatecheck"
)

// localOnlyRule refuses one command, and the commands below it unless exact is set, while the
// nuro edition's lock is on. when narrows the rule to some invocations; nil refuses every one.
type localOnlyRule struct {
	path  string
	exact bool
	why   string
	when  func(*cobra.Command) bool
}

const (
	whySendsOff        = "it sends data off this machine"
	whyBeaconCloud     = "it connects this machine to Beacon Cloud and forwards its traces"
	whyForwardPack     = "it renders a pack that forwards the runtime log to a SIEM or bucket"
	whyForwardHEC      = "the collector would forward the runtime log to Splunk or Falcon LogScale"
	whyHeldEngine      = "the Grok engine is on hold, so Beacon neither runs nor configures it"
	whyOtherVendors    = "it hands private traces to another vendor's agent"
	whyHookBypass      = "it pushes with git push --no-verify, which skips the repository's hooks"
	whyAllHarnesses    = "'all' includes the held Grok engine; name the harnesses to configure instead"
	whySkillsElsewhere = "it installs Beacon skills into ~/.agents/skills, which other vendors' agents read; " +
		"the nuro edition ships its skills through the owner's Claude configuration instead"
	whyRemoteOTLP  = "it points a runtime's telemetry, prompts and responses included, at an address off this machine"
	whyTraceSkills = "it copies captured trace content, prompts included, into the project's .agents/skills, " +
		"which other vendors' agents read and a commit would carry off the machine"
)

// localOnlyRules lists every command path that sends data off the machine, configures the held
// engine or gives another vendor's agent private traces. The inventory behind it is the network
// lane of the nuro fork's ingest: each upstream egress path either has a rule here, a seam set in
// applyLocalOnly, or is refused by the transport lock.
var localOnlyRules = []localOnlyRule{
	{path: "login", why: "it signs in to beacon.sh"},
	{path: "logout", why: "it revokes a beacon.sh session over the network"},
	{path: "share", why: "it uploads sessions to beacon.sh"},
	{path: "endpoint connect", why: whyBeaconCloud},
	{path: "endpoint onboarding", why: "it signs up with Asymptote Labs and resends signups"},
	{path: "endpoint enforcement", why: "it downloads and installs a closed enterprise build"},
	{path: "endpoint update", exact: true, why: "it fetches update manifests and packages from GitHub"},
	{path: "endpoint update enable", why: "it schedules a self-updater that fetches from GitHub"},
	{path: "endpoint cursor usage sync", why: "it pulls usage events from api.cursor.com"},
	{path: "endpoint grok sync", why: whyHeldEngine},
	{path: "endpoint asymptote", why: whyForwardPack},
	{path: "endpoint cloudwatch", why: whyForwardPack},
	{path: "endpoint datadog", why: whyForwardPack},
	{path: "endpoint elastic", why: whyForwardPack},
	{path: "endpoint falcon", why: whyForwardPack},
	{path: "endpoint gcs", why: whyForwardPack},
	{path: "endpoint rapid7", why: whyForwardPack},
	{path: "endpoint s3", why: whyForwardPack},
	{path: "endpoint sentinel", why: whyForwardPack},
	{path: "endpoint sumo", why: whyForwardPack},
	{path: "endpoint wazuh", why: whyForwardPack},
	{path: "endpoint install", why: whyBeaconCloud, when: flagTrue("connect")},
	{path: "endpoint install", why: whyAllHarnesses, when: harnessFlagIs("harness", "all")},
	{path: "endpoint install", why: whyHeldEngine, when: harnessFlagHeld("harness")},
	{path: "endpoint install", why: whyForwardHEC, when: flagPrefixChanged("splunk-", "falcon-")},
	{path: "endpoint repair", why: whyAllHarnesses, when: harnessFlagIs("harness", "all")},
	{path: "endpoint repair", why: whyHeldEngine, when: harnessFlagHeld("harness")},
	{path: "endpoint repair", why: whyForwardHEC, when: flagPrefixChanged("splunk-", "falcon-")},
	{path: "endpoint hooks install", why: whyAllHarnesses, when: flagTrue("all")},
	{path: "endpoint hooks install", why: whyHeldEngine, when: harnessFlagHeld("harness")},
	{path: "endpoint integrations vscode setup", why: whyRemoteOTLP,
		when: func(c *cobra.Command) bool {
			e := flagValue(c, "endpoint")
			return e != "" && !loopbackURL(e)
		}},
	{path: "endpoint integrations claude-cowork setup", why: "--ngrok opens a public tunnel to the local OTLP receiver", when: flagTrue("ngrok")},
	{path: "cloud", why: "it pulls or uploads cloud-agent traces through Devin, GCS or S3"},
	{path: "ci", why: whySendsOff, when: anyFlagChanged("forward", "forward-endpoint", "upload")},
	{path: "mcp connect", why: "it registers the Beacon Cloud MCP server in other vendors' agents"},
	{path: "mcp token", why: "it mints a Beacon Cloud MCP token at beacon.sh"},
	{path: "mcp serve", why: "it serves private traces over unauthenticated loopback HTTP that any local process can read; use --transport stdio",
		when: func(c *cobra.Command) bool {
			return flagValue(c, "transport") != "" && flagValue(c, "transport") != "stdio"
		}},
	{path: "rules pull", why: "it downloads rule packs from a URL"},
	{path: "version check", exact: true, why: "it asks GitHub for the latest release"},
	{path: "version refresh", exact: true, why: "it asks GitHub for the latest release"},
	{path: "memory evaluations run", why: "it sends trace content to a third-party evaluator (TypeSafe Jev or SubQ); --dry-run stays local",
		when: func(c *cobra.Command) bool { return !flagTrue("dry-run")(c) }},
	{path: "handoff resume", why: whyOtherVendors,
		when: func(c *cobra.Command) bool {
			a := flagValue(c, "agent")
			return a != "" && !claudeRuntime(a) && !flagTrue("print")(c)
		}},
	{path: "git notes push", why: whyHookBypass},
	// Beacon's pre-push hook script ignores this command's exit status and ends with `exit 0`, so a
	// refusal never stops the person's own push; only Beacon's notes push is skipped.
	{path: "git hook pre-push", why: whyHookBypass},
	{path: "git setup", why: "it shares session links with remotes and beacon.sh on every git push",
		when: func(c *cobra.Command) bool { return flagTrue("share-notes")(c) || flagTrue("share-sessions")(c) }},
	{path: "skills install", why: whySkillsElsewhere},
	{path: "memory skills install", why: whyTraceSkills},
}

// applyLocalOnly installs the nuro edition's lock on root. The command guards are always wired
// and check the lock each time they run, so a build without the lock behaves exactly as upstream;
// the seams below are only replaced when the lock is on for this process.
func applyLocalOnly(root *cobra.Command) {
	guardLocalOnlyCommands(root)
	if !localonly.Enforced() {
		return
	}

	// "A build distributed through another channel sets it to false, which turns off the startup
	// notice, `beacon version check`, and the endpoint self-updater" (updatecheck/source.go).
	updatecheck.PublicReleases = false
	rootStartUpdateRefresh = func() error { return nil }

	// Pending, so a signup an earlier build queued is neither sent nor rewritten on disk.
	onboardingSend = func(context.Context, onboarding.Submission) (string, error) {
		return onboarding.OutcomePending, localonly.Refuse("Onboarding signup", "it sends an email address and machine details to Asymptote Labs")
	}
	connectManagedEndpoint = func(context.Context, asymptote.ConnectOptions) (*asymptote.ConnectResult, error) {
		return nil, localonly.Refuse("Beacon Cloud connect", whyBeaconCloud)
	}
	locateEnterpriseRelease = func(context.Context, *http.Client, string, string, string) (edition.Release, error) {
		return edition.Release{}, localonly.Refuse("Enterprise edition download", "it downloads a closed build from beacon.sh")
	}
	installEnterpriseRelease = func(context.Context, edition.Release, string, string) (selfupdate.ApplyResult, error) {
		return selfupdate.ApplyResult{}, localonly.Refuse("Enterprise edition install", "it installs a closed build from beacon.sh")
	}
	shareAutoCloud = func() (bool, string) { return false, "" }

	if grok, ok := autoEndpointTargets["grok"]; ok {
		grok.OTLP, grok.Hooks, grok.Skip = nil, nil, "the Grok engine is on hold in the nuro edition"
		autoEndpointTargets["grok"] = grok
	}
	runners := endpointBackfillRunners
	endpointBackfillRunners = func() []endpointBackfillRunner {
		var kept []endpointBackfillRunner
		for _, r := range runners() {
			if !localonly.Held(strings.Fields(r.Name)[0]) {
				kept = append(kept, r)
			}
		}
		return kept
	}

	launch := handoffLaunch
	handoffLaunch = func(plan handoff.Plan, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
		switch {
		case localonly.Held(plan.Target):
			return -1, localonly.Refuse("Resuming in "+plan.Target, whyHeldEngine)
		case plan.BriefPath != "" && !claudeRuntime(plan.Target):
			return -1, localonly.Refuse("A handoff brief for "+plan.Target, whyOtherVendors)
		}
		return launch(plan, stdin, stdout, stderr)
	}

	lockDefaultTransport()
}

// localOnlyGuarded marks a command whose pre-run is already wrapped, so wiring twice is harmless.
const localOnlyGuarded = "beacon.localonly.guarded"

// guardLocalOnlyCommands wraps the pre-run of every command a rule covers.
func guardLocalOnlyCommands(root *cobra.Command) {
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		if rules := localOnlyRulesFor(commandPathBelowRoot(c)); len(rules) > 0 && c.Runnable() && c.Annotations[localOnlyGuarded] == "" {
			if c.Annotations == nil {
				c.Annotations = map[string]string{}
			}
			c.Annotations[localOnlyGuarded] = "true"
			preRunE, preRun := c.PreRunE, c.PreRun
			c.PreRun = nil
			c.PreRunE = func(cmd *cobra.Command, args []string) error {
				if err := checkLocalOnly(cmd, rules); err != nil {
					return err
				}
				if preRunE != nil {
					return preRunE(cmd, args)
				}
				if preRun != nil {
					preRun(cmd, args)
				}
				return nil
			}
		}
		for _, child := range c.Commands() {
			walk(child)
		}
	}
	walk(root)
}

func checkLocalOnly(cmd *cobra.Command, rules []localOnlyRule) error {
	if !localonly.Enforced() {
		return nil
	}
	for _, rule := range rules {
		if rule.when == nil || rule.when(cmd) {
			return localonly.Refuse("`beacon "+commandPathBelowRoot(cmd)+"`", rule.why)
		}
	}
	return nil
}

func localOnlyRulesFor(path string) []localOnlyRule {
	var matched []localOnlyRule
	for _, rule := range localOnlyRules {
		if path == rule.path || (!rule.exact && strings.HasPrefix(path, rule.path+" ")) {
			matched = append(matched, rule)
		}
	}
	return matched
}

func commandPathBelowRoot(c *cobra.Command) string {
	var names []string
	for ; c != nil && c.HasParent(); c = c.Parent() {
		names = append([]string{c.Name()}, names...)
	}
	return strings.Join(names, " ")
}

func flagValue(c *cobra.Command, name string) string {
	f := c.Flags().Lookup(name)
	if f == nil {
		return ""
	}
	return strings.TrimSpace(f.Value.String())
}

func flagTrue(name string) func(*cobra.Command) bool {
	return func(c *cobra.Command) bool {
		f := c.Flags().Lookup(name)
		return f != nil && f.Changed && f.Value.String() == "true"
	}
}

func anyFlagChanged(names ...string) func(*cobra.Command) bool {
	return func(c *cobra.Command) bool {
		for _, name := range names {
			if f := c.Flags().Lookup(name); f != nil && f.Changed {
				return true
			}
		}
		return false
	}
}

// flagPrefixChanged reports whether any flag whose name starts with one of prefixes was set.
func flagPrefixChanged(prefixes ...string) func(*cobra.Command) bool {
	return func(c *cobra.Command) bool {
		changed := false
		c.Flags().Visit(func(f *pflag.Flag) {
			for _, prefix := range prefixes {
				if strings.HasPrefix(f.Name, prefix) {
					changed = true
				}
			}
		})
		return changed
	}
}

func harnessFlagIs(name, value string) func(*cobra.Command) bool {
	return func(c *cobra.Command) bool {
		for _, v := range splitCSV(flagValue(c, name)) {
			if strings.EqualFold(strings.TrimSpace(v), value) {
				return true
			}
		}
		return false
	}
}

func harnessFlagHeld(name string) func(*cobra.Command) bool {
	return func(c *cobra.Command) bool {
		for _, v := range splitCSV(flagValue(c, name)) {
			if localonly.Held(v) {
				return true
			}
		}
		return false
	}
}

func claudeRuntime(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "claude", "claude_code", "claude-code":
		return true
	}
	return false
}

// lockDefaultTransport makes every client built on http.DefaultTransport refuse to dial anything
// but loopback, and drops its proxy so a loopback request is never routed off the machine. It is a
// second line behind the command rules, for egress the rules did not name.
func lockDefaultTransport() {
	t, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return
	}
	dial := t.DialContext
	if dial == nil {
		dial = (&net.Dialer{}).DialContext
	}
	t.Proxy = nil
	t.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		target, ok := loopbackDialAddr(addr)
		if !ok {
			host, _, err := net.SplitHostPort(addr)
			if err != nil {
				host = addr
			}
			return nil, localonly.Refuse("A network connection to "+host, whySendsOff)
		}
		return dial(ctx, network, target)
	}
}

// loopbackDialAddr returns the address to dial for addr when it is on this machine. "localhost" is
// dialed as 127.0.0.1 rather than resolved, so a hosts file or DNS answer cannot point it elsewhere.
func loopbackDialAddr(addr string) (string, bool) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "", false
	}
	if strings.EqualFold(host, "localhost") {
		return net.JoinHostPort("127.0.0.1", port), true
	}
	ip := net.ParseIP(host)
	return addr, ip != nil && ip.IsLoopback()
}

// pinLoopbackHost writes a localhost URL with 127.0.0.1 in its place, so a program Beacon configures
// does not resolve the name itself; any other URL comes back as it was.
func pinLoopbackHost(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || !strings.EqualFold(u.Hostname(), "localhost") {
		return raw
	}
	if port := u.Port(); port != "" {
		u.Host = net.JoinHostPort("127.0.0.1", port)
	} else {
		u.Host = "127.0.0.1"
	}
	return u.String()
}

// loopbackURL reports whether raw names a loopback IP address or localhost.
func loopbackURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return false
	}
	host := u.Hostname()
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
