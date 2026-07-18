package discovery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"time"
)

const defaultUBusTimeout = time.Second

// LANMembershipSource is the authoritative boundary between configured mDNS
// selectors and interfaces OpenWrt currently assigns to its LAN network.
type LANMembershipSource interface {
	LANInterfaces() ([]string, error)
}

// ParseOpenWrtLANStatus extracts only OpenWrt's authoritative LAN device names.
func ParseOpenWrtLANStatus(raw []byte) ([]string, error) {
	var status struct {
		Up       *bool  `json:"up"`
		Device   string `json:"device"`
		L3Device string `json:"l3_device"`
	}
	if err := json.Unmarshal(raw, &status); err != nil {
		return nil, fmt.Errorf("decode ubus LAN status: %w", err)
	}
	if status.Up == nil || !*status.Up {
		return nil, errors.New("OpenWrt LAN interface is not explicitly up")
	}
	seen := make(map[string]struct{}, 2)
	for _, name := range []string{strings.TrimSpace(status.Device), strings.TrimSpace(status.L3Device)} {
		if name == "" || strings.ContainsAny(name, " \t\r\n/\\") {
			continue
		}
		seen[name] = struct{}{}
	}
	if len(seen) == 0 {
		return nil, errors.New("ubus LAN status has no device")
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}

// OpenWrtLANMembership queries the built-in ubus network model. Any lookup,
// execution, timeout, or decoding failure is returned so discovery fails closed.
type OpenWrtLANMembership struct {
	LookPath func(string) (string, error)
	Run      func(context.Context, string, ...string) ([]byte, error)
	Timeout  time.Duration
}

func (membership OpenWrtLANMembership) LANInterfaces() ([]string, error) {
	lookup := membership.LookPath
	if lookup == nil {
		lookup = exec.LookPath
	}
	executable, err := lookup("ubus")
	if err != nil {
		return nil, fmt.Errorf("find ubus: %w", err)
	}
	timeout := membership.Timeout
	if timeout <= 0 {
		timeout = defaultUBusTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	run := membership.Run
	if run == nil {
		run = runBoundedCommand
	}
	raw, err := run(ctx, executable, "-S", "call", "network.interface.lan", "status")
	if err != nil {
		return nil, fmt.Errorf("query ubus LAN status: %w", err)
	}
	return ParseOpenWrtLANStatus(raw)
}
