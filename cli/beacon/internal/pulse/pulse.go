// Package pulse summarizes a runtime log into a liveness pulse: for each harness, when Beacon last
// saw an event from it and how many it saw, graded LIVE, STALE, DARK or HELD.
//
// A pulse holds harness names, timestamps, counts, collection methods and fidelity, and nothing
// else. Prompts, responses, commands, file paths, users, hosts and repositories are never decoded
// into it, so a pulse is safe to hand to a status page or another machine where the person chooses
// to. A harness that sent nothing reads as missing or DARK, never as alive: the grade comes only from
// events Beacon actually wrote.
package pulse

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve"
)

// token matches the harness names and collection methods Beacon writes. A log line can carry any
// text in those fields, so anything else (a path, a sentence) is not copied into a pulse: a name is
// counted as "unknown" and a method is dropped.
var token = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,63}$`)

// Schema names the pulse document format.
const Schema = "beacon.pulse.v1"

// Grades, from the age of a harness's last event.
const (
	GradeLive  = "LIVE"  // an event within LiveWithin
	GradeStale = "STALE" // the last event within StaleWithin
	GradeDark  = "DARK"  // older than StaleWithin, or expected and never seen
	GradeHeld  = "HELD"  // on hold by policy, whatever its events say
)

// LiveWithin and StaleWithin are the grade thresholds.
const (
	LiveWithin  = 6 * time.Hour
	StaleWithin = 24 * time.Hour
)

// Pulse is one machine's liveness summary.
type Pulse struct {
	Schema      string    `json:"schema"`
	GeneratedAt string    `json:"generated_at"`
	Machine     string    `json:"machine,omitempty"`
	Beacon      Build     `json:"beacon"`
	Harnesses   []Harness `json:"harnesses"`
}

// Build names the Beacon build that wrote the pulse.
type Build struct {
	Version string `json:"version"`
	Edition string `json:"edition"`
}

// Harness is one runtime's line in a pulse.
type Harness struct {
	Name        string `json:"name"`
	Grade       string `json:"grade"`
	LastEventAt string `json:"last_event_at,omitempty"`
	Events6h    int    `json:"events_6h"`
	Events24h   int    `json:"events_24h"`
	// Observed24h and Inferred24h split Events24h by event.fidelity; events with neither value
	// count in Events24h only.
	Observed24h int      `json:"observed_24h"`
	Inferred24h int      `json:"inferred_24h"`
	Methods     []string `json:"collection_methods,omitempty"`
	// Hourly24h counts the last 24 hours of events, oldest hour first; the last bucket is the hour
	// ending at GeneratedAt.
	Hourly24h [24]int `json:"hourly_24h"`
}

// Options shape a pulse.
type Options struct {
	Now     time.Time
	Machine string
	Build   Build
	// Expect lists harnesses that get a line even with no events, graded DARK (or HELD).
	Expect []string
	// Held reports harnesses on hold; nil holds none.
	Held func(name string) bool
}

type tally struct {
	last    time.Time
	h       Harness
	methods map[string]bool
}

// FromLog builds a pulse from the runtime log at logPath and its numbered rotations
// (logPath.1, logPath.2, ...). Missing files read as empty.
func FromLog(logPath string, opts Options) (Pulse, error) {
	var readers []io.Reader
	for _, path := range Files(logPath) {
		f, err := os.Open(path)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return Pulse{}, err
		}
		defer f.Close()
		readers = append(readers, f)
	}
	return FromReaders(readers, opts)
}

// Files lists logPath and its numbered rotations, the files a pulse reads.
func Files(logPath string) []string {
	if logPath == "" {
		return nil
	}
	files := []string{logPath}
	entries, err := os.ReadDir(filepath.Dir(logPath))
	if err != nil {
		return files
	}
	prefix := filepath.Base(logPath) + "."
	for _, entry := range entries {
		suffix, ok := strings.CutPrefix(entry.Name(), prefix)
		if !ok {
			continue
		}
		if n, err := strconv.Atoi(suffix); err == nil && n > 0 {
			files = append(files, filepath.Join(filepath.Dir(logPath), entry.Name()))
		}
	}
	return files
}

// FromReaders builds a pulse from runtime log lines. Lines that are not events are skipped.
func FromReaders(readers []io.Reader, opts Options) (Pulse, error) {
	now := opts.Now.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	held := opts.Held
	if held == nil {
		held = func(string) bool { return false }
	}
	tallies := map[string]*tally{}
	get := func(name string) *tally {
		t := tallies[name]
		if t == nil {
			t = &tally{h: Harness{Name: name}, methods: map[string]bool{}}
			tallies[name] = t
		}
		return t
	}

	for _, r := range readers {
		br := bufio.NewReaderSize(r, 64*1024)
		for {
			line, err := br.ReadBytes('\n')
			if len(line) > 0 {
				count(get, line, now)
			}
			if err == io.EOF {
				break
			}
			if err != nil {
				return Pulse{}, err
			}
		}
	}
	for _, name := range opts.Expect {
		if name = strings.ToLower(asymptoteobserve.NormalizeHarnessName(name)); token.MatchString(name) {
			get(name)
		}
	}

	p := Pulse{Schema: Schema, GeneratedAt: now.Format(time.RFC3339), Machine: strings.TrimSpace(opts.Machine), Beacon: opts.Build}
	p.Harnesses = []Harness{}
	for name, t := range tallies {
		h := t.h
		if !t.last.IsZero() {
			h.LastEventAt = t.last.Format(time.RFC3339)
		}
		for m := range t.methods {
			h.Methods = append(h.Methods, m)
		}
		sort.Strings(h.Methods)
		switch age := now.Sub(t.last); {
		case held(name):
			h.Grade = GradeHeld
		case t.last.IsZero():
			h.Grade = GradeDark
		case age <= LiveWithin:
			h.Grade = GradeLive
		case age <= StaleWithin:
			h.Grade = GradeStale
		default:
			h.Grade = GradeDark
		}
		p.Harnesses = append(p.Harnesses, h)
	}
	sort.Slice(p.Harnesses, func(i, j int) bool { return p.Harnesses[i].Name < p.Harnesses[j].Name })
	return p, nil
}

// count folds one log line into its harness's tally. Only the four metadata fields are decoded.
func count(get func(string) *tally, line []byte, now time.Time) {
	var event struct {
		Timestamp string `json:"timestamp"`
		Event     struct {
			Fidelity string `json:"fidelity"`
		} `json:"event"`
		Harness struct {
			Name             string `json:"name"`
			CollectionMethod string `json:"collection_method"`
		} `json:"harness"`
	}
	if err := json.Unmarshal(line, &event); err != nil {
		return
	}
	name := strings.ToLower(asymptoteobserve.NormalizeHarnessName(event.Harness.Name))
	ts, err := time.Parse(time.RFC3339Nano, event.Timestamp)
	if name == "" || err != nil {
		return
	}
	if !token.MatchString(name) {
		name = "unknown"
	}
	t := get(name)
	if ts.After(t.last) {
		t.last = ts.UTC()
	}
	if m := strings.ToLower(strings.TrimSpace(event.Harness.CollectionMethod)); token.MatchString(m) {
		t.methods[m] = true
	}
	age := now.Sub(ts)
	if age < 0 {
		age = 0 // a clock slightly ahead counts as now
	}
	if age >= StaleWithin {
		return
	}
	t.h.Events24h++
	if age < LiveWithin {
		t.h.Events6h++
	}
	t.h.Hourly24h[23-int(age/time.Hour)]++
	switch event.Event.Fidelity {
	case "observed":
		t.h.Observed24h++
	case "inferred":
		t.h.Inferred24h++
	}
}
