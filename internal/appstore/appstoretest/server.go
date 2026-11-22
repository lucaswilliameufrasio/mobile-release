// Package appstoretest is an in-memory fake of the App Store Connect
// provisioning endpoints for tests. It verifies the ES256 token, keeps
// bundle ids, capabilities, profiles, certificates and devices, behaves like
// the real API where it matters (enabling a capability invalidates the
// bundle id's profiles, profile names are unique) and can inject failures.
package appstoretest

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lucaswilliameufrasio/mobile-release/v2/internal/appstore"
)

// Bundle is a stored bundle id.
type Bundle struct {
	ID, Identifier, Name, SeedID string
	Capabilities                 []string
}

// Profile is a stored profile.
type Profile struct {
	ID, Name, Type, State, UUID, BundleID string
	Expires                               time.Time
	CertificateIDs, DeviceIDs             []string
}

// Certificate is a stored certificate.
type Certificate struct {
	ID, Type, Name, Serial string
	Expires                time.Time
}

// Device is a stored device.
type Device struct {
	ID, Name, UDID string
}

// Request is a recorded call.
type Request struct {
	Method, Path string
	Body         map[string]any
}

// Server is the fake API.
type Server struct {
	*httptest.Server
	Key    appstore.Key
	Now    time.Time
	TeamID string
	// PageSize forces pagination of list endpoints (default 200).
	PageSize int
	// Fail returns an error status for "METHOD /path" (exact path, without
	// query) the given number of times, then succeeds.
	Fail map[string]Failure

	mu           sync.Mutex
	next         int
	Bundles      map[string]*Bundle
	Profiles     map[string]*Profile
	Certificates map[string]*Certificate
	Devices      map[string]*Device
	Requests     []Request
	public       *ecdsa.PublicKey
}

// Failure is an injected error.
type Failure struct {
	Status int
	Times  int // 0 means always
	Detail string
	// RetryAfter is sent as the Retry-After header (429).
	RetryAfter string
	// After lets this many matching requests through first.
	After int
}

// New starts a fake server with a fresh API key.
func New(t *testing.T) *Server {
	t.Helper()
	private, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(private)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{
		Key:          appstore.Key{KeyID: "KEY123", IssuerID: "issuer-uuid", PrivateKey: pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})},
		Now:          time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC),
		TeamID:       "ABCDE12345",
		Fail:         map[string]Failure{},
		Bundles:      map[string]*Bundle{},
		Profiles:     map[string]*Profile{},
		Certificates: map[string]*Certificate{},
		Devices:      map[string]*Device{},
		public:       &private.PublicKey,
	}
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.Close)
	return s
}

// Client returns a client pointed at the fake, without real sleeps.
func (s *Server) Client(t *testing.T) *appstore.Client {
	t.Helper()
	client, err := appstore.New(s.Key)
	if err != nil {
		t.Fatal(err)
	}
	client.BaseURL = s.URL
	client.Now = func() time.Time { return s.Now }
	client.Sleep = func(context.Context, time.Duration) error { return nil }
	return client
}

func (s *Server) id(prefix string) string {
	s.next++
	return fmt.Sprintf("%s%03d", prefix, s.next)
}

// AddBundle stores a bundle id with capabilities.
func (s *Server) AddBundle(identifier string, capabilities ...string) *Bundle {
	s.mu.Lock()
	defer s.mu.Unlock()
	bundle := &Bundle{ID: s.id("B"), Identifier: identifier, Name: identifier, SeedID: s.TeamID, Capabilities: capabilities}
	s.Bundles[bundle.ID] = bundle
	return bundle
}

// AddCertificate stores a certificate expiring in days.
func (s *Server) AddCertificate(kind string, days int) *Certificate {
	s.mu.Lock()
	defer s.mu.Unlock()
	certificate := &Certificate{ID: s.id("C"), Type: kind, Name: kind, Serial: strconv.Itoa(s.next), Expires: s.Now.AddDate(0, 0, days)}
	s.Certificates[certificate.ID] = certificate
	return certificate
}

// AddDevice stores an enabled device.
func (s *Server) AddDevice(name string) *Device {
	s.mu.Lock()
	defer s.mu.Unlock()
	device := &Device{ID: s.id("D"), Name: name, UDID: fmt.Sprintf("udid-%d", s.next)}
	s.Devices[device.ID] = device
	return device
}

// AddProfile stores a profile.
func (s *Server) AddProfile(bundle *Bundle, name, kind, state string, days int, certificates []string, devices []string) *Profile {
	s.mu.Lock()
	defer s.mu.Unlock()
	profile := &Profile{ID: s.id("P"), Name: name, Type: kind, State: state, BundleID: bundle.ID, Expires: s.Now.AddDate(0, 0, days), CertificateIDs: certificates, DeviceIDs: devices}
	profile.UUID = "uuid-" + profile.ID
	s.Profiles[profile.ID] = profile
	return profile
}

// Writes returns the recorded POST, PATCH and DELETE calls.
func (s *Server) Writes() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var writes []string
	for _, request := range s.Requests {
		if request.Method != http.MethodGet {
			writes = append(writes, request.Method+" "+request.Path)
		}
	}
	return writes
}

// ProfilesOf returns the profiles of identifier, sorted by name.
func (s *Server) ProfilesOf(identifier string) []Profile {
	s.mu.Lock()
	defer s.mu.Unlock()
	var profiles []Profile
	for _, profile := range s.Profiles {
		if s.Bundles[profile.BundleID].Identifier == identifier {
			profiles = append(profiles, *profile)
		}
	}
	sort.Slice(profiles, func(i, j int) bool { return profiles[i].Name+profiles[i].Type < profiles[j].Name+profiles[j].Type })
	return profiles
}

// Bundle returns the stored bundle with identifier.
func (s *Server) Bundle(identifier string) *Bundle {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, bundle := range s.Bundles {
		if bundle.Identifier == identifier {
			return bundle
		}
	}
	return nil
}

// Content is the fake .mobileprovision body for a profile: like the real
// ones, the signed payload is a plist with the name and UUID.
func Content(name, uuid string) []byte {
	return []byte("<plist><dict><key>Name</key>\n\t<string>" + name + "</string><key>UUID</key><string>" + uuid + "</string></dict></plist>")
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var body map[string]any
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&body)
	}
	s.Requests = append(s.Requests, Request{Method: r.Method, Path: r.URL.Path, Body: body})

	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	claims, err := appstore.VerifyJWT(token, s.public)
	if !ok || err != nil || claims["aud"] != "appstoreconnect-v1" || claims["iss"] != s.Key.IssuerID {
		writeError(w, http.StatusUnauthorized, "NOT_AUTHORIZED", "bad token")
		return
	}
	if failure, ok := s.Fail[r.Method+" "+r.URL.Path]; ok && failure.After > 0 {
		failure.After--
		s.Fail[r.Method+" "+r.URL.Path] = failure
	} else if ok {
		switch {
		case failure.Times == 1:
			delete(s.Fail, r.Method+" "+r.URL.Path)
		case failure.Times > 1:
			failure.Times--
			s.Fail[r.Method+" "+r.URL.Path] = failure
		}
		if failure.RetryAfter != "" {
			w.Header().Set("Retry-After", failure.RetryAfter)
		}
		writeError(w, failure.Status, "INJECTED", firstNonEmpty(failure.Detail, "injected failure"))
		return
	}

	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/v1/bundleIds":
		filter := r.URL.Query().Get("filter[identifier]")
		var items []map[string]any
		for _, bundle := range s.sortedBundles() {
			if strings.Contains(bundle.Identifier, filter) {
				items = append(items, bundleResource(bundle))
			}
		}
		s.page(w, r, items)
	case r.Method == http.MethodPost && r.URL.Path == "/v1/bundleIds":
		attributes := dig(body, "data", "attributes")
		identifier, _ := attributes["identifier"].(string)
		for _, bundle := range s.Bundles {
			if bundle.Identifier == identifier {
				writeError(w, http.StatusConflict, "ENTITY_ERROR", "identifier already registered")
				return
			}
		}
		name, _ := attributes["name"].(string)
		bundle := &Bundle{ID: s.id("B"), Identifier: identifier, Name: name, SeedID: s.TeamID}
		s.Bundles[bundle.ID] = bundle
		writeJSON(w, http.StatusCreated, map[string]any{"data": bundleResource(bundle)})
	case r.Method == http.MethodGet && len(parts) == 4 && parts[1] == "bundleIds" && parts[3] == "bundleIdCapabilities":
		bundle := s.Bundles[parts[2]]
		if bundle == nil {
			writeError(w, http.StatusNotFound, "NOT_FOUND", "bundle id")
			return
		}
		var items []map[string]any
		for _, capability := range bundle.Capabilities {
			items = append(items, map[string]any{"type": "bundleIdCapabilities", "id": bundle.ID + "_" + capability, "attributes": map[string]any{"capabilityType": capability}})
		}
		s.page(w, r, items)
	case r.Method == http.MethodPost && r.URL.Path == "/v1/bundleIdCapabilities":
		capability, _ := dig(body, "data", "attributes")["capabilityType"].(string)
		bundleID, _ := dig(body, "data", "relationships", "bundleId", "data")["id"].(string)
		bundle := s.Bundles[bundleID]
		if bundle == nil {
			writeError(w, http.StatusNotFound, "NOT_FOUND", "bundle id")
			return
		}
		if capability == "APPLE_ID_AUTH" && dig(body, "data", "attributes")["settings"] == nil {
			writeError(w, http.StatusUnprocessableEntity, "ENTITY_ERROR", "APPLE_ID_AUTH needs settings")
			return
		}
		bundle.Capabilities = append(bundle.Capabilities, capability)
		// Like the real portal: profiles of the App ID become invalid.
		for _, profile := range s.Profiles {
			if profile.BundleID == bundle.ID {
				profile.State = appstore.ProfileStateInvalid
			}
		}
		writeJSON(w, http.StatusCreated, map[string]any{"data": map[string]any{"type": "bundleIdCapabilities", "id": bundle.ID + "_" + capability, "attributes": map[string]any{"capabilityType": capability}}})
	case r.Method == http.MethodGet && len(parts) == 4 && parts[1] == "bundleIds" && parts[3] == "profiles":
		var items []map[string]any
		for _, profile := range s.sortedProfiles() {
			if profile.BundleID == parts[2] {
				items = append(items, profileResource(profile))
			}
		}
		s.page(w, r, items)
	case r.Method == http.MethodGet && len(parts) == 4 && parts[1] == "profiles":
		profile := s.Profiles[parts[2]]
		if profile == nil {
			writeError(w, http.StatusNotFound, "NOT_FOUND", "profile")
			return
		}
		ids := profile.CertificateIDs
		kind := "certificates"
		if parts[3] == "devices" {
			ids, kind = profile.DeviceIDs, "devices"
		}
		var items []map[string]any
		for _, id := range ids {
			items = append(items, map[string]any{"type": kind, "id": id, "attributes": map[string]any{}})
		}
		s.page(w, r, items)
	case r.Method == http.MethodPost && r.URL.Path == "/v1/profiles":
		s.createProfile(w, body)
	case r.Method == http.MethodDelete && len(parts) == 3 && parts[1] == "profiles":
		if s.Profiles[parts[2]] == nil {
			writeError(w, http.StatusNotFound, "NOT_FOUND", "profile")
			return
		}
		delete(s.Profiles, parts[2])
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodGet && r.URL.Path == "/v1/certificates":
		types := strings.Split(r.URL.Query().Get("filter[certificateType]"), ",")
		var items []map[string]any
		for _, certificate := range s.sortedCertificates() {
			if contains(types, certificate.Type) {
				items = append(items, map[string]any{"type": "certificates", "id": certificate.ID, "attributes": map[string]any{
					"certificateType": certificate.Type, "name": certificate.Name, "serialNumber": certificate.Serial,
					"expirationDate": certificate.Expires.Format("2006-01-02T15:04:05.000-0700"),
				}})
			}
		}
		s.page(w, r, items)
	case r.Method == http.MethodGet && r.URL.Path == "/v1/devices":
		var items []map[string]any
		for _, device := range s.sortedDevices() {
			items = append(items, map[string]any{"type": "devices", "id": device.ID, "attributes": map[string]any{"name": device.Name, "udid": device.UDID, "deviceClass": "IPHONE"}})
		}
		s.page(w, r, items)
	default:
		writeError(w, http.StatusNotFound, "NOT_FOUND", r.Method+" "+r.URL.Path)
	}
}

func (s *Server) createProfile(w http.ResponseWriter, body map[string]any) {
	attributes := dig(body, "data", "attributes")
	name, _ := attributes["name"].(string)
	kind, _ := attributes["profileType"].(string)
	bundleID, _ := dig(body, "data", "relationships", "bundleId", "data")["id"].(string)
	if s.Bundles[bundleID] == nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "bundle id")
		return
	}
	for _, profile := range s.Profiles {
		if profile.Name == name {
			writeError(w, http.StatusConflict, "ENTITY_ERROR.ATTRIBUTE.INVALID", "Multiple profiles found with the name '"+name+"'")
			return
		}
	}
	certificates := relationshipIDs(body, "certificates")
	if len(certificates) == 0 {
		writeError(w, http.StatusConflict, "ENTITY_ERROR.RELATIONSHIP.INVALID", "a certificate is required")
		return
	}
	for _, id := range certificates {
		if s.Certificates[id] == nil {
			writeError(w, http.StatusConflict, "ENTITY_ERROR.RELATIONSHIP.INVALID", "unknown certificate "+id)
			return
		}
	}
	devices := relationshipIDs(body, "devices")
	if kind != appstore.ProfileIOSAppStore && len(devices) == 0 {
		writeError(w, http.StatusConflict, "ENTITY_ERROR.RELATIONSHIP.INVALID", "devices are required")
		return
	}
	profile := &Profile{ID: s.id("P"), Name: name, Type: kind, State: appstore.ProfileStateActive, BundleID: bundleID, Expires: s.Now.AddDate(1, 0, 0), CertificateIDs: certificates, DeviceIDs: devices}
	profile.UUID = "uuid-" + profile.ID
	s.Profiles[profile.ID] = profile
	writeJSON(w, http.StatusCreated, map[string]any{"data": profileResource(profile)})
}

// page writes items, paginated with absolute next links like the real API.
func (s *Server) page(w http.ResponseWriter, r *http.Request, items []map[string]any) {
	size := s.PageSize
	if size <= 0 {
		size = 200
	}
	offset, _ := strconv.Atoi(r.URL.Query().Get("cursor"))
	end := min(offset+size, len(items))
	if offset > len(items) {
		offset = len(items)
	}
	response := map[string]any{"data": append([]map[string]any{}, items[offset:end]...)}
	if end < len(items) {
		query := r.URL.Query()
		query.Set("cursor", strconv.Itoa(end))
		response["links"] = map[string]any{"next": s.URL + r.URL.Path + "?" + query.Encode()}
	}
	writeJSON(w, http.StatusOK, response)
}

func bundleResource(bundle *Bundle) map[string]any {
	return map[string]any{"type": "bundleIds", "id": bundle.ID, "attributes": map[string]any{
		"identifier": bundle.Identifier, "name": bundle.Name, "platform": "IOS", "seedId": bundle.SeedID,
	}}
}

func profileResource(profile *Profile) map[string]any {
	return map[string]any{"type": "profiles", "id": profile.ID, "attributes": map[string]any{
		"name": profile.Name, "profileType": profile.Type, "profileState": profile.State, "uuid": profile.UUID,
		"expirationDate": profile.Expires.Format("2006-01-02T15:04:05.000-0700"),
		"profileContent": base64.StdEncoding.EncodeToString(Content(profile.Name, profile.UUID)),
	}}
}

func (s *Server) sortedBundles() []*Bundle {
	items := make([]*Bundle, 0, len(s.Bundles))
	for _, item := range s.Bundles {
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	return items
}

func (s *Server) sortedProfiles() []*Profile {
	items := make([]*Profile, 0, len(s.Profiles))
	for _, item := range s.Profiles {
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	return items
}

func (s *Server) sortedCertificates() []*Certificate {
	items := make([]*Certificate, 0, len(s.Certificates))
	for _, item := range s.Certificates {
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	return items
}

func (s *Server) sortedDevices() []*Device {
	items := make([]*Device, 0, len(s.Devices))
	for _, item := range s.Devices {
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	return items
}

func dig(value map[string]any, keys ...string) map[string]any {
	current := value
	for _, key := range keys {
		next, _ := current[key].(map[string]any)
		if next == nil {
			return map[string]any{}
		}
		current = next
	}
	return current
}

func relationshipIDs(body map[string]any, name string) []string {
	list, _ := dig(body, "data", "relationships", name)["data"].([]any)
	var ids []string
	for _, item := range list {
		if entry, ok := item.(map[string]any); ok {
			if id, ok := entry["id"].(string); ok {
				ids = append(ids, id)
			}
		}
	}
	return ids
}

func contains(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, code, detail string) {
	writeJSON(w, status, map[string]any{"errors": []map[string]any{{"status": strconv.Itoa(status), "code": code, "title": http.StatusText(status), "detail": detail}}})
}
