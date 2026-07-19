package api

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestContractDocumentation(t *testing.T) {
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate contract test")
	}
	data, err := os.ReadFile(filepath.Join(filepath.Dir(filename), "..", "..", "docs", "api.md"))
	if err != nil {
		t.Fatal(err)
	}
	doc := string(data)

	if got, want := len(routeDescriptors), 57; got != want {
		t.Fatalf("registered route count = %d, want %d; update the contract inventory deliberately", got, want)
	}
	seen := make(map[string]struct{}, len(routeDescriptors))
	for _, route := range routeDescriptors {
		want := "`" + route.method + " " + route.path + "`"
		if _, duplicate := seen[want]; duplicate {
			t.Errorf("duplicate registered route %s", want)
		}
		seen[want] = struct{}{}
		if !strings.Contains(doc, want) {
			t.Errorf("docs/api.md does not contain registered route %s", want)
		}
	}

	required := []string{
		"## Authentication roles",
		"## Error envelope",
		"64 KiB",
		"unknown-length chunked body",
		"CORS preflight `OPTIONS` requests bypass body validation",
		"15-second request-read deadline",
		"data: {complete snapshot JSON}\\n",
		"slow subscribers are disconnected",
		"all 15 documented BLE `FEATURES` bits",
		"manual `webhook:URL` action requires admin",
		"ver`, `api`, `id`, `model`, `cid`, `features`, `tls`, and `auth",
		"wattline://pair?v=1&id=DEVICE_ID&host=PREFERRED_HOST&http=8377&https=8378&pin=123456&tls=CERT_SHA256",
		"SHA-256 of DER certificate bytes",
		"## Compatibility routes",
	}
	normalizedDoc := strings.Join(strings.Fields(doc), " ")
	for _, phrase := range required {
		if !strings.Contains(normalizedDoc, strings.Join(strings.Fields(phrase), " ")) {
			t.Errorf("docs/api.md does not contain required contract phrase %q", phrase)
		}
	}

	for _, route := range routeDescriptors {
		if route.compatibility {
			want := "`" + route.method + " " + route.path + "`"
			compatibility := doc[strings.Index(doc, "## Compatibility routes"):]
			if !strings.Contains(compatibility, want) {
				t.Errorf("compatibility alias missing from compatibility inventory: %s", want)
			}
		}
	}

	semanticSections := []struct {
		start   string
		end     string
		phrases []string
	}{
		{"## Settings and TLS", "## mDNS", []string{
			"successful durable `SaveMain` activates the target store",
			"all old-store managed SSE streams close",
			"old-store-only managed tokens fail future requests",
			"all managed-token records present in the target store become active",
			"identical or copied secret hashes can reconnect even though their old-store SSE streams closed",
			"bootstrap SSE remains open",
			"not revoked or deleted",
			"switching back to that path can reactivate them",
			"persistence failure preserves the old authenticator and its streams",
			"Clients MUST verify the certificate before sending any bearer token",
			"replaces both public-CA chain validation and hostname/SAN validation",
			"startup hostname plus `localhost`, `127.0.0.1`, and `::1`",
			"public-CA mode",
			"normal chain and hostname/SAN validation",
			"MUST NOT silently downgrade to HTTP",
			"old pin MUST be rejected",
		}},
		{"### Cached state compatibility routes", "### Rule and action compatibility routes", []string{
			"managed-token revocation",
			"successful token-store cutover",
			"slow-subscriber overflow terminates the stream",
		}},
		{"### Rule and action compatibility routes", "### BLE-device pairing compatibility routes", []string{
			"`POST /api/v1/rules` | admin",
			"`PUT /api/v1/rules/{name}` | admin",
			"`DELETE /api/v1/rules/{name}` | admin",
			"manual `webhook:URL` requires admin",
		}},
		{"### BLE-device pairing compatibility routes", "### Deprecated device-control aliases", []string{
			"409 E(operation_in_progress)",
			"when unpair is busy",
		}},
		{"### Deprecated device-control aliases", "## On-target caveats", []string{
			"`clear:true` ignores `watts`",
			"mutation plus authoritative re-GET",
			"mutation plus authoritative re-list",
			"nonempty body",
		}},
	}
	for _, section := range semanticSections {
		start := strings.Index(doc, section.start)
		end := strings.Index(doc, section.end)
		if start < 0 || end <= start {
			t.Fatalf("contract section bounds %q..%q not found", section.start, section.end)
		}
		body := doc[start:end]
		normalizedBody := strings.Join(strings.Fields(body), " ")
		for _, phrase := range section.phrases {
			if !strings.Contains(normalizedBody, strings.Join(strings.Fields(phrase), " ")) {
				t.Errorf("contract section %q does not contain semantic phrase %q", section.start, phrase)
			}
		}
	}
}
