package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeTemp(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "wattline")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadTyped(t *testing.T) {
	cfg, err := Load(writeTemp(t, sample))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DeviceMAC != "DC:04:5A:EB:72:2B" || cfg.PIN != "020555" ||
		cfg.Port != 8377 || !cfg.LANAPI || cfg.Token != "sekrit" {
		t.Fatalf("main: %+v", cfg)
	}
	if len(cfg.Rules) != 1 {
		t.Fatalf("rules: %+v", cfg.Rules)
	}
	r := cfg.Rules[0]
	if r.Name != "no_input_shutdown" || !r.Enabled || r.Condition != "input_power" ||
		r.State != "absent" || r.Hold != 10*time.Minute || !r.ConfirmShutdown ||
		len(r.Actions) != 2 || r.HysteresisMargin != 5 {
		t.Fatalf("rule: %+v", r)
	}
}

func TestLoadSkipsInvalidRules(t *testing.T) {
	bad := sample + `
config rule 'broken'
	option enabled '1'
	option condition 'nonsense'
	list action 'dc_off'

config rule 'unconfirmed_shutdown'
	option enabled '1'
	option condition 'battery_level'
	option op 'below'
	option percent '10'
	list action 'shutdown'
`
	cfg, err := Load(writeTemp(t, bad))
	if err != nil {
		t.Fatal(err)
	}
	// broken condition and shutdown-without-confirm are both skipped
	if len(cfg.Rules) != 1 {
		t.Fatalf("want 1 valid rule, got %d: %+v", len(cfg.Rules), cfg.Rules)
	}
}

func TestLoadSkipsInvalidCron(t *testing.T) {
	bad := sample + `
config rule 'bad_cron'
	option enabled '1'
	option condition 'schedule'
	option cron 'banana'
	list action 'dc_off'

config rule 'good_cron'
	option enabled '1'
	option condition 'schedule'
	option cron '0 22 * * *'
	list action 'dc_off'
`
	cfg, err := Load(writeTemp(t, bad))
	if err != nil {
		t.Fatal(err)
	}
	// The sample rule plus the good_cron rule should load; bad_cron must be skipped.
	if len(cfg.Rules) != 2 {
		t.Fatalf("want 2 valid rules, got %d: %+v", len(cfg.Rules), cfg.Rules)
	}
	for _, r := range cfg.Rules {
		if r.Name == "bad_cron" {
			t.Fatalf("invalid cron rule must be skipped, got: %+v", r)
		}
	}
	found := false
	for _, r := range cfg.Rules {
		if r.Name == "good_cron" {
			found = true
		}
	}
	if !found {
		t.Fatalf("valid cron rule should still load: %+v", cfg.Rules)
	}
}

func TestValidateRejectsEmptyName(t *testing.T) {
	r := Rule{Name: "", Condition: "input_power", State: "absent", Actions: []string{"dc_off"}}
	if err := r.Validate(); err == nil {
		t.Fatal("expected error for empty rule name")
	}
}

func TestSaveRulesRoundTrip(t *testing.T) {
	p := writeTemp(t, sample)
	cfg, _ := Load(p)
	cfg.Rules = append(cfg.Rules, Rule{
		Name: "low_batt", Enabled: true, Condition: "battery_level",
		Op: "below", Percent: 15, Hold: 5 * time.Minute,
		HysteresisMargin: 5, Actions: []string{"dc_off"},
	})
	if err := cfg.SaveRules(p); err != nil {
		t.Fatal(err)
	}
	cfg2, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg2.Rules) != 2 || cfg2.Rules[1].Name != "low_batt" || cfg2.Rules[1].Percent != 15 {
		t.Fatalf("%+v", cfg2.Rules)
	}
	if cfg2.PIN != "020555" { // main section preserved
		t.Fatal("main section lost")
	}
}
