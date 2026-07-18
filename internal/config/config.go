package config

import (
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/robfig/cron/v3"
)

// cronParser mirrors the 5-field spec used by internal/rules; declared
// independently here (rather than importing internal/rules) to avoid an
// import cycle (rules imports config).
var cronParser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)

type Rule struct {
	Name             string        `json:"name"`
	Enabled          bool          `json:"enabled"`
	Condition        string        `json:"condition"` // input_power|battery_level|schedule|port_power
	State            string        `json:"state,omitempty"`
	Op               string        `json:"op,omitempty"` // below|above
	Port             string        `json:"port,omitempty"`
	Percent          int           `json:"percent,omitempty"`
	Watts            float64       `json:"watts,omitempty"`
	Hold             time.Duration `json:"hold"`
	RepeatEvery      time.Duration `json:"repeat_every,omitempty"`
	HysteresisMargin int           `json:"hysteresis_margin"`
	Cron             string        `json:"cron,omitempty"`
	Actions          []string      `json:"actions"`
	ConfirmShutdown  bool          `json:"confirm_shutdown"`
}

type Config struct {
	DeviceMAC string
	PIN       string
	Token     string
	Port      int
	LANAPI    bool

	BLEPIN          string
	HTTPEnabled     bool
	HTTPAddr4       string
	HTTPAddr6       string
	HTTPPort        int
	HTTPSEnabled    bool
	HTTPSAddr4      string
	HTTPSAddr6      string
	HTTPSPort       int
	TLSCert         string
	TLSKey          string
	TokenStore      string
	PairingTTL      time.Duration
	PairingAlwaysOn bool
	Advanced        bool
	MDNSEnabled     bool
	WANAccess       bool
	MDNSInterfaces  []string

	Rules []Rule
}

type ListenerSettings struct {
	Enabled bool   `json:"enabled"`
	Addr4   string `json:"addr4"`
	Addr6   string `json:"addr6"`
	Port    int    `json:"port"`
}

type TLSSettings struct {
	Cert string `json:"cert"`
	Key  string `json:"key"`
}

type MDNSSettings struct {
	Enabled    bool     `json:"enabled"`
	Interfaces []string `json:"interfaces"`
}

// SettingsView is safe to return from an administrative settings endpoint. It
// contains paths and policy, but never the bootstrap bearer token or file data.
type SettingsView struct {
	HTTP            ListenerSettings `json:"http"`
	HTTPS           ListenerSettings `json:"https"`
	TLS             TLSSettings      `json:"tls"`
	TokenStore      string           `json:"token_store"`
	PairingTTL      string           `json:"pairing_ttl"`
	PairingAlwaysOn bool             `json:"pairing_always_on"`
	Advanced        bool             `json:"advanced"`
	MDNS            MDNSSettings     `json:"mdns"`
	WANAccess       bool             `json:"wan_access"`
	BLEPIN          string           `json:"ble_pin"`
}

func (c *Config) SettingsView() SettingsView {
	return SettingsView{
		HTTP:  ListenerSettings{Enabled: c.HTTPEnabled, Addr4: c.HTTPAddr4, Addr6: c.HTTPAddr6, Port: c.HTTPPort},
		HTTPS: ListenerSettings{Enabled: c.HTTPSEnabled, Addr4: c.HTTPSAddr4, Addr6: c.HTTPSAddr6, Port: c.HTTPSPort},
		TLS:   TLSSettings{Cert: c.TLSCert, Key: c.TLSKey}, TokenStore: c.TokenStore,
		PairingTTL: c.PairingTTL.String(), PairingAlwaysOn: c.PairingAlwaysOn, Advanced: c.Advanced,
		MDNS:      MDNSSettings{Enabled: c.MDNSEnabled, Interfaces: append([]string{}, c.MDNSInterfaces...)},
		WANAccess: c.WANAccess, BLEPIN: c.BLEPIN,
	}
}

func hasShutdown(actions []string) bool {
	for _, a := range actions {
		if a == "shutdown" {
			return true
		}
	}
	return false
}

// Validate returns an error describing why the rule is unusable.
func (r *Rule) Validate() error {
	if r.Name == "" {
		return fmt.Errorf("rule name is required")
	}
	switch r.Condition {
	case "input_power":
		if r.State != "present" && r.State != "absent" {
			return fmt.Errorf("input_power needs state present|absent")
		}
	case "battery_level":
		if r.Op != "below" && r.Op != "above" {
			return fmt.Errorf("battery_level needs op below|above")
		}
	case "port_power":
		if (r.Op != "below" && r.Op != "above") || (r.Port != "dc" && r.Port != "usbc") {
			return fmt.Errorf("port_power needs op below|above and port dc|usbc")
		}
	case "schedule":
		if r.Cron == "" {
			return fmt.Errorf("schedule needs cron expression")
		}
		if _, err := cronParser.Parse(r.Cron); err != nil {
			return fmt.Errorf("invalid cron %q: %w", r.Cron, err)
		}
	default:
		return fmt.Errorf("unknown condition %q", r.Condition)
	}
	if len(r.Actions) == 0 {
		return fmt.Errorf("rule has no actions")
	}
	if hasShutdown(r.Actions) && !r.ConfirmShutdown {
		return fmt.Errorf("shutdown action requires confirm_shutdown '1'")
	}
	return nil
}

func sectionToRule(s *UCISection) (Rule, error) {
	r := Rule{Name: s.Name, HysteresisMargin: 5}
	r.Enabled = s.Options["enabled"] == "1"
	r.Condition = s.Options["condition"]
	r.State = s.Options["state"]
	r.Op = s.Options["op"]
	r.Port = s.Options["port"]
	r.Cron = s.Options["cron"]
	r.ConfirmShutdown = s.Options["confirm_shutdown"] == "1"
	r.Actions = s.Lists["action"]
	if v := s.Options["percent"]; v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return r, fmt.Errorf("percent: %w", err)
		}
		r.Percent = n
	}
	if v := s.Options["watts"]; v != "" {
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return r, fmt.Errorf("watts: %w", err)
		}
		r.Watts = f
	}
	if v := s.Options["hysteresis_margin"]; v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return r, fmt.Errorf("hysteresis_margin: %w", err)
		}
		r.HysteresisMargin = n
	}
	for opt, dst := range map[string]*time.Duration{"hold": &r.Hold, "repeat_every": &r.RepeatEvery} {
		if v := s.Options[opt]; v != "" {
			d, err := time.ParseDuration(v)
			if err != nil {
				return r, fmt.Errorf("%s: %w", opt, err)
			}
			*dst = d
		}
	}
	if err := r.Validate(); err != nil {
		return r, err
	}
	return r, nil
}

func ruleToSection(r Rule) *UCISection {
	s := newSection("rule", r.Name)
	set := func(k, v string) {
		if v != "" {
			s.Options[k] = v
		}
	}
	if r.Enabled {
		s.Options["enabled"] = "1"
	} else {
		s.Options["enabled"] = "0"
	}
	set("condition", r.Condition)
	set("state", r.State)
	set("op", r.Op)
	set("port", r.Port)
	set("cron", r.Cron)
	if r.Percent != 0 {
		s.Options["percent"] = strconv.Itoa(r.Percent)
	}
	if r.Watts != 0 {
		s.Options["watts"] = strconv.FormatFloat(r.Watts, 'f', -1, 64)
	}
	if r.Hold != 0 {
		s.Options["hold"] = r.Hold.String()
	}
	if r.RepeatEvery != 0 {
		s.Options["repeat_every"] = r.RepeatEvery.String()
	}
	if r.HysteresisMargin != 5 {
		s.Options["hysteresis_margin"] = strconv.Itoa(r.HysteresisMargin)
	}
	if r.ConfirmShutdown {
		s.Options["confirm_shutdown"] = "1"
	}
	s.Lists["action"] = r.Actions
	return s
}

func defaultConfig() *Config {
	return &Config{
		PIN: "020555", Port: 8377, LANAPI: true,
		BLEPIN:      "020555",
		HTTPEnabled: true, HTTPAddr4: "0.0.0.0", HTTPAddr6: "::", HTTPPort: 8377,
		HTTPSEnabled: true, HTTPSAddr4: "0.0.0.0", HTTPSAddr6: "::", HTTPSPort: 8378,
		TLSCert: "/etc/wattline/tls/server.crt", TLSKey: "/etc/wattline/tls/server.key",
		TokenStore: "/etc/wattline/tokens.json", PairingTTL: 5 * time.Minute,
		MDNSEnabled: true, MDNSInterfaces: []string{"br-lan"},
	}
}

func parseBoolOption(options map[string]string, key string, dst *bool) error {
	v, ok := options[key]
	if !ok {
		return nil
	}
	switch v {
	case "0":
		*dst = false
	case "1":
		*dst = true
	default:
		return fmt.Errorf("%s must be 0 or 1", key)
	}
	return nil
}

func parsePortOption(options map[string]string, key string, dst *int) error {
	v, ok := options[key]
	if !ok {
		return nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 || n > 65535 {
		return fmt.Errorf("%s must be a port from 1 to 65535", key)
	}
	*dst = n
	return nil
}

func validateAddress(name, value string, ipv4 bool) error {
	if value == "" {
		return nil
	}
	ip := net.ParseIP(value)
	if ip == nil || (ipv4 && ip.To4() == nil) || (!ipv4 && ip.To4() != nil) {
		return fmt.Errorf("%s is not a valid IPv%d address", name, map[bool]int{true: 4, false: 6}[ipv4])
	}
	return nil
}

func validatePath(name, value string) error {
	if value == "" || !filepath.IsAbs(value) || filepath.Clean(value) != value || strings.ContainsAny(value, "\x00\r\n") {
		return fmt.Errorf("%s must be a clean absolute path", name)
	}
	return nil
}

var interfaceNameRE = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,15}$`)

func listenersConflict(a4, a6 string, aPort int, b4, b6 string, bPort int) bool {
	if aPort != bPort {
		return false
	}
	v4Conflict := a4 != "" && b4 != "" && (a4 == b4 || a4 == "0.0.0.0" || b4 == "0.0.0.0")
	v6Conflict := a6 != "" && b6 != "" && (a6 == b6 || a6 == "::" || b6 == "::")
	return v4Conflict || v6Conflict
}

// Validate checks settings that would otherwise make listener startup,
// persistence, or LAN discovery fail.
func (c *Config) Validate() error {
	for _, p := range []struct {
		name  string
		value int
	}{{"port", c.HTTPPort}, {"https_port", c.HTTPSPort}} {
		if p.value < 1 || p.value > 65535 {
			return fmt.Errorf("%s must be a port from 1 to 65535", p.name)
		}
	}
	for _, address := range []struct {
		name, value string
		ipv4        bool
	}{{"http_addr4", c.HTTPAddr4, true}, {"http_addr6", c.HTTPAddr6, false},
		{"https_addr4", c.HTTPSAddr4, true}, {"https_addr6", c.HTTPSAddr6, false}} {
		if err := validateAddress(address.name, address.value, address.ipv4); err != nil {
			return err
		}
	}
	if !c.HTTPEnabled && !c.HTTPSEnabled {
		return fmt.Errorf("at least one HTTP or HTTPS listener must be enabled")
	}
	if c.HTTPEnabled && c.HTTPAddr4 == "" && c.HTTPAddr6 == "" {
		return fmt.Errorf("enabled HTTP listener needs an IPv4 or IPv6 address")
	}
	if c.HTTPSEnabled && c.HTTPSAddr4 == "" && c.HTTPSAddr6 == "" {
		return fmt.Errorf("enabled HTTPS listener needs an IPv4 or IPv6 address")
	}
	if c.HTTPEnabled && c.HTTPSEnabled && listenersConflict(c.HTTPAddr4, c.HTTPAddr6, c.HTTPPort, c.HTTPSAddr4, c.HTTPSAddr6, c.HTTPSPort) {
		return fmt.Errorf("HTTP and HTTPS listeners overlap on port %d", c.HTTPPort)
	}
	for _, path := range []struct{ name, value string }{{"tls_cert", c.TLSCert}, {"tls_key", c.TLSKey}, {"token_store", c.TokenStore}} {
		if err := validatePath(path.name, path.value); err != nil {
			return err
		}
	}
	if c.PairingTTL <= 0 {
		return fmt.Errorf("pairing_ttl must be positive")
	}
	seen := make(map[string]struct{}, len(c.MDNSInterfaces))
	for _, name := range c.MDNSInterfaces {
		if !interfaceNameRE.MatchString(name) {
			return fmt.Errorf("invalid mdns_interface %q", name)
		}
		if _, duplicate := seen[name]; duplicate {
			return fmt.Errorf("duplicate mdns_interface %q", name)
		}
		seen[name] = struct{}{}
	}
	if c.MDNSEnabled && len(c.MDNSInterfaces) == 0 {
		return fmt.Errorf("enabled mDNS needs at least one interface")
	}
	return nil
}

func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	doc, err := ParseUCI(string(raw))
	if err != nil {
		return nil, err
	}
	cfg := defaultConfig()
	if main := doc.Find("wattline", "main"); main != nil {
		if v := main.Options["device_mac"]; v != "" {
			cfg.DeviceMAC = v
		}
		if v := main.Options["pin"]; v != "" {
			cfg.PIN = v
			cfg.BLEPIN = v
		}
		if v := main.Options["token"]; v != "" {
			cfg.Token = v
		}
		if err := parsePortOption(main.Options, "port", &cfg.HTTPPort); err != nil {
			return nil, err
		}
		cfg.Port = cfg.HTTPPort
		cfg.LANAPI = main.Options["lan_api"] != "0"
		if _, ok := main.Options["http_addr4"]; !ok {
			if cfg.LANAPI {
				cfg.HTTPAddr4 = "0.0.0.0"
			} else {
				cfg.HTTPAddr4 = "127.0.0.1"
			}
		} else {
			cfg.HTTPAddr4 = main.Options["http_addr4"]
		}
		if _, ok := main.Options["http_addr6"]; !ok {
			if cfg.LANAPI {
				cfg.HTTPAddr6 = "::"
			} else {
				cfg.HTTPAddr6 = "::1"
			}
		} else {
			cfg.HTTPAddr6 = main.Options["http_addr6"]
		}
		if err := parseBoolOption(main.Options, "http_enabled", &cfg.HTTPEnabled); err != nil {
			return nil, err
		}
		if err := parseBoolOption(main.Options, "https_enabled", &cfg.HTTPSEnabled); err != nil {
			return nil, err
		}
		if v, ok := main.Options["https_addr4"]; ok {
			cfg.HTTPSAddr4 = v
		}
		if v, ok := main.Options["https_addr6"]; ok {
			cfg.HTTPSAddr6 = v
		}
		if err := parsePortOption(main.Options, "https_port", &cfg.HTTPSPort); err != nil {
			return nil, err
		}
		if v, ok := main.Options["tls_cert"]; ok {
			cfg.TLSCert = v
		}
		if v, ok := main.Options["tls_key"]; ok {
			cfg.TLSKey = v
		}
		if v, ok := main.Options["token_store"]; ok {
			cfg.TokenStore = v
		}
		if v, ok := main.Options["pairing_ttl"]; ok {
			d, err := time.ParseDuration(v)
			if err != nil {
				return nil, fmt.Errorf("pairing_ttl: %w", err)
			}
			cfg.PairingTTL = d
		}
		for key, dst := range map[string]*bool{
			"pairing_always_on": &cfg.PairingAlwaysOn, "advanced": &cfg.Advanced,
			"mdns_enabled": &cfg.MDNSEnabled, "wan_access": &cfg.WANAccess,
		} {
			if err := parseBoolOption(main.Options, key, dst); err != nil {
				return nil, err
			}
		}
		// Legacy configs have neither key and retain the br-lan default. Once
		// mdns_enabled is explicitly configured, an absent list means the user
		// deliberately cleared the interfaces (valid while mDNS is disabled).
		if _, configured := main.Options["mdns_enabled"]; configured {
			cfg.MDNSInterfaces = []string{}
		}
		if interfaces, ok := main.Lists["mdns_interface"]; ok {
			cfg.MDNSInterfaces = append([]string(nil), interfaces...)
		}
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	for _, s := range doc.Sections {
		if s.Type != "rule" {
			continue
		}
		r, err := sectionToRule(s)
		if err != nil {
			log.Printf("wattline: skipping invalid rule %q: %v", s.Name, err)
			continue
		}
		cfg.Rules = append(cfg.Rules, r)
	}
	return cfg, nil
}

// saveMu serializes the read-modify-write cycles of SavePairing (pairing
// goroutine) and SaveRules (HTTP handlers) so concurrent saves can't clobber
// each other's changes through a stale read.
var saveMu sync.Mutex

// EnsureBootstrapToken installs candidate only when the main section has no
// bootstrap token. The locked read-modify-write preserves rules, extension
// fields, and a token provisioned concurrently by another caller.
func EnsureBootstrapToken(path, candidate string) (string, error) {
	if candidate == "" || strings.ContainsAny(candidate, " \t\r\n") {
		return "", errors.New("bootstrap token candidate is invalid")
	}
	saveMu.Lock()
	defer saveMu.Unlock()
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	doc, err := ParseUCI(string(raw))
	if err != nil {
		return "", err
	}
	main := doc.Find("wattline", "main")
	if main == nil {
		main = newSection("wattline", "main")
		doc.Sections = append(doc.Sections, main)
	}
	if existing := main.Options["token"]; existing != "" {
		info, err := os.Stat(path)
		if err != nil {
			return "", err
		}
		if info.Mode().Perm()&^os.FileMode(0o600) != 0 {
			if err := writeConfigAtomicPrivate(path, []byte(doc.Serialize())); err != nil {
				return "", err
			}
		}
		return existing, nil
	}
	main.Options["token"] = candidate
	if err := writeConfigAtomicPrivate(path, []byte(doc.Serialize())); err != nil {
		return "", err
	}
	return candidate, nil
}

func writeConfigAtomicPrivate(path string, data []byte) error {
	mode := os.FileMode(0o600)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm() & 0o600
	} else if !os.IsNotExist(err) {
		return err
	}
	return writeConfigAtomicWithMode(path, data, mode)
}

// writeConfigAtomic writes beside path so the final rename is atomic. Existing
// permissions are retained; a newly created secret-bearing config is private.
func writeConfigAtomic(path string, data []byte) error {
	mode := os.FileMode(0o600)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	} else if !os.IsNotExist(err) {
		return err
	}
	return writeConfigAtomicWithMode(path, data, mode)
}

func writeConfigAtomicWithMode(path string, data []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

func boolOption(v bool) string {
	if v {
		return "1"
	}
	return "0"
}

// SaveMain updates only the named wattline main section. Rule sections,
// extension options/lists, and unknown section types are retained.
func (c *Config) SaveMain(path string) error {
	if err := c.Validate(); err != nil {
		return err
	}
	saveMu.Lock()
	defer saveMu.Unlock()
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	doc, err := ParseUCI(string(raw))
	if err != nil {
		return err
	}
	main := doc.Find("wattline", "main")
	if main == nil {
		main = newSection("wattline", "main")
		doc.Sections = append(doc.Sections, main)
	}
	pin := c.BLEPIN
	if pin == "" {
		pin = c.PIN
	}
	main.Options["device_mac"] = c.DeviceMAC
	main.Options["pin"] = pin
	main.Options["port"] = strconv.Itoa(c.HTTPPort)
	main.Options["lan_api"] = boolOption(c.LANAPI)
	main.Options["http_enabled"] = boolOption(c.HTTPEnabled)
	main.Options["http_addr4"] = c.HTTPAddr4
	main.Options["http_addr6"] = c.HTTPAddr6
	main.Options["https_enabled"] = boolOption(c.HTTPSEnabled)
	main.Options["https_addr4"] = c.HTTPSAddr4
	main.Options["https_addr6"] = c.HTTPSAddr6
	main.Options["https_port"] = strconv.Itoa(c.HTTPSPort)
	main.Options["tls_cert"] = c.TLSCert
	main.Options["tls_key"] = c.TLSKey
	main.Options["token_store"] = c.TokenStore
	main.Options["pairing_ttl"] = c.PairingTTL.String()
	main.Options["pairing_always_on"] = boolOption(c.PairingAlwaysOn)
	main.Options["advanced"] = boolOption(c.Advanced)
	main.Options["mdns_enabled"] = boolOption(c.MDNSEnabled)
	main.Options["wan_access"] = boolOption(c.WANAccess)
	main.Lists["mdns_interface"] = append([]string(nil), c.MDNSInterfaces...)
	return writeConfigAtomic(path, []byte(doc.Serialize()))
}

// SavePairing records the paired device in the main section, preserving the
// rest of the file. An empty pin keeps the currently configured PIN.
func SavePairing(path, mac, pin string) error {
	saveMu.Lock()
	defer saveMu.Unlock()
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	doc, err := ParseUCI(string(raw))
	if err != nil {
		return err
	}
	main := doc.Find("wattline", "main")
	if main == nil {
		main = newSection("wattline", "main")
		doc.Sections = append(doc.Sections, main)
	}
	main.Options["device_mac"] = mac
	if pin != "" {
		main.Options["pin"] = pin
	}
	return writeConfigAtomic(path, []byte(doc.Serialize()))
}

// SaveRules rewrites the config file, replacing all rule sections while
// preserving every non-rule section verbatim.
func (c *Config) SaveRules(path string) error {
	saveMu.Lock()
	defer saveMu.Unlock()
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	doc, err := ParseUCI(string(raw))
	if err != nil {
		return err
	}
	kept := doc.Sections[:0]
	for _, s := range doc.Sections {
		if s.Type != "rule" {
			kept = append(kept, s)
		}
	}
	doc.Sections = kept
	for _, r := range c.Rules {
		doc.Sections = append(doc.Sections, ruleToSection(r))
	}
	return writeConfigAtomic(path, []byte(doc.Serialize()))
}
