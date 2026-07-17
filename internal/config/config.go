package config

import (
	"fmt"
	"log"
	"os"
	"strconv"
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
	Condition        string        `json:"condition"` // input_power|battery_level|schedule|port_power|temperature
	State            string        `json:"state,omitempty"`
	Op               string        `json:"op,omitempty"` // below|above
	Port             string        `json:"port,omitempty"`
	Percent          int           `json:"percent,omitempty"`
	Watts            float64       `json:"watts,omitempty"`
	TempC            float64       `json:"temp_c,omitempty"` // temperature condition threshold (°C)
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
	Rules     []Rule
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
	case "temperature":
		if r.Op != "below" && r.Op != "above" {
			return fmt.Errorf("temperature needs op below|above")
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
	if v := s.Options["temp_c"]; v != "" {
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return r, fmt.Errorf("temp_c: %w", err)
		}
		r.TempC = f
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
	if r.TempC != 0 {
		s.Options["temp_c"] = strconv.FormatFloat(r.TempC, 'f', -1, 64)
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

func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	doc, err := ParseUCI(string(raw))
	if err != nil {
		return nil, err
	}
	cfg := &Config{PIN: "020555", Port: 8377, LANAPI: true}
	if main := doc.Find("wattline", "main"); main != nil {
		if v := main.Options["device_mac"]; v != "" {
			cfg.DeviceMAC = v
		}
		if v := main.Options["pin"]; v != "" {
			cfg.PIN = v
		}
		if v := main.Options["token"]; v != "" {
			cfg.Token = v
		}
		if v := main.Options["port"]; v != "" {
			if n, err := strconv.Atoi(v); err == nil {
				cfg.Port = n
			}
		}
		cfg.LANAPI = main.Options["lan_api"] != "0"
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
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(doc.Serialize()), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
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
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(doc.Serialize()), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
