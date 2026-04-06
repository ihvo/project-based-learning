package client

import (
	"crypto/rand"
	"fmt"
	"strings"
)

// ClientID is the two-letter Azureus-style identifier for Peer Pressure.
const ClientID = "PP"

// Version is the current Peer Pressure release (semver).
const Version = "0.1.0"

// GeneratePeerID creates a 20-byte Azureus-style peer ID:
//
//	-PP<MJMNPTPT>-<12 random bytes>
func GeneratePeerID() [20]byte {
	var id [20]byte
	copy(id[:], FormatVersionPrefix(Version))
	if _, err := rand.Read(id[8:]); err != nil {
		panic("crypto/rand failed: " + err.Error())
	}
	return id
}

// FormatVersionPrefix builds the 8-byte prefix from a version string.
// "0.1.0" → "-PP0100-", "1.2.3" → "-PP1230-".
func FormatVersionPrefix(version string) string {
	parts := strings.SplitN(version, ".", 3)
	var digits [4]byte
	for i := range 3 {
		if i < len(parts) && len(parts[i]) > 0 {
			digits[i] = parts[i][0]
		} else {
			digits[i] = '0'
		}
	}
	digits[3] = '0'
	return fmt.Sprintf("-%s%c%c%c%c-", ClientID, digits[0], digits[1], digits[2], digits[3])
}

// knownClients maps Azureus-style two-letter codes to human-readable names.
var knownClients = map[string]string{
	"PP": "Peer Pressure",
	"qB": "qBittorrent",
	"TR": "Transmission",
	"DE": "Deluge",
	"AZ": "Vuze",
	"UT": "µTorrent",
	"lt": "libtorrent",
	"LT": "libtorrent (Rasterbar)",
	"BI": "BiglyBT",
}

// ParsePeerID extracts the client name and version from a 20-byte peer ID.
// Returns (clientName, versionStr, ok). ok is false for unrecognized formats.
func ParsePeerID(id [20]byte) (clientName string, version string, ok bool) {
	if id[0] == '-' && id[7] == '-' {
		code := string(id[1:3])
		ver := string(id[3:7])
		name, known := knownClients[code]
		if !known {
			name = "Unknown (" + code + ")"
		}
		return name, ver, true
	}
	return "", "", false
}

// UserAgent returns a human-readable client string for extension handshakes.
func UserAgent() string {
	return "Peer Pressure " + Version
}
