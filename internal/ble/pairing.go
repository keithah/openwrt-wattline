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
	Pair(mac string, recover bool, report PairProgress) error
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

// PairingPhase is the stable, API-facing step within a scan or pair operation.
type PairingPhase string

const (
	PhasePreparingAdapter   PairingPhase = "preparing_adapter"
	PhaseClearingStaleBond  PairingPhase = "clearing_stale_bond"
	PhaseLocatingDevice     PairingPhase = "locating_device"
	PhaseExchangingPIN      PairingPhase = "exchanging_pin"
	PhaseAwaitingPIN        PairingPhase = "awaiting_pin"
	PhaseConfirmingBond     PairingPhase = "confirming_bond"
	PhaseTrustingDevice     PairingPhase = "trusting_device"
	PhaseReconnecting       PairingPhase = "reconnecting"
	PhaseVerifyingHandshake PairingPhase = "verifying_handshake"
	PhaseSavingPairing      PairingPhase = "saving_pairing"
	PhaseComplete           PairingPhase = "complete"
	PhaseFailed             PairingPhase = "failed"
)

// PairProgress receives curated progress safe to expose through the API.
type PairProgress func(PairingPhase, string)

// PairingEvent is one bounded, in-memory progress entry.
type PairingEvent struct {
	At      time.Time    `json:"at"`
	Phase   PairingPhase `json:"phase"`
	Message string       `json:"message"`
}

// ErrBusy means a scan or pair operation is already in flight.
var ErrBusy = errors.New("pairing operation already in progress")

// PairingStatus is the API-facing snapshot of the pairing state machine.
type PairingStatus struct {
	Stage       PairingStage   `json:"stage"`
	Phase       PairingPhase   `json:"phase,omitempty"`
	Message     string         `json:"message,omitempty"`
	Error       string         `json:"error,omitempty"`
	Target      string         `json:"target,omitempty"`
	StartedAt   *time.Time     `json:"started_at,omitempty"`
	UpdatedAt   *time.Time     `json:"updated_at,omitempty"`
	ElapsedMS   int64          `json:"elapsed_ms,omitempty"`
	Events      []PairingEvent `json:"events,omitempty"`
	Devices     []Found        `json:"devices"`
	PinRequired bool           `json:"pin_required,omitempty"`
	PinDeadline *time.Time     `json:"pin_deadline,omitempty"`
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
	Prompt *PasskeyPrompt
	// WaitConnected blocks until the connector re-establishes a session (or
	// times out) after a pair reports success. BlueZ can report Paired: yes
	// without storing a long-term key; a reconnect that survives the
	// protected handshake is the only real proof the bond works.
	WaitConnected func(PairProgress) bool
	Persist       func(mac, pin string) error
	Now           func() time.Time
}

// Pairing is an async state machine: idle → scanning|pairing → idle|paired|error.
// One operation at a time; concurrent starts get ErrBusy.
type Pairing struct {
	d PairingDeps

	mu       sync.Mutex
	stage    PairingStage
	err      string
	target   string
	devices  []Found
	phase    PairingPhase
	message  string
	started  time.Time
	updated  time.Time
	finished time.Time
	events   []PairingEvent
}

const pairingEventCap = 32

func NewPairing(d PairingDeps) *Pairing {
	if d.ScanFor == 0 {
		d.ScanFor = 12 * time.Second
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	return &Pairing{d: d, stage: StageIdle}
}

func (p *Pairing) Status() PairingStatus {
	p.mu.Lock()
	defer p.mu.Unlock()
	end := p.finished
	if end.IsZero() && !p.started.IsZero() {
		end = p.d.Now()
	}
	elapsed := end.Sub(p.started)
	if elapsed < 0 || p.started.IsZero() {
		elapsed = 0
	}
	var startedAt, updatedAt *time.Time
	if !p.started.IsZero() {
		started, updated := p.started, p.updated
		startedAt, updatedAt = &started, &updated
	}
	return PairingStatus{
		Stage: p.stage, Phase: p.phase, Message: p.message, Error: p.err,
		Target: p.target, StartedAt: startedAt, UpdatedAt: updatedAt,
		ElapsedMS:   elapsed.Milliseconds(),
		Events:      append([]PairingEvent(nil), p.events...),
		Devices:     append([]Found(nil), p.devices...),
		PinRequired: p.d.Prompt != nil && p.d.Prompt.Waiting(),
		PinDeadline: func() *time.Time {
			if p.d.Prompt == nil {
				return nil
			}
			d := p.d.Prompt.Deadline()
			if d.IsZero() {
				return nil
			}
			return &d
		}(),
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
	now := p.d.Now()
	p.stage, p.err, p.target = stage, "", ""
	p.phase, p.message = "", ""
	p.started, p.updated, p.finished = now, now, time.Time{}
	p.events = nil
	return nil
}

func (p *Pairing) setPhase(phase PairingPhase, message string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.phase == phase {
		return
	}
	now := p.d.Now()
	p.phase, p.message, p.updated = phase, message, now
	p.events = append(p.events, PairingEvent{At: now, Phase: phase, Message: message})
	if len(p.events) > pairingEventCap {
		p.events = append([]PairingEvent(nil), p.events[len(p.events)-pairingEventCap:]...)
	}
}

func (p *Pairing) finish(stage PairingStage, err error) {
	if err != nil {
		p.setPhase(PhaseFailed, err.Error())
		stage = StageError
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stage = stage
	if err != nil {
		p.err = err.Error()
	}
	p.finished = p.d.Now()
	p.updated = p.finished
}

// StartScan kicks off an async device scan; results land in Status().Devices.
func (p *Pairing) StartScan() error {
	if err := p.begin(StageScanning); err != nil {
		return err
	}
	p.setPhase(PhaseLocatingDevice, "Scanning for Link-Power devices")
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
			p.setPhase(PhaseComplete, "Scan complete")
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
	return p.startPair(mac, pin, false, false)
}

// StartRecover clears the router's stale BlueZ object and requests a fresh
// PIN bond. Link-Power exposes no device-side erase-all-bonds command.
func (p *Pairing) StartRecover(mac, pin string) error {
	return p.startPair(mac, pin, true, false)
}

func (p *Pairing) StartInteractive(mac string, recover bool) error {
	return p.startPair(mac, "", recover, true)
}
func (p *Pairing) SubmitPIN(pin string) error {
	if p.d.Prompt == nil || (!p.d.Prompt.Active() && !p.d.Prompt.Waiting()) {
		return ErrPasskeyNotWaiting
	}
	return p.d.Prompt.Submit(pin)
}
func (p *Pairing) Cancel() error {
	if p.d.Prompt == nil || (!p.d.Prompt.Active() && !p.d.Prompt.Waiting()) {
		return ErrPasskeyNotWaiting
	}
	p.d.Prompt.Cancel()
	return nil
}

func (p *Pairing) startPair(mac, pin string, recover bool, interactive bool) error {
	if err := p.begin(StagePairing); err != nil {
		return err
	}
	p.mu.Lock()
	p.target = mac
	p.mu.Unlock()
	p.setPhase(PhasePreparingAdapter, "Preparing the Bluetooth adapter")
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
		resumed := false
		resume := func() {
			if !resumed && p.d.Resume != nil {
				p.d.Resume()
			}
			resumed = true
		}
		defer resume()
		if interactive && p.d.Prompt != nil {
			p.d.Prompt.Activate(func() { p.setPhase(PhaseAwaitingPIN, "Waiting for pairing PIN") })
		}
		err := p.d.Ops.Pair(mac, recover, p.setPhase)
		if interactive && p.d.Prompt != nil {
			p.d.Prompt.Deactivate()
		}
		if err == nil {
			p.setPhase(PhaseTrustingDevice, "Trusting Link-Power on this router")
			err = p.d.Ops.Trust(mac)
		}
		if err != nil {
			restorePIN()
			p.finish(StagePaired, err)
			return
		}
		p.setPhase(PhaseReconnecting, "Reconnecting to Link-Power")
		resume()
		if p.d.WaitConnected != nil && !p.d.WaitConnected(p.setPhase) {
			restorePIN()
			p.finish(StagePaired, errors.New(
				"the protected reconnect did not complete; the replacement bond was not verified"))
			return
		}
		if p.d.Persist != nil {
			p.setPhase(PhaseSavingPairing, "Saving the verified pairing")
			err = p.d.Persist(mac, pin)
		}
		if err != nil {
			restorePIN()
			p.finish(StagePaired, err)
			return
		}
		p.setPhase(PhaseComplete, "Pairing verified")
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
