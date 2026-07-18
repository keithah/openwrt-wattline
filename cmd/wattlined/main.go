// Command wattlined monitors a PeakDo Link-Power over BLE and runs automation
// rules on an OpenWrt router. See docs/superpowers/specs/2026-07-14-wattline-router-design.md.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/keithah/openwrt-wattline/internal/actions"
	"github.com/keithah/openwrt-wattline/internal/api"
	"github.com/keithah/openwrt-wattline/internal/auth"
	"github.com/keithah/openwrt-wattline/internal/ble"
	"github.com/keithah/openwrt-wattline/internal/config"
	"github.com/keithah/openwrt-wattline/internal/control"
	"github.com/keithah/openwrt-wattline/internal/rules"
	serverpkg "github.com/keithah/openwrt-wattline/internal/server"
	"github.com/keithah/openwrt-wattline/internal/state"
)

func main() {
	cfgPath := flag.String("config", "/etc/config/wattline", "UCI config path")
	initOnly := flag.Bool("init", false, "initialize bootstrap credentials and TLS certificate, then exit")
	flag.Parse()
	hostname, _ := os.Hostname()
	if *initOnly {
		if _, err := initialize(*cfgPath, []string{hostname}); err != nil {
			log.Fatalf("wattlined: initialize: %v", err)
		}
		return
	}
	stop := make(chan struct{})
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	go func() { <-sig; close(stop) }()
	if err := run(*cfgPath, stop); err != nil {
		log.Fatalf("wattlined: %v", err)
	}
}

func randomBootstrapToken() (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}

func initialize(cfgPath string, names []string) (serverpkg.Certificate, error) {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return serverpkg.Certificate{}, err
	}
	certificate, err := serverpkg.EnsureCertificate(cfg.TLSCert, cfg.TLSKey, names)
	if err != nil {
		return serverpkg.Certificate{}, err
	}
	candidate, err := randomBootstrapToken()
	if err != nil {
		return serverpkg.Certificate{}, fmt.Errorf("generate bootstrap token: %w", err)
	}
	if _, err := config.EnsureBootstrapToken(cfgPath, candidate); err != nil {
		return serverpkg.Certificate{}, fmt.Errorf("save bootstrap token: %w", err)
	}
	cfg, err = config.Load(cfgPath)
	if err != nil {
		return serverpkg.Certificate{}, err
	}
	if _, err := auth.OpenStore(cfg.TokenStore, cfg.Token); err != nil {
		return serverpkg.Certificate{}, fmt.Errorf("initialize token store: %w", err)
	}
	return certificate, nil
}

type liveConfig struct {
	mu      sync.RWMutex
	cfg     *config.Config
	store   *auth.Store
	pairing *auth.Pairing
}

func cloneConfig(cfg *config.Config) *config.Config {
	if cfg == nil {
		return nil
	}
	copy := *cfg
	copy.MDNSInterfaces = append([]string(nil), cfg.MDNSInterfaces...)
	copy.Rules = append([]config.Rule(nil), cfg.Rules...)
	return &copy
}

func (l *liveConfig) current() *config.Config {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return cloneConfig(l.cfg)
}

func (l *liveConfig) authStore() *auth.Store {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.store
}

func (l *liveConfig) save(path string, next *config.Config) error {
	if err := next.SaveMain(path); err != nil {
		return err
	}
	fresh, err := config.Load(path)
	if err != nil {
		return err
	}
	l.mu.Lock()
	l.cfg = cloneConfig(fresh)
	l.mu.Unlock()
	return nil
}

func (l *liveConfig) reload(path string) error {
	fresh, err := config.Load(path)
	if err != nil {
		return err
	}
	l.mu.Lock()
	l.cfg = cloneConfig(fresh)
	l.mu.Unlock()
	return nil
}

func (l *liveConfig) apply(before, after *config.Config) (func(), error) {
	var newStore *auth.Store
	var err error
	if before.TokenStore != after.TokenStore {
		newStore, err = auth.OpenStore(after.TokenStore, before.Token)
		if err != nil {
			return nil, err
		}
	}
	pairRollback := func() {}
	if before.PairingTTL != after.PairingTTL || before.PairingAlwaysOn != after.PairingAlwaysOn {
		pairRollback, err = l.pairing.Reconfigure(after.PairingTTL, after.PairingAlwaysOn)
		if err != nil {
			return nil, err
		}
	}
	storeRollback := func() {}
	if newStore != nil {
		l.mu.Lock()
		oldStore := l.store
		l.store = newStore
		l.mu.Unlock()
		rebindRollback := l.pairing.RebindStore(newStore)
		storeRollback = func() { rebindRollback(); l.mu.Lock(); l.store = oldStore; l.mu.Unlock() }
	}
	if before.BLEPIN != after.BLEPIN {
		ble.SetAgentPIN(after.BLEPIN)
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			if before.BLEPIN != after.BLEPIN {
				ble.SetAgentPIN(before.BLEPIN)
			}
			storeRollback()
			pairRollback()
		})
	}, nil
}

func listenerConfig(cfg *config.Config) serverpkg.ListenerConfig {
	return serverpkg.ListenerConfig{
		HTTP:     serverpkg.Endpoint{Enabled: cfg.HTTPEnabled, Addr4: cfg.HTTPAddr4, Addr6: cfg.HTTPAddr6, Port: cfg.HTTPPort},
		HTTPS:    serverpkg.Endpoint{Enabled: cfg.HTTPSEnabled, Addr4: cfg.HTTPSAddr4, Addr6: cfg.HTTPSAddr6, Port: cfg.HTTPSPort},
		CertFile: cfg.TLSCert, KeyFile: cfg.TLSKey,
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
	if cfg.Token == "" {
		return errors.New("bootstrap token is missing; run wattlined -init")
	}
	hostname, _ := os.Hostname()
	certificate, err := serverpkg.EnsureCertificate(cfg.TLSCert, cfg.TLSKey, []string{hostname})
	if err != nil {
		return err
	}
	tokenStore, err := auth.OpenStore(cfg.TokenStore, cfg.Token)
	if err != nil {
		return err
	}
	clientPairing := auth.NewPairing(tokenStore, cfg.PairingTTL, cfg.PairingAlwaysOn)
	live := &liveConfig{cfg: cloneConfig(cfg), store: tokenStore, pairing: clientPairing}
	var certMu sync.RWMutex
	fingerprint := certificate.SHA256
	getFingerprint := func() string { certMu.RLock(); defer certMu.RUnlock(); return fingerprint }
	rotateTLS := func() (string, error) {
		current := live.current()
		rotated, err := serverpkg.RotateCertificate(current.TLSCert, current.TLSKey, []string{hostname})
		if err != nil {
			return "", err
		}
		certMu.Lock()
		fingerprint = rotated.SHA256
		certMu.Unlock()
		return rotated.SHA256, nil
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
		if _, err := ble.RegisterPairingAgent(live.current().BLEPIN); err != nil {
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
	getSession := func() control.Session {
		mu.Lock()
		defer mu.Unlock()
		if sess == nil {
			return nil
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
	deviceControl := control.NewService(getSession, store, conn, func() bool { return live.current().Advanced })

	pairing := ble.NewPairing(ble.PairingDeps{
		Ops:     ble.NewLazyPairOps(),
		Prepare: ensureAgent,
		Pause:   conn.Pause,
		Resume:  conn.Resume,
		// Empty pin = restore the configured PIN (reloaded from disk, since a
		// prior successful pair may have persisted a new one).
		SetPIN: func(pin string) {
			if pin == "" {
				pin = live.current().BLEPIN
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
		Persist: func(mac, pin string) error {
			if err := config.SavePairing(cfgPath, mac, pin); err != nil {
				return err
			}
			return live.reload(cfgPath)
		},
	})

	handler := api.NewServer(api.Deps{
		Store: store, Engine: eng, Exec: exec, Token: cfg.Token,
		Pairing: pairing,
		Auth:    tokenStore, AuthStore: live.authStore, ClientPairing: clientPairing,
		Identity:  func() ble.Identity { mu.Lock(); defer mu.Unlock(); return ident },
		Connected: func() bool { return store.Snapshot().Connected },
		Control: func() api.Control {
			mu.Lock()
			defer mu.Unlock()
			if sess == nil {
				return nil
			}
			return sess
		},
		DeviceControl:  deviceControl,
		Settings:       live.current,
		SaveMain:       func(next *config.Config) error { return live.save(cfgPath, next) },
		ApplySettings:  live.apply,
		TLSFingerprint: getFingerprint,
		RotateTLS:      rotateTLS,
		PreferredHost: func() string {
			if hostname != "" {
				return hostname
			}
			return "wattline.lan"
		},
		SaveBLEPIN: func(pin string) error {
			current := live.current()
			before := cloneConfig(current)
			current.BLEPIN, current.PIN = pin, pin
			rollback, err := live.apply(before, current)
			if err != nil {
				return err
			}
			if err := live.save(cfgPath, current); err != nil {
				rollback()
				return err
			}
			return nil
		},
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
	})
	group, err := serverpkg.Start(context.Background(), listenerConfig(cfg), handler)
	if err != nil {
		return err
	}
	log.Printf("wattline: HTTP API listeners started")

	// SIGHUP reload
	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)

	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			err := group.Shutdown(ctx)
			cancel()
			return err
		case err := <-group.Errors():
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			_ = group.Shutdown(ctx)
			cancel()
			return fmt.Errorf("API listener: %w", err)
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
