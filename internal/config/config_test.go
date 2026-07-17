package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
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

func TestSavePairingUpdatesMACAndPIN(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/wattline"
	src := `config wattline 'main'
	option device_mac ''
	option pin '020555'
	option token 'tok'

config rule 'keepme'
	option enabled '1'
`
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := SavePairing(path, "DC:04:5A:EB:72:2B", "111222"); err != nil {
		t.Fatalf("SavePairing: %v", err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DeviceMAC != "DC:04:5A:EB:72:2B" || cfg.PIN != "111222" {
		t.Fatalf("got mac=%q pin=%q", cfg.DeviceMAC, cfg.PIN)
	}
	raw, _ := os.ReadFile(path)
	if !strings.Contains(string(raw), "config rule 'keepme'") {
		t.Fatalf("rule section lost:\n%s", raw)
	}
	// empty pin keeps the existing one
	if err := SavePairing(path, "AA:BB:CC:DD:EE:FF", ""); err != nil {
		t.Fatalf("SavePairing(empty pin): %v", err)
	}
	cfg, err = Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DeviceMAC != "AA:BB:CC:DD:EE:FF" || cfg.PIN != "111222" {
		t.Fatalf("after empty-pin save: mac=%q pin=%q", cfg.DeviceMAC, cfg.PIN)
	}
}

func TestConfigCompatibilityOldFiveKeyFixture(t *testing.T) {
	path := writeTemp(t, `config wattline 'main'
	option device_mac 'DC:04:5A:EB:72:2B'
	option pin '020555'
	option token 'bootstrap-secret'
	option port '9123'
	option lan_api '1'

config rule 'keepme'
	option enabled '1'
	option condition 'input_power'
	option state 'absent'
	list action 'dc_off'
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.BLEPIN != "020555" || cfg.PIN != "020555" {
		t.Fatalf("legacy pin mapping: BLEPIN=%q PIN=%q", cfg.BLEPIN, cfg.PIN)
	}
	if cfg.HTTPPort != 9123 || cfg.Port != 9123 {
		t.Fatalf("legacy port mapping: HTTPPort=%d Port=%d", cfg.HTTPPort, cfg.Port)
	}
	if !cfg.LANAPI || cfg.HTTPAddr4 != "0.0.0.0" || cfg.HTTPAddr6 != "::" {
		t.Fatalf("legacy lan_api mapping: LANAPI=%v addr4=%q addr6=%q", cfg.LANAPI, cfg.HTTPAddr4, cfg.HTTPAddr6)
	}
	if !cfg.HTTPEnabled || !cfg.HTTPSEnabled || cfg.HTTPSPort != 8378 {
		t.Fatalf("listener defaults: %+v", cfg)
	}
	if len(cfg.Rules) != 1 || cfg.Rules[0].Name != "keepme" {
		t.Fatalf("legacy rule lost: %+v", cfg.Rules)
	}
}

func TestConfigCompatibilityLANAPILoopbackDerivation(t *testing.T) {
	cfg, err := Load(writeTemp(t, `config wattline 'main'
	option lan_api '0'
`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HTTPAddr4 != "127.0.0.1" || cfg.HTTPAddr6 != "::1" {
		t.Fatalf("loopback derivation: addr4=%q addr6=%q", cfg.HTTPAddr4, cfg.HTTPAddr6)
	}
}

func TestNetworkConfigCompleteFixture(t *testing.T) {
	cfg, err := Load(writeTemp(t, `config wattline 'main'
	option device_mac 'DC:04:5A:EB:72:2B'
	option pin '123456'
	option token 'do-not-return'
	option port '8080'
	option lan_api '0'
	option http_enabled '1'
	option http_addr4 '192.0.2.10'
	option http_addr6 '2001:db8::10'
	option https_enabled '1'
	option https_addr4 '192.0.2.11'
	option https_addr6 '2001:db8::11'
	option https_port '8443'
	option tls_cert '/etc/wattline/custom.crt'
	option tls_key '/etc/wattline/custom.key'
	option token_store '/etc/wattline/custom-tokens.json'
	option pairing_ttl '12m30s'
	option pairing_always_on '1'
	option advanced '1'
	option mdns_enabled '1'
	list mdns_interface 'br-lan'
	list mdns_interface 'wlan0'
	option wan_access '1'
`))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.HTTPEnabled || cfg.HTTPAddr4 != "192.0.2.10" || cfg.HTTPAddr6 != "2001:db8::10" || cfg.HTTPPort != 8080 {
		t.Fatalf("http: %+v", cfg)
	}
	if !cfg.HTTPSEnabled || cfg.HTTPSAddr4 != "192.0.2.11" || cfg.HTTPSAddr6 != "2001:db8::11" || cfg.HTTPSPort != 8443 {
		t.Fatalf("https: %+v", cfg)
	}
	if cfg.TLSCert != "/etc/wattline/custom.crt" || cfg.TLSKey != "/etc/wattline/custom.key" || cfg.TokenStore != "/etc/wattline/custom-tokens.json" {
		t.Fatalf("paths: %+v", cfg)
	}
	if cfg.PairingTTL != 12*time.Minute+30*time.Second || !cfg.PairingAlwaysOn || !cfg.Advanced || !cfg.MDNSEnabled || !cfg.WANAccess {
		t.Fatalf("security/discovery: %+v", cfg)
	}
	if !reflect.DeepEqual(cfg.MDNSInterfaces, []string{"br-lan", "wlan0"}) {
		t.Fatalf("mDNS interfaces: %#v", cfg.MDNSInterfaces)
	}
}

func TestNetworkConfigDefaults(t *testing.T) {
	cfg, err := Load(writeTemp(t, "config wattline 'main'\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.HTTPEnabled || cfg.HTTPAddr4 != "0.0.0.0" || cfg.HTTPAddr6 != "::" || cfg.HTTPPort != 8377 {
		t.Fatalf("http defaults: %+v", cfg)
	}
	if !cfg.HTTPSEnabled || cfg.HTTPSAddr4 != "0.0.0.0" || cfg.HTTPSAddr6 != "::" || cfg.HTTPSPort != 8378 {
		t.Fatalf("https defaults: %+v", cfg)
	}
	if cfg.TLSCert != "/etc/wattline/tls/server.crt" || cfg.TLSKey != "/etc/wattline/tls/server.key" || cfg.TokenStore != "/etc/wattline/tokens.json" {
		t.Fatalf("path defaults: %+v", cfg)
	}
	if cfg.PairingTTL != 5*time.Minute || cfg.PairingAlwaysOn || cfg.Advanced || !cfg.MDNSEnabled || cfg.WANAccess {
		t.Fatalf("policy defaults: %+v", cfg)
	}
	if !reflect.DeepEqual(cfg.MDNSInterfaces, []string{"br-lan"}) {
		t.Fatalf("mDNS defaults: %#v", cfg.MDNSInterfaces)
	}
}

func TestNetworkConfigRejectsInvalidValues(t *testing.T) {
	tests := []struct {
		name, options string
	}{
		{"zero HTTP port", "option port '0'"},
		{"large HTTPS port", "option https_port '65536'"},
		{"invalid HTTP port", "option port 'eight'"},
		{"invalid TTL", "option pairing_ttl 'soon'"},
		{"zero TTL", "option pairing_ttl '0s'"},
		{"both listeners disabled", "option http_enabled '0'\n\toption https_enabled '0'"},
		{"HTTP enabled without addresses", "option http_addr4 ''\n\toption http_addr6 ''"},
		{"wrong IPv4 family", "option http_addr4 '2001:db8::1'"},
		{"wrong IPv6 family", "option https_addr6 '192.0.2.1'"},
		{"non-absolute certificate", "option tls_cert 'server.crt'"},
		{"unclean key path", "option tls_key '/etc/wattline/../server.key'"},
		{"empty token store", "option token_store ''"},
		{"empty mDNS interface", "list mdns_interface ''"},
		{"invalid mDNS interface", "list mdns_interface 'bad interface'"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load(writeTemp(t, "config wattline 'main'\n\t"+tt.options+"\n"))
			if err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestSecurityConfigSaveMainPreservesRulesAndUnknownData(t *testing.T) {
	path := writeTemp(t, `config wattline 'main'
	option device_mac 'old'
	option token 'bootstrap-secret'
	option vendor_extension 'preserve-me'
	option port '8377'

config vendor 'opaque'
	option payload 'keep-this'

config rule 'keepme'
	option enabled '1'
	option condition 'input_power'
	option state 'absent'
	list action 'dc_off'
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg.DeviceMAC = "new"
	cfg.BLEPIN, cfg.PIN = "654321", "654321"
	cfg.HTTPPort, cfg.Port = 9000, 9000
	cfg.MDNSInterfaces = []string{"br-lan", "guest"}
	cfg.WANAccess = true
	if err := cfg.SaveMain(path); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"option vendor_extension 'preserve-me'", "config vendor 'opaque'", "option payload 'keep-this'", "config rule 'keepme'", "option token 'bootstrap-secret'"} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("SaveMain lost %q:\n%s", want, raw)
		}
	}
	saved, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if saved.DeviceMAC != "new" || saved.BLEPIN != "654321" || saved.HTTPPort != 9000 || !saved.WANAccess || !reflect.DeepEqual(saved.MDNSInterfaces, []string{"br-lan", "guest"}) {
		t.Fatalf("saved main mismatch: %+v", saved)
	}
}

func TestSecurityConfigSettingsViewContainsNoSecrets(t *testing.T) {
	cfg, err := Load(writeTemp(t, `config wattline 'main'
	option token 'top-secret-bearer'
	option tls_key '/etc/wattline/private.key'
`))
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(cfg.SettingsView())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "top-secret-bearer") || strings.Contains(string(b), "token\"") {
		t.Fatalf("settings leaked bearer secret: %s", b)
	}
	if strings.Contains(string(b), "PRIVATE KEY") {
		t.Fatalf("settings leaked private-key bytes: %s", b)
	}
	want := `"key":"/etc/wattline/private.key"`
	if !strings.Contains(string(b), want) {
		t.Fatalf("settings omitted safe private-key path %s: %s", want, b)
	}
}

func TestSecurityConfigSettingsViewRendersClearedInterfacesAsArray(t *testing.T) {
	cfg, err := Load(writeTemp(t, `config wattline 'main'
	option mdns_enabled '0'
	list mdns_interface 'br-lan'
`))
	if err != nil {
		t.Fatal(err)
	}
	cfg.MDNSInterfaces = []string{}
	b, err := json.Marshal(cfg.SettingsView())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"interfaces":[]`) {
		t.Fatalf("cleared interfaces must render as an empty array: %s", b)
	}
}

func TestSecurityConfigSaveMainRoundTripsClearedMDNSInterfaces(t *testing.T) {
	path := writeTemp(t, `config wattline 'main'
	option mdns_enabled '1'
	list mdns_interface 'br-lan'
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg.MDNSEnabled = false
	cfg.MDNSInterfaces = []string{}
	if err := cfg.SaveMain(path); err != nil {
		t.Fatal(err)
	}
	saved, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if saved.MDNSEnabled || len(saved.MDNSInterfaces) != 0 {
		t.Fatalf("cleared mDNS settings did not round-trip: enabled=%v interfaces=%#v", saved.MDNSEnabled, saved.MDNSInterfaces)
	}
}

func TestSecurityConfigSaveMainPreservesFreshOnDiskToken(t *testing.T) {
	path := writeTemp(t, `config wattline 'main'
	option token 'old-bootstrap'
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	rotated := strings.Replace(string(raw), "old-bootstrap", "fresh-bootstrap", 1)
	if err := os.WriteFile(path, []byte(rotated), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg.Token = "stale-in-memory"
	cfg.Advanced = true
	if err := cfg.SaveMain(path); err != nil {
		t.Fatal(err)
	}
	saved, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Token != "fresh-bootstrap" {
		t.Fatalf("SaveMain rewrote freshly rotated token: %q", saved.Token)
	}
}

func TestSecurityConfigSaveMainPreservesFileMode(t *testing.T) {
	path := writeTemp(t, "config wattline 'main'\n")
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Advanced = true
	if err := cfg.SaveMain(path); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("config mode after SaveMain = %04o, want 0600", got)
	}
}

func TestSecurityConfigAtomicWriteCleansTempAfterRenameFailure(t *testing.T) {
	dir := t.TempDir()
	destination := filepath.Join(dir, "wattline")
	if err := os.Mkdir(destination, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writeConfigAtomic(destination, []byte("secret")); err == nil {
		t.Fatal("expected rename over directory to fail")
	}
	matches, err := filepath.Glob(filepath.Join(dir, ".wattline.tmp-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("atomic write left temporary files: %#v", matches)
	}
}

func TestSecurityConfigAtomicWriteUsesPrivateCreateMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wattline")
	if err := writeConfigAtomic(path, []byte("config wattline 'main'\n\toption token 'secret'\n")); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("new secret-bearing config mode = %04o, want 0600", got)
	}
}
