// Package localonly is the lock the nuro edition of Beacon is built with. Under it, nothing Beacon
// captures leaves the machine, the held Grok engine is neither executed nor configured, and private
// traces are not handed to another vendor's agent. Commands and code paths that would do any of
// those refuse with ErrRefused instead of running.
//
// The lock is turned on, never off, at run time: the nuro edition sets it at link time
// (-X .../internal/version.Edition=nuro), and BEACON_LOCAL_ONLY=1 turns the same lock on for any
// other build. Nothing in the environment can lift it from a nuro build; that takes a rebuild,
// which is the explicit step the owner's go has to name.
package localonly

import (
	"fmt"
	"os"
	"strings"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/version"
)

// Edition is the version.Edition value that turns the lock on.
const Edition = "nuro"

// Env turns the lock on for a build of any edition when set to 1, true, yes or on.
const Env = "BEACON_LOCAL_ONLY"

// HeldEngines are harness names the lock keeps on hold: Beacon neither runs their binaries for
// discovery nor installs hooks, syncs sessions or backfills from them.
var HeldEngines = []string{"grok", "grok_bot"}

// Enforced reports whether this process runs under the lock.
func Enforced() bool {
	if version.Edition == Edition {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(os.Getenv(Env))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// Held reports whether name, a harness or install-target name in any spelling Beacon accepts,
// is an engine the lock keeps on hold.
func Held(name string) bool {
	key := strings.ToLower(strings.TrimSpace(name))
	key = strings.ReplaceAll(key, "-", "_")
	for _, held := range HeldEngines {
		if key == held || key == held+"_build" {
			return true
		}
	}
	return false
}

// ErrRefused is what a locked path returns. What names the command or action; Why says what it
// would have done.
type ErrRefused struct {
	What string
	Why  string
}

func (e *ErrRefused) Error() string {
	return fmt.Sprintf("%s is off in the nuro edition of Beacon: %s. Nothing leaves this machine, "+
		"the Grok engine stays held, and private traces stay with Claude. Turning it on takes the "+
		"owner's explicit go and a build without the lock", e.What, e.Why)
}

// Refuse returns an ErrRefused for what, explained by why.
func Refuse(what, why string) error {
	return &ErrRefused{What: what, Why: why}
}
