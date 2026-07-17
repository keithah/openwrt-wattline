package api

import (
	"net/http"
	"sort"
	"time"

	"github.com/keithah/openwrt-wattline/internal/proto"
	"github.com/keithah/openwrt-wattline/internal/state"
)

type deviceFeatures struct {
	Shutdown        bool `json:"shutdown"`
	DCBypass        bool `json:"dc_bypass"`
	DCBypassControl bool `json:"dc_bypass_control"`
	RunningMode     bool `json:"running_mode"`
	BarrierFree     bool `json:"barrier_free"`
	USBFirmware     bool `json:"usb_firmware"`
	BLEPIN          bool `json:"ble_pin"`
}

type deviceAvailable struct {
	CurrentTime bool `json:"current_time"`
	OTA         bool `json:"ota"`
	DC          bool `json:"dc"`
	USBC        bool `json:"usbc"`
}

type deviceConnection struct {
	Connected bool   `json:"connected"`
	Phase     string `json:"phase"`
	Reconnect string `json:"reconnect"`
}

type commandView struct {
	ID        string              `json:"id"`
	Operation string              `json:"operation"`
	Requested any                 `json:"requested,omitempty"`
	Phase     string              `json:"phase"`
	StartedAt time.Time           `json:"started_at"`
	UpdatedAt time.Time           `json:"updated_at"`
	Error     *state.CommandError `json:"error"`
}

type commandLists struct {
	Active []commandView `json:"active"`
	Recent []commandView `json:"recent"`
}

type deviceView struct {
	ID                  string           `json:"id"`
	Model               string           `json:"model"`
	HardwareRevision    string           `json:"hardware_revision"`
	ApplicationFirmware string           `json:"application_firmware"`
	OTAFirmware         string           `json:"ota_firmware"`
	CID                 uint16           `json:"cid"`
	FeaturesRaw         uint32           `json:"features_raw"`
	Features            deviceFeatures   `json:"features"`
	Available           deviceAvailable  `json:"available"`
	Mode                string           `json:"mode"`
	Connection          deviceConnection `json:"connection"`
	Commands            commandLists     `json:"commands"`
	MagicDNSName        string           `json:"magic_dns_name"`
}

type snapshotIdentity struct {
	ID   string `json:"id"`
	Mode string `json:"mode"`
}

type snapshotView struct {
	Battery   *proto.Battery   `json:"battery,omitempty"`
	DC        *proto.DCPort    `json:"dc,omitempty"`
	TypeC     *proto.TypeCPort `json:"typec,omitempty"`
	Connected bool             `json:"connected"`
	UpdatedAt time.Time        `json:"updated_at"`
	Identity  snapshotIdentity `json:"identity"`
	Commands  commandLists     `json:"commands"`
}

func snapshotResponse(snap state.Snapshot) snapshotView {
	identity := snapshotIdentity{}
	if snap.Device != nil {
		identity = snapshotIdentity{ID: snap.Device.MAC, Mode: snap.Device.Mode}
	}
	return snapshotView{Battery: snap.Battery, DC: snap.DC, TypeC: snap.TypeC, Connected: snap.Connected,
		UpdatedAt: snap.UpdatedAt, Identity: identity, Commands: commandsFromSnapshot(snap)}
}

func commandFromState(command state.Command) commandView {
	return commandView{ID: command.ID, Operation: command.Operation, Requested: command.Requested,
		Phase: command.Phase, StartedAt: command.StartedAt, UpdatedAt: command.UpdatedAt, Error: command.Error}
}

func commandsFromSnapshot(snap state.Snapshot) commandLists {
	commands := commandLists{Active: []commandView{}, Recent: []commandView{}}
	ids := make([]string, 0, len(snap.PendingCommands))
	for id := range snap.PendingCommands {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		commands.Active = append(commands.Active, commandFromState(snap.PendingCommands[id]))
	}
	for _, command := range snap.RecentCommands {
		commands.Recent = append(commands.Recent, commandFromState(command))
	}
	return commands
}

func (s *server) device(w http.ResponseWriter, r *http.Request) {
	if requireNoBody(r) != nil {
		writeAPIError(w, "invalid_request")
		return
	}
	snap := s.d.Store.Snapshot()
	id := state.Identity{}
	if snap.Device != nil {
		id = *snap.Device
	}
	phase := state.ConnectionDisconnected
	reconnect := "disarmed"
	if snap.Connection != nil {
		phase = snap.Connection.Phase
		if snap.Connection.ReconnectArmed {
			reconnect = "armed"
		}
	}
	if phase == state.ConnectionBootloader {
		reconnect = "bootloader"
	}
	magicDNS := ""
	if s.d.MagicDNSName != nil {
		magicDNS = s.d.MagicDNSName()
	}
	writeJSON(w, http.StatusOK, deviceView{
		ID: id.MAC, Model: id.Model, HardwareRevision: id.HWRev, ApplicationFirmware: id.AppFirmware,
		OTAFirmware: id.BootloaderFirmware, CID: id.CID, FeaturesRaw: id.Features,
		Features: deviceFeatures{Shutdown: id.FeatureSet.Shutdown, DCBypass: id.FeatureSet.DCBypass,
			DCBypassControl: id.FeatureSet.DCBypassControl, RunningMode: id.FeatureSet.FactoryMode,
			BarrierFree: id.Characteristics["command"], USBFirmware: id.FeatureSet.USBPort, BLEPIN: id.Characteristics["command"]},
		Available: deviceAvailable{CurrentTime: id.Characteristics["current_time"], OTA: id.Characteristics["ota"],
			DC: id.Characteristics["dc"], USBC: id.Characteristics["typec"]},
		Mode: id.Mode, Connection: deviceConnection{Connected: snap.Connected, Phase: phase, Reconnect: reconnect},
		Commands: commandsFromSnapshot(snap), MagicDNSName: magicDNS,
	})
}

type boolRequest struct {
	Enabled *bool `json:"enabled"`
}

func (s *server) setDC(w http.ResponseWriter, r *http.Request) {
	var req boolRequest
	if decodeJSON(r, &req) != nil || req.Enabled == nil {
		writeAPIError(w, "invalid_request")
		return
	}
	if s.d.DeviceControl == nil {
		writeAPIError(w, "device_disconnected")
		return
	}
	observed, err := s.d.DeviceControl.SetDC(r.Context(), *req.Enabled)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Enabled bool        `json:"enabled"`
		Command commandView `json:"command"`
	}{
		Enabled: observed.Enabled, Command: s.latestCommand("dc_output"),
	})
}

func (s *server) setTypeCOutput(w http.ResponseWriter, r *http.Request) {
	var req boolRequest
	if decodeJSON(r, &req) != nil || req.Enabled == nil {
		writeAPIError(w, "invalid_request")
		return
	}
	if s.d.DeviceControl == nil {
		writeAPIError(w, "device_disconnected")
		return
	}
	observed, err := s.d.DeviceControl.SetTypeCOutput(r.Context(), *req.Enabled)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Enabled bool        `json:"enabled"`
		Mode    uint8       `json:"mode"`
		Command commandView `json:"command"`
	}{
		Enabled: observed.Mode == 3, Mode: observed.Mode, Command: s.latestCommand("usbc_output"),
	})
}

func (s *server) setBypass(w http.ResponseWriter, r *http.Request) {
	var req boolRequest
	if decodeJSON(r, &req) != nil || req.Enabled == nil {
		writeAPIError(w, "invalid_request")
		return
	}
	if s.d.DeviceControl == nil {
		writeAPIError(w, "device_disconnected")
		return
	}
	observed, err := s.d.DeviceControl.SetBypass(r.Context(), *req.Enabled)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Enabled bool        `json:"enabled"`
		Command commandView `json:"command"`
	}{
		Enabled: observed.Bypass, Command: s.latestCommand("dc_bypass"),
	})
}

func (s *server) latestCommand(operation string) commandView {
	recent := s.d.Store.Snapshot().RecentCommands
	for i := len(recent) - 1; i >= 0; i-- {
		if recent[i].Operation == operation {
			return commandFromState(recent[i])
		}
	}
	return commandView{}
}

var canonicalLimitTypes = map[string]int{"global": proto.LimitGlobal, "input": proto.LimitInput, "output": proto.LimitOutput, "runtime": proto.LimitRuntime}

type limitView struct {
	Type  string `json:"type"`
	Level int    `json:"level"`
	Watts *int   `json:"watts"`
}

func limitResponse(name string, level int) limitView {
	if name == "runtime" && level == -1 {
		return limitView{Type: name, Level: level}
	}
	watts := proto.LevelToWatts(level)
	return limitView{Type: name, Level: level, Watts: &watts}
}

func (s *server) getLimit(w http.ResponseWriter, r *http.Request) {
	if requireNoBody(r) != nil {
		writeAPIError(w, "invalid_request")
		return
	}
	name := r.PathValue("type")
	typ, ok := canonicalLimitTypes[name]
	if !ok {
		writeAPIError(w, "invalid_request")
		return
	}
	if s.d.DeviceControl == nil {
		writeAPIError(w, "device_disconnected")
		return
	}
	level, err := s.d.DeviceControl.GetUSBCLimit(r.Context(), typ)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, limitResponse(name, level))
}

func (s *server) putLimit(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("type")
	typ, ok := canonicalLimitTypes[name]
	if !ok || typ == proto.LimitRuntime {
		writeAPIError(w, "invalid_request")
		return
	}
	var req struct {
		Watts *int `json:"watts"`
	}
	if decodeJSON(r, &req) != nil || req.Watts == nil {
		writeAPIError(w, "invalid_request")
		return
	}
	level := proto.WattsToLevel(*req.Watts)
	if level < 0 {
		writeAPIError(w, "invalid_request")
		return
	}
	if s.d.DeviceControl == nil {
		writeAPIError(w, "device_disconnected")
		return
	}
	observed, err := s.d.DeviceControl.PutUSBCLimit(r.Context(), typ, level)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, limitResponse(name, observed))
}

func (s *server) deleteLimit(w http.ResponseWriter, r *http.Request) {
	if requireNoBody(r) != nil {
		writeAPIError(w, "invalid_request")
		return
	}
	name := r.PathValue("type")
	typ, ok := canonicalLimitTypes[name]
	if !ok || typ == proto.LimitRuntime {
		writeAPIError(w, "invalid_request")
		return
	}
	if s.d.DeviceControl == nil {
		writeAPIError(w, "device_disconnected")
		return
	}
	observed, err := s.d.DeviceControl.DeleteUSBCLimit(r.Context(), typ)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, limitResponse(name, observed))
}

func (s *server) getThreshold(w http.ResponseWriter, r *http.Request) {
	if requireNoBody(r) != nil {
		writeAPIError(w, "invalid_request")
		return
	}
	if s.d.DeviceControl == nil {
		writeAPIError(w, "device_disconnected")
		return
	}
	volts, err := s.d.DeviceControl.GetBypassThreshold(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Volts float64 `json:"volts"`
	}{volts})
}

func (s *server) putThreshold(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Volts *float64 `json:"volts"`
	}
	if decodeJSON(r, &req) != nil || req.Volts == nil || *req.Volts <= 0 || *req.Volts > 60 {
		writeAPIError(w, "invalid_request")
		return
	}
	if s.d.DeviceControl == nil {
		writeAPIError(w, "device_disconnected")
		return
	}
	volts, err := s.d.DeviceControl.PutBypassThreshold(r.Context(), *req.Volts)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Volts float64 `json:"volts"`
	}{volts})
}
