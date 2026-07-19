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
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/keithah/openwrt-wattline/internal/actions"
	"github.com/keithah/openwrt-wattline/internal/api"
	"github.com/keithah/openwrt-wattline/internal/auth"
	"github.com/keithah/openwrt-wattline/internal/ble"
	"github.com/keithah/openwrt-wattline/internal/config"
	"github.com/keithah/openwrt-wattline/internal/control"
	"github.com/keithah/openwrt-wattline/internal/discovery"
	"github.com/keithah/openwrt-wattline/internal/rules"
	serverpkg "github.com/keithah/openwrt-wattline/internal/server"
	"github.com/keithah/openwrt-wattline/internal/state"
)

// version is replaced with the IPK version by package/Makefile.
var version = "dev"

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
	mu              sync.RWMutex
	cfg             *config.Config
	store           *auth.Store
	pairing         *auth.Pairing
	pendingOldStore *auth.Store
}

// tlsIdentity distinguishes the certificate loaded by the active listener
// from a newly staged certificate that takes effect only after restart.
type tlsIdentity struct {
	served serverpkg.Certificate
	names  []string
	paths  func() (string, string)
}

func newTLSIdentity(served serverpkg.Certificate, names []string) *tlsIdentity {
	return &tlsIdentity{served: served, names: append([]string(nil), names...)}
}

func (t *tlsIdentity) fingerprint() string { return t.served.SHA256 }

func (t *tlsIdentity) rotate() (string, error) {
	certFile, keyFile := t.served.CertFile, t.served.KeyFile
	if t.paths != nil {
		certFile, keyFile = t.paths()
	}
	rotated, err := serverpkg.RotateCertificate(certFile, keyFile, t.names)
	if err != nil {
		return "", err
	}
	return rotated.SHA256, nil
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
	l.mu.RLock()
	bootstrap := l.cfg.Token
	l.mu.RUnlock()
	if err := next.SaveMain(path); err != nil {
		return err
	}
	committed := cloneConfig(next)
	committed.Token = bootstrap
	l.mu.Lock()
	l.cfg = committed
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
		l.pendingOldStore = oldStore
		l.mu.Unlock()
		rebindRollback := l.pairing.RebindStore(newStore)
		storeRollback = func() {
			rebindRollback()
			l.mu.Lock()
			l.store = oldStore
			l.pendingOldStore = nil
			l.mu.Unlock()
		}
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

func (l *liveConfig) commit(before, after *config.Config) {
	if before.TokenStore == after.TokenStore {
		return
	}
	l.mu.Lock()
	oldStore := l.pendingOldStore
	l.pendingOldStore = nil
	l.mu.Unlock()
	if oldStore != nil {
		oldStore.InvalidateManagedSubscribers()
	}
}

func listenerConfig(cfg *config.Config) serverpkg.ListenerConfig {
	return serverpkg.ListenerConfig{
		HTTP:     serverpkg.Endpoint{Enabled: cfg.HTTPEnabled, Addr4: cfg.HTTPAddr4, Addr6: cfg.HTTPAddr6, Port: cfg.HTTPPort},
		HTTPS:    serverpkg.Endpoint{Enabled: cfg.HTTPSEnabled, Addr4: cfg.HTTPSAddr4, Addr6: cfg.HTTPSAddr6, Port: cfg.HTTPSPort},
		CertFile: cfg.TLSCert, KeyFile: cfg.TLSKey,
	}
}

func discoveryOptions(daemonVersion, hostname string, store *state.Store, live *liveConfig, tlsState *tlsIdentity, served serverpkg.ListenerConfig) discovery.Options {
	return discovery.Options{
		Version: daemonVersion, Hostname: hostname, Store: store,
		Config: func() *config.Config {
			current := live.current()
			current.HTTPEnabled, current.HTTPPort = served.HTTP.Enabled, served.HTTP.Port
			current.HTTPSEnabled, current.HTTPSPort = served.HTTPS.Enabled, served.HTTPS.Port
			return current
		},
		TLSFingerprint: tlsState.fingerprint, Logf: log.Printf,
	}
}

func preferredLANHost(hostname string) string {
	hostname = strings.TrimSuffix(strings.TrimSpace(hostname), ".")
	if hostname == "" {
		return "wattline.local"
	}
	if strings.HasSuffix(strings.ToLower(hostname), ".local") {
		return hostname
	}
	return hostname + ".local"
}

func pairingConnectionProgress(snapshot state.Snapshot) (ble.PairingPhase, string, bool) {
	if snapshot.Connected && snapshot.Connection != nil && snapshot.Connection.Phase == state.ConnectionReady {
		return "", "", true
	}
	if snapshot.Connection != nil && snapshot.Connection.Phase == state.ConnectionHandshaking {
		return ble.PhaseVerifyingHandshake, "Verifying the protected Wattline handshake", false
	}
	return ble.PhaseReconnecting, "Reconnecting to Link-Power", false
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
	tlsState := newTLSIdentity(certificate, []string{hostname})
	tlsState.paths = func() (string, string) {
		current := live.current()
		return current.TLSCert, current.TLSKey
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
	magicDNS := discovery.NewMagicDNSCache(discovery.Tailscale{})
	magicDNS.Refresh(context.Background())
	servedListeners := listenerConfig(cfg)
	discoveryService := discovery.NewService(discoveryOptions(version, hostname, store, live, tlsState, servedListeners))
	eng, err := rules.NewEngine(cfg.Rules)
	if err != nil {
		return err
	}

	var (
		identityMu sync.Mutex
		ident      ble.Identity
	)
	dial := func() (ble.Transport, error) {
		return ble.ScanAndConnectPrefixes([]string{"Link-Power", "PeakDo-OTA"})
	}
	conn := ble.NewConnector(dial, store, func(_ *ble.Session, id ble.Identity) {
		identityMu.Lock()
		ident = id
		identityMu.Unlock()
		log.Printf("wattline: connected to %s (%s) fw %s", id.Model, id.MAC, id.Firmware)
	})
	getDev := func() actions.Device {
		if session := conn.Session(); session != nil {
			return session
		}
		return noDevice{}
	}
	getSession := func() control.Session {
		return conn.Session()
	}
	exec := actions.NewExecutor(deviceFunc(getDev), "Link-Power")

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
		WaitConnected: func(report ble.PairProgress) bool {
			deadline := time.Now().Add(60 * time.Second)
			var last ble.PairingPhase
			for time.Now().Before(deadline) {
				phase, message, done := pairingConnectionProgress(store.Snapshot())
				if done {
					return true
				}
				if phase != last {
					report(phase, message)
					last = phase
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
		Identity:  func() ble.Identity { identityMu.Lock(); defer identityMu.Unlock(); return ident },
		Connected: func() bool { return store.Snapshot().Connected },
		Control: func() api.Control {
			return conn.Session()
		},
		DeviceControl: deviceControl,
		Settings:      live.current,
		SaveMain: func(next *config.Config) error {
			if err := live.save(cfgPath, next); err != nil {
				return err
			}
			discoveryService.Refresh()
			return nil
		},
		ApplySettings:  live.apply,
		CommitSettings: live.commit,
		TLSFingerprint: tlsState.fingerprint,
		RotateTLS:      tlsState.rotate,
		MagicDNSName:   magicDNS.Name,
		PreferredHost: func() string {
			return preferredLANHost(hostname)
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
	group, err := serverpkg.Start(context.Background(), servedListeners, handler)
	if err != nil {
		return err
	}
	log.Printf("wattline: HTTP API listeners started")
	discoveryContext, stopDiscovery := context.WithCancel(context.Background())
	discoveryDone := make(chan error, 1)
	go func() { discoveryDone <- discoveryService.Run(discoveryContext) }()
	defer func() {
		stopDiscovery()
		<-discoveryDone
	}()

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
			magicDNS.Refresh(context.Background())
			discoveryService.Refresh()
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
