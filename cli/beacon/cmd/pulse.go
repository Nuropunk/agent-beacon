package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/atomicfile"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/lifecycle"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/localonly"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/pulse"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/version"
)

// pulseMachineEnv names the machine in a pulse when --machine is not given. A pulse never reads
// the hostname: the alias is whatever the person chose to call this machine.
const pulseMachineEnv = "BEACON_PULSE_MACHINE"

type pulseOptions struct {
	userMode   bool
	systemMode bool
	logPath    string
	machine    string
	out        string
	expect     []string
	now        string
}

var pulseOpts pulseOptions

var pulseCmd = &cobra.Command{
	Use:   "pulse",
	Short: "Report which agent runtimes Beacon has seen lately, as metadata-only JSON",
	Long: `Report which agent runtimes Beacon has seen lately.

For each runtime in the runtime log, the pulse gives the time of its last event, event counts for
the last 6 and 24 hours (split by observed and inferred fidelity), an hourly histogram, and a grade:
LIVE (an event within 6 hours), STALE (within 24 hours), DARK (older, or named with --expect and
never seen) or HELD (on hold in the nuro edition). The grade comes only from events Beacon wrote;
a runtime that sent nothing is never reported alive.

A pulse holds harness names, times, counts and collection methods only: no prompts, commands,
paths, users, hostnames or repositories. It is printed to stdout or, with --out, written to a file
(0600). It never leaves the machine on its own.`,
	Args:         cobra.NoArgs,
	SilenceUsage: true,
	RunE:         runPulse,
}

func runPulse(cmd *cobra.Command, _ []string) error {
	userMode := pulseOpts.userMode && !pulseOpts.systemMode
	runtimeLog := lifecycle.ResolveRuntimeLog(userMode, pulseOpts.logPath)
	opts := pulse.Options{
		Machine: firstNonEmpty(pulseOpts.machine, os.Getenv(pulseMachineEnv)),
		Build:   pulse.Build{Version: version.Version, Edition: version.Edition},
		Expect:  splitCSV(strings.Join(pulseOpts.expect, ",")),
	}
	if localonly.Enforced() {
		opts.Held = localonly.Held
		opts.Expect = append(opts.Expect, localonly.HeldEngines[0])
	}
	if now := strings.TrimSpace(pulseOpts.now); now != "" {
		parsed, err := time.Parse(time.RFC3339, now)
		if err != nil {
			return fmt.Errorf("--now: %w", err)
		}
		opts.Now = parsed
	}
	p, err := pulse.FromLog(runtimeLog.EffectiveLogPath, opts)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if pulseOpts.out == "" {
		_, err = cmd.OutOrStdout().Write(data)
		return err
	}
	if err := os.MkdirAll(filepath.Dir(pulseOpts.out), 0o700); err != nil {
		return err
	}
	return atomicfile.Write(pulseOpts.out, data, 0o600)
}

func init() {
	rootCmd.AddCommand(pulseCmd)
	f := pulseCmd.Flags()
	f.BoolVar(&pulseOpts.userMode, "user", true, "Use per-user endpoint paths")
	f.BoolVar(&pulseOpts.systemMode, "system", false, "Use system endpoint paths")
	f.StringVar(&pulseOpts.logPath, "log-path", "", "Runtime JSONL log path (defaults to the local runtime log)")
	f.StringVar(&pulseOpts.machine, "machine", "", "Name for this machine in the pulse (default $"+pulseMachineEnv+"; the hostname is never used)")
	f.StringVar(&pulseOpts.out, "out", "", "Write the pulse to this file (0600) instead of stdout")
	f.StringSliceVar(&pulseOpts.expect, "expect", nil, "Runtimes to list even with no events, graded DARK (comma-separated)")
	f.StringVar(&pulseOpts.now, "now", "", "Grade as of this RFC3339 time instead of now")
	_ = f.MarkHidden("now")
}
