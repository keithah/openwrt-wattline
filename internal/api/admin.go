package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/keithah/openwrt-wattline/internal/auth"
	"github.com/keithah/openwrt-wattline/internal/config"
	qrcode "github.com/skip2/go-qrcode"
)

func requesterIP(remoteAddr string) string {
	if address, err := netip.ParseAddrPort(remoteAddr); err == nil {
		return address.Addr().Unmap().String()
	}
	if address, err := netip.ParseAddr(strings.Trim(remoteAddr, "[]")); err == nil {
		return address.Unmap().String()
	}
	return "invalid"
}

func (s *server) clientPair(w http.ResponseWriter, r *http.Request) {
	var request struct {
		PIN   string `json:"pin"`
		Label string `json:"label"`
	}
	if err := decodeJSON(r, &request); err != nil || request.PIN == "" || request.Label == "" {
		writeAPIError(w, "invalid_request")
		return
	}
	s.settingsMu.RLock()
	defer s.settingsMu.RUnlock()
	if s.d.ClientPairing == nil {
		writeAPIError(w, "internal_error")
		return
	}
	metadata, err := s.connectionMetadata()
	if err != nil {
		writeAPIError(w, "internal_error")
		return
	}
	secret, tokenMetadata, err := s.d.ClientPairing.Exchange(requesterIP(r.RemoteAddr), request.PIN, request.Label)
	if err != nil {
		if errors.Is(err, auth.ErrInvalidOrExpiredPIN) {
			writeAPIError(w, "invalid_or_expired_pin")
		} else if errors.Is(err, auth.ErrInvalidLabel) {
			writeAPIError(w, "invalid_request")
		} else {
			writeAPIError(w, "internal_error")
		}
		return
	}
	writeJSON(w, http.StatusCreated, struct {
		Token         string            `json:"token"`
		TokenMetadata auth.TokenMeta    `json:"token_metadata"`
		DeviceID      string            `json:"device_id"`
		BaseURLs      map[string]string `json:"base_urls"`
		TLSSHA256     string            `json:"tls_sha256"`
		MagicDNSName  string            `json:"magic_dns_name"`
	}{secret, tokenMetadata, metadata.DeviceID, metadata.BaseURLs, metadata.TLSSHA256, s.magicDNSName()})
}

func (s *server) pairingModeStatus(w http.ResponseWriter, r *http.Request) {
	if requireNoBody(r) != nil {
		writeAPIError(w, "invalid_request")
		return
	}
	s.settingsMu.RLock()
	defer s.settingsMu.RUnlock()
	if s.d.ClientPairing == nil {
		writeAPIError(w, "internal_error")
		return
	}
	writeJSON(w, http.StatusOK, s.d.ClientPairing.Status(true))
}

func (s *server) openPairingMode(w http.ResponseWriter, r *http.Request) {
	if requireNoBody(r) != nil {
		writeAPIError(w, "invalid_request")
		return
	}
	s.settingsMu.RLock()
	defer s.settingsMu.RUnlock()
	if s.d.ClientPairing == nil {
		writeAPIError(w, "internal_error")
		return
	}
	writeJSON(w, http.StatusOK, s.d.ClientPairing.Open())
}

func (s *server) closePairingMode(w http.ResponseWriter, r *http.Request) {
	if requireNoBody(r) != nil {
		writeAPIError(w, "invalid_request")
		return
	}
	s.settingsMu.RLock()
	defer s.settingsMu.RUnlock()
	if s.d.ClientPairing == nil {
		writeAPIError(w, "internal_error")
		return
	}
	s.d.ClientPairing.Close()
	writeJSON(w, http.StatusOK, struct {
		Open bool `json:"open"`
	}{false})
}

func (s *server) pairingQRCode(w http.ResponseWriter, r *http.Request) {
	if requireNoBody(r) != nil || r.URL.RawQuery != "" {
		writeAPIError(w, "invalid_request")
		return
	}
	s.settingsMu.RLock()
	defer s.settingsMu.RUnlock()
	if s.d.ClientPairing == nil {
		writeAPIError(w, "internal_error")
		return
	}
	status := s.d.ClientPairing.Status(true)
	if !status.Open {
		writeAPIError(w, "capability_unsupported")
		return
	}
	metadata, err := s.connectionMetadata()
	if err != nil {
		writeAPIError(w, "internal_error")
		return
	}
	png, err := qrcode.Encode(pairingURIFromMetadata(metadata, status.PIN), qrcode.Medium, 256)
	if err != nil {
		writeAPIError(w, "internal_error")
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(png)
}

func (s *server) listTokens(w http.ResponseWriter, r *http.Request) {
	if requireNoBody(r) != nil {
		writeAPIError(w, "invalid_request")
		return
	}
	s.settingsMu.RLock()
	defer s.settingsMu.RUnlock()
	store := s.authStore()
	if store == nil {
		writeJSON(w, http.StatusOK, []auth.TokenMeta{})
		return
	}
	writeJSON(w, http.StatusOK, store.List())
}

func (s *server) revokeToken(w http.ResponseWriter, r *http.Request) {
	if requireNoBody(r) != nil {
		writeAPIError(w, "invalid_request")
		return
	}
	s.settingsMu.RLock()
	defer s.settingsMu.RUnlock()
	store := s.authStore()
	if store == nil {
		writeAPIError(w, "not_found")
		return
	}
	id := r.PathValue("id")
	if id == "" {
		writeAPIError(w, "invalid_request")
		return
	}
	if err := store.Revoke(id); err != nil {
		switch {
		case errors.Is(err, auth.ErrBootstrapToken):
			writeAPIError(w, "invalid_request")
		case errors.Is(err, auth.ErrTokenNotFound):
			writeAPIError(w, "not_found")
		default:
			writeAPIError(w, "internal_error")
		}
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Revoked string `json:"revoked"`
	}{id})
}

func (s *server) currentConfig() (*config.Config, bool) {
	if s.d.Settings == nil {
		return nil, false
	}
	cfg := s.d.Settings()
	if cfg == nil {
		return nil, false
	}
	copy := *cfg
	copy.MDNSInterfaces = append([]string(nil), cfg.MDNSInterfaces...)
	return &copy, true
}

type settingsTLS struct {
	Cert   string `json:"cert"`
	Key    string `json:"key"`
	SHA256 string `json:"sha256"`
}

type settingsResponse struct {
	HTTP            config.ListenerSettings `json:"http"`
	HTTPS           config.ListenerSettings `json:"https"`
	TLS             settingsTLS             `json:"tls"`
	TokenStore      string                  `json:"token_store"`
	PairingTTL      string                  `json:"pairing_ttl"`
	PairingAlwaysOn bool                    `json:"pairing_always_on"`
	Advanced        bool                    `json:"advanced"`
	MDNS            config.MDNSSettings     `json:"mdns"`
	WANAccess       bool                    `json:"wan_access"`
	BLEPIN          string                  `json:"ble_pin"`
	RestartRequired *bool                   `json:"restart_required,omitempty"`
}

func (s *server) settingsResponse(cfg *config.Config, restart *bool) settingsResponse {
	view := cfg.SettingsView()
	return settingsResponse{HTTP: view.HTTP, HTTPS: view.HTTPS,
		TLS:        settingsTLS{Cert: view.TLS.Cert, Key: view.TLS.Key, SHA256: s.tlsFingerprint()},
		TokenStore: view.TokenStore, PairingTTL: view.PairingTTL, PairingAlwaysOn: view.PairingAlwaysOn,
		Advanced: view.Advanced, MDNS: view.MDNS, WANAccess: view.WANAccess, BLEPIN: view.BLEPIN,
		RestartRequired: restart}
}

func (s *server) getSettings(w http.ResponseWriter, r *http.Request) {
	if requireNoBody(r) != nil {
		writeAPIError(w, "invalid_request")
		return
	}
	s.settingsMu.RLock()
	defer s.settingsMu.RUnlock()
	cfg, ok := s.currentConfig()
	if !ok {
		writeAPIError(w, "internal_error")
		return
	}
	writeJSON(w, http.StatusOK, s.settingsResponse(cfg, nil))
}

func decodeObject(raw json.RawMessage, allowed ...string) (map[string]json.RawMessage, error) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, errors.New("object is null")
	}
	var object map[string]json.RawMessage
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := decoder.Decode(&object); err != nil || object == nil {
		return nil, errors.New("invalid object")
	}
	allowedSet := make(map[string]struct{}, len(allowed))
	for _, key := range allowed {
		allowedSet[key] = struct{}{}
	}
	for key := range object {
		if _, ok := allowedSet[key]; !ok {
			return nil, fmt.Errorf("unknown field %s", key)
		}
	}
	return object, nil
}

func decodeValue[T any](raw json.RawMessage) (T, error) {
	var value T
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return value, errors.New("value is null")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := decoder.Decode(&value); err != nil {
		return value, err
	}
	return value, nil
}

func applyListenerPatch(raw json.RawMessage, enabled *bool, addr4, addr6 *string, port *int) error {
	object, err := decodeObject(raw, "enabled", "addr4", "addr6", "port")
	if err != nil {
		return err
	}
	if value, ok := object["enabled"]; ok {
		if *enabled, err = decodeValue[bool](value); err != nil {
			return err
		}
	}
	if value, ok := object["addr4"]; ok {
		if *addr4, err = decodeValue[string](value); err != nil {
			return err
		}
	}
	if value, ok := object["addr6"]; ok {
		if *addr6, err = decodeValue[string](value); err != nil {
			return err
		}
	}
	if value, ok := object["port"]; ok {
		if *port, err = decodeValue[int](value); err != nil {
			return err
		}
	}
	return nil
}

func applySettingsPatch(cfg *config.Config, raw json.RawMessage) error {
	object, err := decodeObject(raw, "http", "https", "tls", "token_store", "pairing_ttl", "pairing_always_on", "advanced", "mdns", "wan_access", "ble_pin")
	if err != nil {
		return err
	}
	if value, ok := object["http"]; ok {
		if err := applyListenerPatch(value, &cfg.HTTPEnabled, &cfg.HTTPAddr4, &cfg.HTTPAddr6, &cfg.HTTPPort); err != nil {
			return err
		}
		cfg.Port = cfg.HTTPPort
	}
	if value, ok := object["https"]; ok {
		if err := applyListenerPatch(value, &cfg.HTTPSEnabled, &cfg.HTTPSAddr4, &cfg.HTTPSAddr6, &cfg.HTTPSPort); err != nil {
			return err
		}
	}
	if value, ok := object["tls"]; ok {
		nested, err := decodeObject(value, "cert", "key")
		if err != nil {
			return err
		}
		if value, ok := nested["cert"]; ok {
			if cfg.TLSCert, err = decodeValue[string](value); err != nil {
				return err
			}
		}
		if value, ok := nested["key"]; ok {
			if cfg.TLSKey, err = decodeValue[string](value); err != nil {
				return err
			}
		}
	}
	if value, ok := object["token_store"]; ok {
		if cfg.TokenStore, err = decodeValue[string](value); err != nil {
			return err
		}
	}
	if value, ok := object["pairing_ttl"]; ok {
		text, err := decodeValue[string](value)
		if err != nil {
			return err
		}
		if cfg.PairingTTL, err = time.ParseDuration(text); err != nil {
			return err
		}
	}
	for key, destination := range map[string]*bool{"pairing_always_on": &cfg.PairingAlwaysOn, "advanced": &cfg.Advanced, "wan_access": &cfg.WANAccess} {
		if value, ok := object[key]; ok {
			if *destination, err = decodeValue[bool](value); err != nil {
				return err
			}
		}
	}
	if value, ok := object["ble_pin"]; ok {
		if cfg.BLEPIN, err = decodeValue[string](value); err != nil {
			return err
		}
		if len(cfg.BLEPIN) != 6 {
			return errors.New("BLE PIN must be six digits")
		}
		for _, digit := range cfg.BLEPIN {
			if digit < '0' || digit > '9' {
				return errors.New("BLE PIN must be six digits")
			}
		}
		cfg.PIN = cfg.BLEPIN
	}
	if value, ok := object["mdns"]; ok {
		nested, err := decodeObject(value, "enabled", "interfaces")
		if err != nil {
			return err
		}
		if value, ok := nested["enabled"]; ok {
			if cfg.MDNSEnabled, err = decodeValue[bool](value); err != nil {
				return err
			}
		}
		if value, ok := nested["interfaces"]; ok {
			if cfg.MDNSInterfaces, err = decodeValue[[]string](value); err != nil {
				return err
			}
		}
	}
	return cfg.Validate()
}

func restartFieldsChanged(before, after *config.Config) bool {
	return before.HTTPEnabled != after.HTTPEnabled || before.HTTPAddr4 != after.HTTPAddr4 || before.HTTPAddr6 != after.HTTPAddr6 || before.HTTPPort != after.HTTPPort ||
		before.HTTPSEnabled != after.HTTPSEnabled || before.HTTPSAddr4 != after.HTTPSAddr4 || before.HTTPSAddr6 != after.HTTPSAddr6 || before.HTTPSPort != after.HTTPSPort ||
		before.TLSCert != after.TLSCert || before.TLSKey != after.TLSKey || before.MDNSEnabled != after.MDNSEnabled ||
		strings.Join(before.MDNSInterfaces, "\x00") != strings.Join(after.MDNSInterfaces, "\x00") || before.WANAccess != after.WANAccess
}

func liveFieldsChanged(before, after *config.Config) bool {
	return before.PairingTTL != after.PairingTTL || before.PairingAlwaysOn != after.PairingAlwaysOn ||
		before.Advanced != after.Advanced || before.BLEPIN != after.BLEPIN || before.TokenStore != after.TokenStore
}

func (s *server) putSettings(w http.ResponseWriter, r *http.Request) {
	s.settingsMu.Lock()
	defer s.settingsMu.Unlock()
	before, ok := s.currentConfig()
	if !ok || s.d.SaveMain == nil {
		writeAPIError(w, "internal_error")
		return
	}
	var raw json.RawMessage
	if err := decodeJSON(r, &raw); err != nil {
		writeAPIError(w, "invalid_request")
		return
	}
	after := *before
	after.MDNSInterfaces = append([]string(nil), before.MDNSInterfaces...)
	if err := applySettingsPatch(&after, raw); err != nil {
		writeAPIError(w, "invalid_request")
		return
	}
	rollback := func() {}
	if liveFieldsChanged(before, &after) {
		if s.d.ApplySettings == nil {
			writeAPIError(w, "internal_error")
			return
		}
		var err error
		rollback, err = s.d.ApplySettings(before, &after)
		if err != nil {
			writeAPIError(w, "internal_error")
			return
		}
		if rollback == nil {
			rollback = func() {}
		}
	}
	if err := s.d.SaveMain(&after); err != nil {
		rollback()
		writeAPIError(w, "internal_error")
		return
	}
	restart := restartFieldsChanged(before, &after)
	writeJSON(w, http.StatusOK, s.settingsResponse(&after, &restart))
}

func (s *server) deviceID() string {
	if s.d.Store != nil {
		if device := s.d.Store.Snapshot().Device; device != nil && device.MAC != "" {
			return device.MAC
		}
	}
	if s.d.Identity != nil {
		return s.d.Identity().MAC
	}
	return ""
}

func (s *server) magicDNSName() string {
	if s.d.MagicDNSName != nil {
		return s.d.MagicDNSName()
	}
	return ""
}

func (s *server) preferredHost() string {
	if magic := s.magicDNSName(); magic != "" {
		return magic
	}
	if s.d.PreferredHost != nil {
		return s.d.PreferredHost()
	}
	return "wattline.lan"
}

func (s *server) tlsFingerprint() string {
	if s.d.TLSFingerprint != nil {
		return s.d.TLSFingerprint()
	}
	return ""
}

func hostPort(host string, port int) string {
	return net.JoinHostPort(host, strconv.Itoa(port))
}

func baseURLsFor(cfg *config.Config, host string) map[string]string {
	urls := map[string]string{}
	if cfg.HTTPEnabled {
		urls["http"] = "http://" + hostPort(host, cfg.HTTPPort) + "/api/v1"
	}
	if cfg.HTTPSEnabled {
		urls["https"] = "https://" + hostPort(host, cfg.HTTPSPort) + "/api/v1"
	}
	return urls
}

type connectionMetadata struct {
	Config    *config.Config
	DeviceID  string
	Host      string
	BaseURLs  map[string]string
	TLSSHA256 string
}

func validFingerprint(fingerprint string) bool {
	if len(fingerprint) != 64 {
		return false
	}
	for _, character := range fingerprint {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func (s *server) connectionMetadata() (connectionMetadata, error) {
	cfg, ok := s.currentConfig()
	if !ok || cfg.Validate() != nil {
		return connectionMetadata{}, errors.New("connection settings unavailable")
	}
	deviceID := strings.TrimSpace(s.deviceID())
	host := strings.TrimSpace(s.preferredHost())
	if deviceID == "" || host == "" {
		return connectionMetadata{}, errors.New("connection identity unavailable")
	}
	urls := baseURLsFor(cfg, host)
	if len(urls) == 0 {
		return connectionMetadata{}, errors.New("no API listener enabled")
	}
	fingerprint := s.tlsFingerprint()
	if cfg.HTTPSEnabled && !validFingerprint(fingerprint) {
		return connectionMetadata{}, errors.New("HTTPS fingerprint unavailable")
	}
	return connectionMetadata{Config: cfg, DeviceID: deviceID, Host: host, BaseURLs: urls, TLSSHA256: fingerprint}, nil
}

func escapeRFC3986(value string) string {
	const hex = "0123456789ABCDEF"
	var out strings.Builder
	for i := 0; i < len(value); i++ {
		b := value[i]
		if (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z') || (b >= '0' && b <= '9') || strings.ContainsRune("-._~", rune(b)) {
			out.WriteByte(b)
		} else {
			out.WriteByte('%')
			out.WriteByte(hex[b>>4])
			out.WriteByte(hex[b&15])
		}
	}
	return out.String()
}

func (s *server) pairingURI(pin string) string {
	s.settingsMu.RLock()
	defer s.settingsMu.RUnlock()
	metadata, err := s.connectionMetadata()
	if err != nil {
		return ""
	}
	return pairingURIFromMetadata(metadata, pin)
}

func pairingURIFromMetadata(metadata connectionMetadata, pin string) string {
	parts := []string{"v=1", "id=" + escapeRFC3986(metadata.DeviceID), "host=" + escapeRFC3986(metadata.Host)}
	if metadata.Config.HTTPEnabled {
		parts = append(parts, "http="+strconv.Itoa(metadata.Config.HTTPPort))
	}
	if metadata.Config.HTTPSEnabled {
		parts = append(parts, "https="+strconv.Itoa(metadata.Config.HTTPSPort))
	}
	parts = append(parts, "pin="+escapeRFC3986(pin))
	if metadata.Config.HTTPSEnabled {
		parts = append(parts, "tls="+escapeRFC3986(metadata.TLSSHA256))
	}
	return "wattline://pair?" + strings.Join(parts, "&")
}
