package appstore

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// BundleID is an App ID.
type BundleID struct {
	ID         string
	Identifier string
	Name       string
	Platform   string
	// SeedID is the App ID prefix, normally the team id.
	SeedID string
}

// Capability is a capability enabled on a bundle id.
type Capability struct {
	ID   string
	Type string
}

// Setting configures a capability (e.g. Sign in with Apple consent).
type Setting struct {
	Key     string   `json:"key"`
	Options []Option `json:"options,omitempty"`
}

// Option of a capability setting.
type Option struct {
	Key     string `json:"key"`
	Enabled bool   `json:"enabled,omitempty"`
}

// Profile types and states.
const (
	ProfileIOSDevelopment = "IOS_APP_DEVELOPMENT"
	ProfileIOSAdHoc       = "IOS_APP_ADHOC"
	ProfileIOSAppStore    = "IOS_APP_STORE"

	ProfileStateActive  = "ACTIVE"
	ProfileStateInvalid = "INVALID"
)

// Profile is a provisioning profile.
type Profile struct {
	ID      string
	Name    string
	Type    string
	State   string
	UUID    string
	Expires time.Time
	// Content is the .mobileprovision file.
	Content []byte
}

// Certificate is a signing certificate (the private key is never part of
// the API).
type Certificate struct {
	ID      string
	Type    string
	Name    string
	Serial  string
	Expires time.Time
}

// Device is a registered device.
type Device struct {
	ID    string
	Name  string
	UDID  string
	Class string
}

// CreateProfileRequest creates a profile.
type CreateProfileRequest struct {
	Name           string
	Type           string
	BundleID       string // resource id, not the identifier
	CertificateIDs []string
	DeviceIDs      []string
}

type resource struct {
	Type          string          `json:"type"`
	ID            string          `json:"id"`
	Attributes    json.RawMessage `json:"attributes,omitempty"`
	Relationships json.RawMessage `json:"relationships,omitempty"`
}

type listResponse struct {
	Data  []resource `json:"data"`
	Links struct {
		Next string `json:"next"`
	} `json:"links"`
}

type singleResponse struct {
	Data resource `json:"data"`
}

type linkage struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

type toOne struct {
	Data linkage `json:"data"`
}

type toMany struct {
	Data []linkage `json:"data"`
}

// list follows every page of path.
func (c *Client) list(ctx context.Context, path string) ([]resource, error) {
	var all []resource
	for next := path; next != ""; {
		var page listResponse
		if err := c.do(ctx, http.MethodGet, next, nil, &page); err != nil {
			return nil, err
		}
		all = append(all, page.Data...)
		next = page.Links.Next
	}
	return all, nil
}

func query(values map[string]string) string {
	encoded := url.Values{}
	for key, value := range values {
		encoded.Set(key, value)
	}
	return encoded.Encode()
}

// FindBundleID returns the iOS (or universal) App ID with identifier, or nil.
func (c *Client) FindBundleID(ctx context.Context, identifier string) (*BundleID, error) {
	resources, err := c.list(ctx, "/v1/bundleIds?"+query(map[string]string{
		"filter[identifier]": identifier,
		"limit":              "200",
	}))
	if err != nil {
		return nil, err
	}
	for _, item := range resources {
		bundle, err := decodeBundleID(item)
		if err != nil {
			return nil, err
		}
		// The filter also matches longer identifiers; keep the exact one.
		if bundle.Identifier == identifier && (bundle.Platform == "IOS" || bundle.Platform == "UNIVERSAL") {
			return &bundle, nil
		}
	}
	return nil, nil
}

// CreateBundleID registers an iOS App ID.
func (c *Client) CreateBundleID(ctx context.Context, identifier, name string) (BundleID, error) {
	body := map[string]any{"data": map[string]any{
		"type": "bundleIds",
		"attributes": map[string]string{
			"identifier": identifier,
			"name":       name,
			"platform":   "IOS",
		},
	}}
	var response singleResponse
	if err := c.do(ctx, http.MethodPost, "/v1/bundleIds", body, &response); err != nil {
		return BundleID{}, err
	}
	return decodeBundleID(response.Data)
}

func decodeBundleID(item resource) (BundleID, error) {
	var attributes struct {
		Identifier string `json:"identifier"`
		Name       string `json:"name"`
		Platform   string `json:"platform"`
		SeedID     string `json:"seedId"`
	}
	if err := json.Unmarshal(item.Attributes, &attributes); err != nil {
		return BundleID{}, fmt.Errorf("bundle id %s: %w", item.ID, err)
	}
	return BundleID{ID: item.ID, Identifier: attributes.Identifier, Name: attributes.Name, Platform: attributes.Platform, SeedID: attributes.SeedID}, nil
}

// Capabilities lists the capabilities enabled on a bundle id.
func (c *Client) Capabilities(ctx context.Context, bundleID string) ([]Capability, error) {
	resources, err := c.list(ctx, "/v1/bundleIds/"+url.PathEscape(bundleID)+"/bundleIdCapabilities?limit=200")
	if err != nil {
		return nil, err
	}
	capabilities := make([]Capability, 0, len(resources))
	for _, item := range resources {
		var attributes struct {
			CapabilityType string `json:"capabilityType"`
		}
		if err := json.Unmarshal(item.Attributes, &attributes); err != nil {
			return nil, err
		}
		capabilities = append(capabilities, Capability{ID: item.ID, Type: attributes.CapabilityType})
	}
	return capabilities, nil
}

// EnableCapability enables a capability on a bundle id.
func (c *Client) EnableCapability(ctx context.Context, bundleID, capabilityType string, settings []Setting) (Capability, error) {
	attributes := map[string]any{"capabilityType": capabilityType}
	if len(settings) > 0 {
		attributes["settings"] = settings
	}
	body := map[string]any{"data": map[string]any{
		"type":       "bundleIdCapabilities",
		"attributes": attributes,
		"relationships": map[string]any{
			"bundleId": toOne{Data: linkage{Type: "bundleIds", ID: bundleID}},
		},
	}}
	var response singleResponse
	if err := c.do(ctx, http.MethodPost, "/v1/bundleIdCapabilities", body, &response); err != nil {
		return Capability{}, err
	}
	return Capability{ID: response.Data.ID, Type: capabilityType}, nil
}

// Profiles lists the profiles of a bundle id.
func (c *Client) Profiles(ctx context.Context, bundleID string) ([]Profile, error) {
	resources, err := c.list(ctx, "/v1/bundleIds/"+url.PathEscape(bundleID)+"/profiles?limit=200")
	if err != nil {
		return nil, err
	}
	profiles := make([]Profile, 0, len(resources))
	for _, item := range resources {
		profile, err := decodeProfile(item)
		if err != nil {
			return nil, err
		}
		profiles = append(profiles, profile)
	}
	return profiles, nil
}

func decodeProfile(item resource) (Profile, error) {
	var attributes struct {
		Name           string `json:"name"`
		ProfileType    string `json:"profileType"`
		ProfileState   string `json:"profileState"`
		UUID           string `json:"uuid"`
		ExpirationDate string `json:"expirationDate"`
		ProfileContent string `json:"profileContent"`
	}
	if err := json.Unmarshal(item.Attributes, &attributes); err != nil {
		return Profile{}, fmt.Errorf("profile %s: %w", item.ID, err)
	}
	profile := Profile{ID: item.ID, Name: attributes.Name, Type: attributes.ProfileType, State: attributes.ProfileState, UUID: attributes.UUID}
	if attributes.ExpirationDate != "" {
		expires, err := parseTime(attributes.ExpirationDate)
		if err != nil {
			return Profile{}, fmt.Errorf("profile %s: %w", item.ID, err)
		}
		profile.Expires = expires
	}
	if attributes.ProfileContent != "" {
		content, err := base64.StdEncoding.DecodeString(attributes.ProfileContent)
		if err != nil {
			return Profile{}, fmt.Errorf("profile %s: content: %w", item.ID, err)
		}
		profile.Content = content
	}
	return profile, nil
}

func parseTime(value string) (time.Time, error) {
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05.000-0700", "2006-01-02T15:04:05.000Z0700"} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed, nil
		}
	}
	return time.Time{}, fmt.Errorf("unexpected date %q", value)
}

// ProfileCertificateIDs lists the certificates included in a profile.
func (c *Client) ProfileCertificateIDs(ctx context.Context, profileID string) ([]string, error) {
	return c.ids(ctx, "/v1/profiles/"+url.PathEscape(profileID)+"/certificates?limit=200&fields[certificates]=serialNumber")
}

// ProfileDeviceIDs lists the devices included in a profile.
func (c *Client) ProfileDeviceIDs(ctx context.Context, profileID string) ([]string, error) {
	return c.ids(ctx, "/v1/profiles/"+url.PathEscape(profileID)+"/devices?limit=200&fields[devices]=udid")
}

func (c *Client) ids(ctx context.Context, path string) ([]string, error) {
	resources, err := c.list(ctx, path)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(resources))
	for _, item := range resources {
		ids = append(ids, item.ID)
	}
	return ids, nil
}

// Certificates lists certificates of the given types.
func (c *Client) Certificates(ctx context.Context, types []string) ([]Certificate, error) {
	resources, err := c.list(ctx, "/v1/certificates?"+query(map[string]string{
		"filter[certificateType]": strings.Join(types, ","),
		"limit":                   "200",
	}))
	if err != nil {
		return nil, err
	}
	certificates := make([]Certificate, 0, len(resources))
	for _, item := range resources {
		var attributes struct {
			CertificateType string `json:"certificateType"`
			Name            string `json:"name"`
			DisplayName     string `json:"displayName"`
			SerialNumber    string `json:"serialNumber"`
			ExpirationDate  string `json:"expirationDate"`
		}
		if err := json.Unmarshal(item.Attributes, &attributes); err != nil {
			return nil, err
		}
		certificate := Certificate{ID: item.ID, Type: attributes.CertificateType, Name: firstNonEmpty(attributes.DisplayName, attributes.Name), Serial: attributes.SerialNumber}
		if attributes.ExpirationDate != "" {
			expires, err := parseTime(attributes.ExpirationDate)
			if err != nil {
				return nil, err
			}
			certificate.Expires = expires
		}
		certificates = append(certificates, certificate)
	}
	return certificates, nil
}

// Devices lists the enabled iOS devices.
func (c *Client) Devices(ctx context.Context) ([]Device, error) {
	resources, err := c.list(ctx, "/v1/devices?"+query(map[string]string{
		"filter[platform]": "IOS",
		"filter[status]":   "ENABLED",
		"limit":            "200",
	}))
	if err != nil {
		return nil, err
	}
	devices := make([]Device, 0, len(resources))
	for _, item := range resources {
		var attributes struct {
			Name        string `json:"name"`
			UDID        string `json:"udid"`
			DeviceClass string `json:"deviceClass"`
		}
		if err := json.Unmarshal(item.Attributes, &attributes); err != nil {
			return nil, err
		}
		devices = append(devices, Device{ID: item.ID, Name: attributes.Name, UDID: attributes.UDID, Class: attributes.DeviceClass})
	}
	return devices, nil
}

// CreateProfile creates a profile and returns it with its content.
func (c *Client) CreateProfile(ctx context.Context, request CreateProfileRequest) (Profile, error) {
	relationships := map[string]any{
		"bundleId":     toOne{Data: linkage{Type: "bundleIds", ID: request.BundleID}},
		"certificates": toMany{Data: linkages("certificates", request.CertificateIDs)},
	}
	if len(request.DeviceIDs) > 0 {
		relationships["devices"] = toMany{Data: linkages("devices", request.DeviceIDs)}
	}
	body := map[string]any{"data": map[string]any{
		"type":          "profiles",
		"attributes":    map[string]string{"name": request.Name, "profileType": request.Type},
		"relationships": relationships,
	}}
	var response singleResponse
	if err := c.do(ctx, http.MethodPost, "/v1/profiles", body, &response); err != nil {
		return Profile{}, err
	}
	return decodeProfile(response.Data)
}

// DeleteProfile deletes a profile. Deleting a profile that no longer exists
// succeeds, so retries are idempotent.
func (c *Client) DeleteProfile(ctx context.Context, profileID string) error {
	err := c.do(ctx, http.MethodDelete, "/v1/profiles/"+url.PathEscape(profileID), nil, nil)
	if IsNotFound(err) {
		return nil
	}
	return err
}

func linkages(kind string, ids []string) []linkage {
	links := make([]linkage, 0, len(ids))
	for _, id := range ids {
		links = append(links, linkage{Type: kind, ID: id})
	}
	return links
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
