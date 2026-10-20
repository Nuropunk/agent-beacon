package cloudshuttle

import (
	"os"
	"strings"

	"github.com/asymptote-labs/agent-beacon/cli/beacon-hooks/internal/version"
)

// localOnly reports whether this hook runs under the nuro edition's lock, decided the way the beacon
// CLI's internal/localonly decides it: the nuro edition at link time, or BEACON_LOCAL_ONLY turned on.
// Under the lock the shuttle never uploads, whatever the environment configures.
func localOnly() bool {
	if version.Edition == "nuro" {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(os.Getenv("BEACON_LOCAL_ONLY"))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}
