package provider

import (
	"fmt"
	"strings"
)

const providerIDPrefix = ProviderName + "://"

// ProviderID is a node's identity at Ankra Cloud: ankracloud://<zone>/<server-id>.
type ProviderID struct {
	Zone     string
	ServerID string
}

// String formats the providerID.
func (identifier ProviderID) String() string {
	return providerIDPrefix + identifier.Zone + "/" + identifier.ServerID
}

// ParseProviderID reads ankracloud://<zone>/<server-id>.
func ParseProviderID(value string) (ProviderID, error) {
	rest, hasPrefix := strings.CutPrefix(value, providerIDPrefix)
	if !hasPrefix {
		return ProviderID{}, fmt.Errorf("providerID %q does not start with %s", value, providerIDPrefix)
	}
	zone, serverID, hasSeparator := strings.Cut(rest, "/")
	if !hasSeparator || zone == "" || serverID == "" || strings.Contains(serverID, "/") {
		return ProviderID{}, fmt.Errorf("providerID %q is not %s<zone>/<server-id>", value, providerIDPrefix)
	}
	return ProviderID{Zone: zone, ServerID: serverID}, nil
}
