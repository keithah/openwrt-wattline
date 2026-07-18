// Package discovery publishes the cached Wattline identity on LAN interfaces.
package discovery

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/keithah/openwrt-wattline/internal/config"
)

// Metadata is the complete versioned DNS-SD TXT payload. Numeric zero values
// for CID and Features mean that the corresponding BLE value is not known yet.
type Metadata struct {
	Version  string
	ID       string
	Model    string
	TLS      string
	CID      uint16
	Features uint32
	API      int
}

// TXT returns fields in the normative API order. An empty device ID suppresses
// publication, because clients use that ID to correlate LAN and VPN sightings.
func TXT(metadata Metadata) []string {
	if strings.TrimSpace(metadata.ID) == "" {
		return nil
	}
	cid, features := "", ""
	if metadata.CID != 0 {
		cid = fmt.Sprintf("%04x", metadata.CID)
	}
	if metadata.Features != 0 {
		features = fmt.Sprintf("%08x", metadata.Features)
	}
	tls := strings.ToLower(metadata.TLS)
	if tls == "" {
		tls = "none"
	}
	return []string{
		"ver=" + metadata.Version,
		"api=" + strconv.Itoa(metadata.API),
		"id=" + metadata.ID,
		"model=" + metadata.Model,
		"cid=" + cid,
		"features=" + features,
		"tls=" + tls,
		"auth=pin",
	}
}

// PreferredPort returns the advertised API port. HTTPS is authoritative when
// enabled; HTTP is the compatibility fallback.
func PreferredPort(cfg config.Config) int {
	if cfg.HTTPSEnabled {
		return cfg.HTTPSPort
	}
	if cfg.HTTPEnabled {
		return cfg.HTTPPort
	}
	return 0
}
