package pulse

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var now = time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)

func line(harness, method, fidelity string, at time.Time, extra string) string {
	return fmt.Sprintf(`{"timestamp":%q,"event":{"kind":"tool","action":"tool.invoked","fidelity":%q},"harness":{"name":%q,"collection_method":%q}%s}`,
		at.Format(time.RFC3339Nano), fidelity, harness, method, extra) + "\n"
}

func build(t *testing.T, log string, opts Options) Pulse {
	t.Helper()
	if opts.Now.IsZero() {
		opts.Now = now
	}
	p, err := FromReaders([]io.Reader{strings.NewReader(log)}, opts)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func harness(t *testing.T, p Pulse, name string) Harness {
	t.Helper()
	for _, h := range p.Harnesses {
		if h.Name == name {
			return h
		}
	}
	t.Fatalf("no %q in pulse: %+v", name, p.Harnesses)
	return Harness{}
}

func TestPulseGradesFromRealEventsOnly(t *testing.T) {
	log := line("claude_code", "hook", "observed", now.Add(-time.Hour), "") +
		line("claude", "otlp", "inferred", now.Add(-30*time.Minute), "") +
		line("codex", "otlp", "observed", now.Add(-10*time.Hour), "") +
		line("cursor", "hook", "observed", now.Add(-30*time.Hour), "") +
		line("grok", "hook", "observed", now.Add(-2*time.Hour), "") +
		"not json\n" + `{"timestamp":"yesterday","harness":{"name":"codex"}}` + "\n" + `{"timestamp":"2026-10-20T11:00:00Z"}` + "\n"
	p := build(t, log, Options{
		Machine: "m4",
		Build:   Build{Version: "v1", Edition: "nuro"},
		Expect:  []string{"gemini"},
		Held:    func(name string) bool { return strings.HasPrefix(name, "grok") },
	})

	if p.Schema != Schema || p.GeneratedAt != "2026-10-20T12:00:00Z" || p.Machine != "m4" || p.Beacon.Edition != "nuro" {
		t.Fatalf("header = %+v", p)
	}
	claude := harness(t, p, "claude_code")
	if claude.Grade != GradeLive || claude.Events24h != 2 || claude.Events6h != 2 || claude.Observed24h != 1 || claude.Inferred24h != 1 {
		t.Errorf("claude_code = %+v; both spellings should fold into one LIVE line", claude)
	}
	if claude.LastEventAt != "2026-10-20T11:30:00Z" || strings.Join(claude.Methods, ",") != "hook,otlp" {
		t.Errorf("claude_code last/methods = %q %v", claude.LastEventAt, claude.Methods)
	}
	if codex := harness(t, p, "codex_cli"); codex.Grade != GradeStale || codex.Events6h != 0 || codex.Events24h != 1 {
		t.Errorf("codex_cli = %+v, want STALE with one event in 24h", codex)
	}
	if cursor := harness(t, p, "cursor"); cursor.Grade != GradeDark || cursor.Events24h != 0 || cursor.LastEventAt == "" {
		t.Errorf("cursor = %+v, want DARK with its last event kept", cursor)
	}
	if gemini := harness(t, p, "gemini_cli"); gemini.Grade != GradeDark || gemini.LastEventAt != "" || gemini.Events24h != 0 {
		t.Errorf("gemini_cli = %+v, want an expected runtime never seen to read DARK", gemini)
	}
	for _, h := range p.Harnesses {
		if strings.HasPrefix(h.Name, "grok") && h.Grade != GradeHeld {
			t.Errorf("%s = %s, want HELD whatever its events say", h.Name, h.Grade)
		}
	}
}

func TestPulseGradeBoundaries(t *testing.T) {
	for _, tc := range []struct {
		age  time.Duration
		want string
	}{
		{0, GradeLive}, {LiveWithin, GradeLive}, {LiveWithin + time.Second, GradeStale},
		{StaleWithin, GradeStale}, {StaleWithin + time.Second, GradeDark}, {-time.Minute, GradeLive},
	} {
		p := build(t, line("codex", "otlp", "observed", now.Add(-tc.age), ""), Options{})
		if got := harness(t, p, "codex_cli").Grade; got != tc.want {
			t.Errorf("age %v: grade %s, want %s", tc.age, got, tc.want)
		}
	}
}

func TestPulseHourlyBuckets(t *testing.T) {
	log := line("codex", "otlp", "observed", now.Add(-30*time.Minute), "") +
		line("codex", "otlp", "observed", now.Add(-90*time.Minute), "") +
		line("codex", "otlp", "observed", now.Add(-(23*time.Hour+30*time.Minute)), "") +
		line("codex", "otlp", "observed", now.Add(-25*time.Hour), "")
	h := harness(t, build(t, log, Options{}), "codex_cli")
	if h.Hourly24h[23] != 1 || h.Hourly24h[22] != 1 || h.Hourly24h[0] != 1 {
		t.Errorf("hourly = %v", h.Hourly24h)
	}
	sum := 0
	for _, n := range h.Hourly24h {
		sum += n
	}
	if sum != h.Events24h || sum != 3 {
		t.Errorf("hourly sums to %d, events_24h %d, want 3", sum, h.Events24h)
	}
}

// The pulse is for sharing beyond the machine, so nothing but metadata may reach it.
func TestPulseCarriesNoContent(t *testing.T) {
	secret := `,"prompt":{"text":"SECRET-PROMPT"},"command":{"line":"cat /Users/someone/private.txt"},"user":{"name":"someone"},"endpoint":{"hostname":"secret-host"},"file":{"path":"/repo/secret-repo/x.go"},"raw":{"k":"SECRET-RAW"}`
	p := build(t, line("claude_code", "hook", "observed", now.Add(-time.Minute), secret), Options{})
	data, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{"SECRET", "someone", "secret-host", "secret-repo", "/Users", "private"} {
		if strings.Contains(string(data), leak) {
			t.Errorf("pulse carries %q: %s", leak, data)
		}
	}
}

func TestPulseReadsLongLines(t *testing.T) {
	big := `,"prompt":{"text":"` + strings.Repeat("x", 3<<20) + `"}`
	p := build(t, line("codex", "otlp", "observed", now.Add(-time.Minute), big), Options{})
	if harness(t, p, "codex_cli").Events24h != 1 {
		t.Fatal("a 3 MiB event line was dropped")
	}
}

func TestFromLogReadsRotations(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "runtime.jsonl")
	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("runtime.jsonl", line("codex", "otlp", "observed", now.Add(-time.Minute), ""))
	write("runtime.jsonl.1", line("codex", "otlp", "observed", now.Add(-2*time.Hour), ""))
	write("runtime.jsonl.2", line("cursor", "hook", "observed", now.Add(-3*time.Hour), ""))
	write("runtime.jsonl.lock", line("ignored", "hook", "observed", now, ""))
	write("runtime.jsonl.bak", line("ignored", "hook", "observed", now, ""))

	if files := Files(logPath); len(files) != 3 {
		t.Fatalf("Files = %v, want the log and its two numbered rotations", files)
	}
	p, err := FromLog(logPath, Options{Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Harnesses) != 2 || harness(t, p, "codex_cli").Events24h != 2 {
		t.Fatalf("pulse = %+v", p.Harnesses)
	}
	if _, err := FromLog(filepath.Join(dir, "missing.jsonl"), Options{Now: now}); err != nil {
		t.Fatalf("a missing log should read as empty: %v", err)
	}
}
