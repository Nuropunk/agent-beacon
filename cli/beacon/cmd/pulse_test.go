package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/pulse"
)

func runPulseWith(t *testing.T, opts pulseOptions) (string, pulse.Pulse) {
	t.Helper()
	prev := pulseOpts
	t.Cleanup(func() { pulseOpts = prev })
	pulseOpts = opts
	var out bytes.Buffer
	pulseCmd.SetOut(&out)
	t.Cleanup(func() { pulseCmd.SetOut(nil) })
	if err := runPulse(pulseCmd, nil); err != nil {
		t.Fatal(err)
	}
	raw := out.String()
	if opts.out != "" {
		data, err := os.ReadFile(opts.out)
		if err != nil {
			t.Fatal(err)
		}
		raw = string(data)
	}
	var p pulse.Pulse
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		t.Fatalf("pulse is not JSON: %v\n%s", err, raw)
	}
	return raw, p
}

func writePulseLog(t *testing.T) string {
	t.Helper()
	logPath := filepath.Join(t.TempDir(), "runtime.jsonl")
	log := `{"timestamp":"2026-10-20T11:00:00Z","event":{"fidelity":"observed"},"harness":{"name":"claude_code","collection_method":"hook"},"prompt":{"text":"PRIVATE"}}` + "\n"
	if err := os.WriteFile(logPath, []byte(log), 0o600); err != nil {
		t.Fatal(err)
	}
	return logPath
}

func TestPulseCommandWritesAPrivateFile(t *testing.T) {
	t.Setenv(pulseMachineEnv, "")
	out := filepath.Join(t.TempDir(), "nested", "pulse.json")
	raw, p := runPulseWith(t, pulseOptions{userMode: true, logPath: writePulseLog(t), machine: "m4", out: out, now: "2026-10-20T12:00:00Z"})
	info, err := os.Stat(out)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("pulse file mode = %v, want 0600", info.Mode().Perm())
	}
	if p.Machine != "m4" || len(p.Harnesses) == 0 || p.Harnesses[0].Name != "claude_code" || p.Harnesses[0].Grade != pulse.GradeLive {
		t.Errorf("pulse = %+v", p)
	}
	host, _ := os.Hostname()
	if strings.Contains(raw, "PRIVATE") || (host != "" && strings.Contains(raw, host)) {
		t.Errorf("pulse carries content or the hostname: %s", raw)
	}
}

func TestPulseCommandListsTheHeldEngineInTheNuroEdition(t *testing.T) {
	lockNuroEdition(t)
	t.Setenv(pulseMachineEnv, "studio")
	_, p := runPulseWith(t, pulseOptions{userMode: true, logPath: writePulseLog(t), now: "2026-10-20T12:00:00Z"})
	if p.Machine != "studio" || p.Beacon.Edition != "nuro" {
		t.Errorf("pulse header = %+v", p)
	}
	held := false
	for _, h := range p.Harnesses {
		if strings.HasPrefix(h.Name, "grok") {
			held = h.Grade == pulse.GradeHeld
		}
	}
	if !held {
		t.Errorf("the held engine is missing or not HELD: %+v", p.Harnesses)
	}
}
