package ble

import (
	"errors"
	"sync"
	"time"
)

// Found is one device seen during a pairing scan.
type Found struct {
	MAC    string `json:"mac"`
	Name   string `json:"name"`
	RSSI   int16  `json:"rssi"`
	Paired bool   `json:"paired"`
}

// PairOps abstracts the BlueZ pairing primitives (see bluez.go) so the
// Pairing manager is testable off-Linux.
type PairOps interface {
	Scan(dur time.Duration) ([]Found, error)
	Pair(mac string) error
	Trust(mac string) error
	Unpair(mac string) error
}

type PairingStage string

const (
	StageIdle     PairingStage = "idle"
	StageScanning PairingStage = "scanning"
	StagePairing  PairingStage = "pairing"
	StagePaired   PairingStage = "paired"
	StageError    PairingStage = "error"
)

// ErrBusy means a scan or pair operation is already in flight.
var ErrBusy = errors.New("pairing operation already in progress")

// PairingStatus is the API-facing snapshot of the pairing state machine.
type PairingStatus struct {
	Stage   PairingStage `json:"stage"`
	Error   string       `json:"error,omitempty"`
	Target  string       `json:"target,omitempty"`
	Devices []Found      `json:"devices"`
}

// PairingDeps wires the manager to the daemon: BlueZ primitives, connector
// pause/resume (the Link-Power accepts one central, so the connector must
// release the radio during scan/pair), the agent PIN setter, and UCI persist.
type PairingDeps struct {
	Ops     PairOps
	ScanFor time.Duration
	// Prepare runs before a pair attempt; used to (re)register the BlueZ
	// agent when it wasn't available at daemon startup.
	Prepare func() error
	Pause   func()
	Resume  func()
	// SetPIN updates the agent PIN. An empty string means "restore the
	// configured PIN" — the manager calls that after a failed attempt so a
	// wrong GUI PIN never outlives the operation that supplied it.
	SetPIN func(string)
	// WaitConnected blocks until the connector re-establishes a session (or
	// times out) after a pair reports success. BlueZ can report Paired: yes
	// without storing a long-term key; a reconnect that survives the
	// protected handshake is the only real proof the bond works.
	WaitConnected func() bool
	Persist       func(mac, pin string) error
}

// Pairing is an async state machine: idle → scanning|pairing → idle|paired|error.
// One operation at a time; concurrent starts get ErrBusy.
type Pairing struct {
	d PairingDeps

	mu      sync.Mutex
	stage   PairingStage
	err     string
	target  string
	devices []Found
}

func NewPairing(d PairingDeps) *Pairing {
	if d.ScanFor == 0 {
		d.ScanFor = 12 * time.Second
	}
	return &Pairing{d: d, stage: StageIdle}
}

func (p *Pairing) Status() PairingStatus {
	p.mu.Lock()
	defer p.mu.Unlock()
	return PairingStatus{
		Stage:   p.stage,
		Error:   p.err,
		Target:  p.target,
		Devices: append([]Found(nil), p.devices...),
	}
}

// busyLocked reports whether an operation is in flight. Callers hold p.mu.
func (p *Pairing) busyLocked() bool {
	return p.stage == StageScanning || p.stage == StagePairing
}

// begin transitions to stage if no operation is in flight.
func (p *Pairing) begin(stage PairingStage) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.busyLocked() {
		return ErrBusy
	}
	p.stage, p.err = stage, ""
	return nil
}

func (p *Pairing) finish(stage PairingStage, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err != nil {
		p.stage, p.err = StageError, err.Error()
		return
	}
	p.stage = stage
}

// StartScan kicks off an async device scan; results land in Status().Devices.
func (p *Pairing) StartScan() error {
	if err := p.begin(StageScanning); err != nil {
		return err
	}
	go func() {
		if p.d.Pause != nil {
			p.d.Pause()
		}
		defer func() {
			if p.d.Resume != nil {
				p.d.Resume()
			}
		}()
		devs, err := p.d.Ops.Scan(p.d.ScanFor)
		if err == nil {
			p.mu.Lock()
			p.devices = devs
			p.mu.Unlock()
		}
		p.finish(StageIdle, err)
	}()
	return nil
}

// StartPair kicks off an async pair+trust of mac. A non-empty pin overrides
// the configured agent PIN for this attempt and is persisted alongside the
// MAC on success; on failure the override is rolled back. An empty pin keeps
// the configured one. Success requires the connector to reconnect afterwards
// (WaitConnected): BlueZ can report a pair as done without storing a
// long-term key, and only a surviving protected handshake proves the bond.
func (p *Pairing) StartPair(mac, pin string) error {
	if err := p.begin(StagePairing); err != nil {
		return err
	}
	p.mu.Lock()
	p.target = mac
	p.mu.Unlock()
	go func() {
		if p.d.Prepare != nil {
			if err := p.d.Prepare(); err != nil {
				p.finish(StagePaired, err)
				return
			}
		}
		if pin != "" && p.d.SetPIN != nil {
			p.d.SetPIN(pin)
		}
		restorePIN := func() {
			if pin != "" && p.d.SetPIN != nil {
				p.d.SetPIN("")
			}
		}
		if p.d.Pause != nil {
			p.d.Pause()
		}
		err := p.d.Ops.Pair(mac)
		if err == nil {
			err = p.d.Ops.Trust(mac)
		}
		if p.d.Resume != nil {
			p.d.Resume()
		}
		if err != nil {
			restorePIN()
			p.finish(StagePaired, err)
			return
		}
		if p.d.WaitConnected != nil && !p.d.WaitConnected() {
			restorePIN()
			p.finish(StagePaired, errors.New(
				"pairing reported success but the device did not reconnect; "+
					"the bond may not have been stored — unpair it from other "+
					"hosts (phone/laptop) and try again"))
			return
		}
		if p.d.Persist != nil {
			err = p.d.Persist(mac, pin)
		}
		p.finish(StagePaired, err)
	}()
	return nil
}

// Unpair removes the bond for mac synchronously.
func (p *Pairing) Unpair(mac string) error {
	p.mu.Lock()
	busy := p.busyLocked()
	p.mu.Unlock()
	if busy {
		return ErrBusy
	}
	return p.d.Ops.Unpair(mac)
}
