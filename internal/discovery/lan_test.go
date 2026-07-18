package discovery

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestParseOpenWrtLANStatusUsesAuthoritativeDevices(t *testing.T) {
	got, err := ParseOpenWrtLANStatus([]byte(`{"up":true,"device":"br-lan","l3_device":"br-lan"}`))
	if err != nil || !reflect.DeepEqual(got, []string{"br-lan"}) {
		t.Fatalf("members=%#v err=%v", got, err)
	}
	for _, raw := range []string{`{`, `{}`, `{"device":""}`, `{"device":"br-lan"}`, `{"up":false,"device":"br-lan"}`} {
		if _, err := ParseOpenWrtLANStatus([]byte(raw)); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}

func TestOpenWrtLANMembershipFailsClosedAndUsesBoundedUbusCall(t *testing.T) {
	membership := OpenWrtLANMembership{
		LookPath: func(name string) (string, error) {
			if name != "ubus" {
				t.Fatalf("lookup = %q", name)
			}
			return "/sbin/ubus", nil
		},
		Run: func(ctx context.Context, executable string, args ...string) ([]byte, error) {
			if _, ok := ctx.Deadline(); !ok {
				t.Fatal("ubus context has no deadline")
			}
			if executable != "/sbin/ubus" || !reflect.DeepEqual(args, []string{"-S", "call", "network.interface.lan", "status"}) {
				t.Fatalf("command=%q args=%#v", executable, args)
			}
			return []byte(`{"up":true,"device":"br-lan","l3_device":"br-lan"}`), nil
		},
	}
	if got, err := membership.LANInterfaces(); err != nil || !reflect.DeepEqual(got, []string{"br-lan"}) {
		t.Fatalf("members=%#v err=%v", got, err)
	}
	membership.LookPath = func(string) (string, error) { return "", errors.New("missing") }
	if _, err := membership.LANInterfaces(); err == nil {
		t.Fatal("missing ubus did not fail closed")
	}
}
