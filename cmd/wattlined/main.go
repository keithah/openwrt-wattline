// Command wattlined monitors a PeakDo Link-Power over BLE and runs automation
// rules on an OpenWrt router. See docs/superpowers/specs/2026-07-14-wattline-router-design.md.
package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/keithah/openwrt-wattline/internal/actions"
	"github.com/keithah/openwrt-wattline/internal/api"
	"github.com/keithah/openwrt-wattline/internal/ble"
	"github.com/keithah/openwrt-wattline/internal/config"
	"github.com/keithah/openwrt-wattline/internal/rules"
	"github.com/keithah/openwrt-wattline/internal/state"
)

func main() {
	cfgPath := flag.String("config", "/etc/config/wattline", "UCI config path")
	flag.Parse()
	stop := make(chan struct{})
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	go func() { <-sig; close(stop) }()
	if err := run(*cfgPath, stop); err != nil {
		log.Fatalf("wattlined: %v", err)
	}
}

// tickOnce evaluates rules and dispatches any firings against the current device.
func tickOnce(eng *rules.Engine, store *state.Store, dev func() actions.Device,
	exec *actions.Executor, now time.Time) {
	snap := store.Snapshot()
	for _, f := range eng.Tick(snap, now) {
		log.Printf("wattline: rule %q fired", f.Rule.Name)
		if errs := exec.Execute(f.Rule.Actions, snap, f.Rule.Name, f.At); len(errs) > 0 {
			for _, e := range errs {
				log.Printf("wattline: action error for %q: %v", f.Rule.Name, e)
			}
		}
	}
}

func run(cfgPath string, stop <-chan struct{}) error {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return err
	}
	// The agent may fail to register when bluetoothd/the dongle come up after
	// the daemon; ensureAgent retries before each pair attempt (idempotent).
	var agentMu sync.Mutex
	agentOK := false
	ensureAgent := func() error {
		agentMu.Lock()
		defer agentMu.Unlock()
		if agentOK {
			return nil
		}
		if _, err := ble.RegisterPairingAgent(cfg.PIN); err != nil {
			return err
		}
		agentOK = true
		return nil
	}
	if err := ensureAgent(); err != nil {
		log.Printf("wattline: pairing agent unavailable (non-fatal, retried on pair): %v", err)
	}

	store := state.NewStore()
	eng, err := rules.NewEngine(cfg.Rules)
	if err != nil {
		return err
	}

	var (
		mu    sync.Mutex
		sess  *ble.Session
		ident ble.Identity
	)
	getDev := func() actions.Device {
		mu.Lock()
		defer mu.Unlock()
		if sess == nil {
			return noDevice{}
		}
		return sess
	}
	exec := actions.NewExecutor(deviceFunc(getDev), "Link-Power")

	dial := func() (ble.Transport, error) { return ble.ScanAndConnect("Link-Power") }
	conn := ble.NewConnector(dial, store, func(s *ble.Session, id ble.Identity) {
		mu.Lock()
		sess, ident = s, id
		mu.Unlock()
		log.Printf("wattline: connected to %s (%s) fw %s", id.Model, id.MAC, id.Firmware)
	})
	go conn.Run(stop)

	pairing := ble.NewPairing(ble.PairingDeps{
		Ops:     ble.NewLazyPairOps(),
		Prepare: ensureAgent,
		Pause:   conn.Pause,
		Resume:  conn.Resume,
		// Empty pin = restore the configured PIN (reloaded from disk, since a
		// prior successful pair may have persisted a new one).
		SetPIN: func(pin string) {
			if pin == "" {
				if c, err := config.Load(cfgPath); err == nil {
					pin = c.PIN
				}
			}
			ble.SetAgentPIN(pin)
		},
		// A pair only counts once the connector reconnects and survives the
		// protected handshake (continue.md: transient Paired: yes is not
		// success). The connector retries every 2s; give it a minute.
		WaitConnected: func() bool {
			deadline := time.Now().Add(60 * time.Second)
			for time.Now().Before(deadline) {
				if store.Snapshot().Connected {
					return true
				}
				time.Sleep(500 * time.Millisecond)
			}
			return false
		},
		Persist: func(mac, pin string) error { return config.SavePairing(cfgPath, mac, pin) },
	})

	srv := &http.Server{
		Addr: bindAddr(cfg),
		Handler: api.NewServer(api.Deps{
			Store: store, Engine: eng, Exec: exec, Token: cfg.Token,
			Pairing:   pairing,
			Identity:  func() ble.Identity { mu.Lock(); defer mu.Unlock(); return ident },
			Connected: func() bool { return store.Snapshot().Connected },
			LoadRules: func() []config.Rule {
				c, err := config.Load(cfgPath)
				if err != nil {
					log.Printf("wattline: LoadRules: %v", err)
					return nil
				}
				return c.Rules
			},
			SaveRules: func(rs []config.Rule) error {
				c, err := config.Load(cfgPath)
				if err != nil {
					return err
				}
				c.Rules = rs
				if err := c.SaveRules(cfgPath); err != nil {
					return err
				}
				return eng.SetRules(rs)
			},
		}),
	}
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("wattline: api server: %v", err)
		}
	}()
	log.Printf("wattline: API listening on %s", srv.Addr)

	// SIGHUP reload
	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)

	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			srv.Shutdown(ctx)
			cancel()
			return nil
		case <-hup:
			if c, err := config.Load(cfgPath); err == nil {
				if err := eng.SetRules(c.Rules); err != nil {
					log.Printf("wattline: reload rules failed: %v", err)
				} else {
					log.Printf("wattline: reloaded %d rules", len(c.Rules))
				}
			}
		case now := <-ticker.C:
			tickOnce(eng, store, getDev, exec, now)
		}
	}
}

func bindAddr(cfg *config.Config) string {
	host := "127.0.0.1"
	if cfg.LANAPI {
		host = "0.0.0.0"
	}
	return host + ":" + strconv.Itoa(cfg.Port)
}

// noDevice is used when no session is active: actions error clearly.
type noDevice struct{}

func (noDevice) DCControl(bool) error     { return errNotConnected }
func (noDevice) TypeCOutput(bool) error   { return errNotConnected }
func (noDevice) BypassControl(bool) error { return errNotConnected }
func (noDevice) Restart() error           { return errNotConnected }
func (noDevice) Shutdown() error          { return errNotConnected }

var errNotConnected = errNC("device not connected")

type errNC string

func (e errNC) Error() string { return string(e) }

// deviceFunc adapts a provider func to actions.Device by resolving per-call.
type deviceFunc func() actions.Device

func (f deviceFunc) DCControl(on bool) error     { return f().DCControl(on) }
func (f deviceFunc) TypeCOutput(on bool) error   { return f().TypeCOutput(on) }
func (f deviceFunc) BypassControl(on bool) error { return f().BypassControl(on) }
func (f deviceFunc) Restart() error              { return f().Restart() }
func (f deviceFunc) Shutdown() error             { return f().Shutdown() }
