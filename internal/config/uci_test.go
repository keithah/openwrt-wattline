package config

import "testing"

const sample = `
config wattline 'main'
	option device_mac 'DC:04:5A:EB:72:2B'
	option pin '020555'
	option port '8377'
	option lan_api '1'
	option token 'sekrit'

config rule 'no_input_shutdown'
	option enabled '1'
	option condition 'input_power'
	option state 'absent'
	option hold '10m'
	list action 'webhook:https://ntfy.sh/keith-power?msg=input+lost'
	list action 'shutdown'
	option confirm_shutdown '1'
`

func TestParseAndSerializeRoundTrip(t *testing.T) {
	doc, err := ParseUCI(sample)
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Sections) != 2 {
		t.Fatalf("want 2 sections, got %d", len(doc.Sections))
	}
	main := doc.Find("wattline", "main")
	if main == nil || main.Options["pin"] != "020555" {
		t.Fatalf("main section: %+v", main)
	}
	rule := doc.Find("rule", "no_input_shutdown")
	if rule == nil || len(rule.Lists["action"]) != 2 || rule.Lists["action"][1] != "shutdown" {
		t.Fatalf("rule section: %+v", rule)
	}
	// Round-trip: serialize then re-parse must be identical.
	doc2, err := ParseUCI(doc.Serialize())
	if err != nil {
		t.Fatal(err)
	}
	if doc2.Serialize() != doc.Serialize() {
		t.Fatalf("round-trip mismatch:\n%s\n---\n%s", doc.Serialize(), doc2.Serialize())
	}
}
