package actions

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/keithah/openwrt-wattline/internal/state"
)

type fakeDev struct {
	calls []string
	fail  bool
}

func (f *fakeDev) rec(s string) error {
	f.calls = append(f.calls, s)
	if f.fail {
		return errors.New("boom")
	}
	return nil
}
func (f *fakeDev) DCControl(on bool) error     { return f.rec("dc") }
func (f *fakeDev) TypeCOutput(on bool) error   { return f.rec("usbc") }
func (f *fakeDev) BypassControl(on bool) error { return f.rec("bypass") }
func (f *fakeDev) Restart() error              { return f.rec("restart") }
func (f *fakeDev) Shutdown() error             { return f.rec("shutdown") }

func TestExecuteDispatch(t *testing.T) {
	dev := &fakeDev{}
	x := NewExecutor(dev, "Link-Power-2")
	errs := x.Execute([]string{"dc_off", "usbc_on", "bypass_off", "restart", "shutdown"},
		state.Snapshot{}, "r", time.Now())
	if len(errs) != 0 {
		t.Fatalf("errs: %v", errs)
	}
	want := []string{"dc", "usbc", "bypass", "restart", "shutdown"}
	for i, w := range want {
		if dev.calls[i] != w {
			t.Fatalf("call %d = %s want %s", i, dev.calls[i], w)
		}
	}
}

func TestWebhookPostsJSON(t *testing.T) {
	var got WebhookPayload
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Error(err)
		}
	}))
	defer srv.Close()
	x := NewExecutor(&fakeDev{}, "Link-Power-2")
	at := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	errs := x.Execute([]string{"webhook:" + srv.URL}, state.Snapshot{Connected: true}, "myrule", at)
	if len(errs) != 0 {
		t.Fatalf("errs: %v", errs)
	}
	if got.Rule != "myrule" || got.Device != "Link-Power-2" || !got.Telemetry.Connected || !got.FiredAt.Equal(at) {
		t.Fatalf("payload: %+v", got)
	}
}

func TestDeviceFailureStillRunsWebhook(t *testing.T) {
	srvHit := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { srvHit = true }))
	defer srv.Close()
	x := NewExecutor(&fakeDev{fail: true}, "d")
	errs := x.Execute([]string{"dc_off", "webhook:" + srv.URL}, state.Snapshot{}, "r", time.Now())
	if len(errs) != 1 || !srvHit {
		t.Fatalf("errs=%v srvHit=%v", errs, srvHit)
	}
}

func TestValidateAction(t *testing.T) {
	for _, ok := range []string{"dc_on", "dc_off", "usbc_on", "usbc_off",
		"bypass_on", "bypass_off", "restart", "shutdown", "webhook:https://x"} {
		if err := ValidateAction(ok); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	for _, bad := range []string{"dc_toggle", "webhook:", "nope"} {
		if err := ValidateAction(bad); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
}
