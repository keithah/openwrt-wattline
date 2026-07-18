package discovery

import (
	"reflect"
	"strings"
	"testing"

	"github.com/keithah/openwrt-wattline/internal/config"
)

func TestTXTUsesExactContractOrderAndWidths(t *testing.T) {
	fingerprint := strings.Repeat("a", 64)
	got := TXT(Metadata{Version: "1.3.0", API: 1, ID: "DC:04:5A:EB:72:2B", Model: "BP4SL3V2", CID: 0x305, Features: 0xfff, TLS: fingerprint})
	want := []string{"ver=1.3.0", "api=1", "id=DC:04:5A:EB:72:2B", "model=BP4SL3V2", "cid=0305", "features=00000fff", "tls=" + fingerprint, "auth=pin"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("TXT = %#v, want %#v", got, want)
	}
}

func TestTXTDoesNotPublishWithoutDeviceID(t *testing.T) {
	if got := TXT(Metadata{Version: "dev", API: 1, TLS: "none"}); got != nil {
		t.Fatalf("TXT = %#v, want nil", got)
	}
}

func TestPreferredPortUsesHTTPSThenHTTP(t *testing.T) {
	tests := []struct {
		name string
		cfg  config.Config
		want int
	}{
		{"https", config.Config{HTTPEnabled: true, HTTPPort: 8377, HTTPSEnabled: true, HTTPSPort: 8378}, 8378},
		{"http", config.Config{HTTPEnabled: true, HTTPPort: 8377, HTTPSPort: 8378}, 8377},
		{"none", config.Config{}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := PreferredPort(tt.cfg); got != tt.want {
				t.Fatalf("PreferredPort = %d, want %d", got, tt.want)
			}
		})
	}
}
