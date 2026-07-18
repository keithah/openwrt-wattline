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

	if got, want := len(routeDescriptors), 56; got != want {
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
		"data: {complete snapshot JSON}\\n",
		"ver`, `api`, `id`, `model`, `cid`, `features`, `tls`, and `auth",
		"wattline://pair?v=1&id=DEVICE_ID&host=PREFERRED_HOST&http=8377&https=8378&pin=123456&tls=CERT_SHA256",
		"SHA-256 of DER certificate bytes",
		"## Compatibility routes",
	}
	for _, phrase := range required {
		if !strings.Contains(doc, phrase) {
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
}
