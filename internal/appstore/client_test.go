package appstore_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/lucaswilliameufrasio/mobile-release/v2/internal/appstore"
	"github.com/lucaswilliameufrasio/mobile-release/v2/internal/appstore/appstoretest"
)

func TestTokenIsES256WithAppStoreClaims(t *testing.T) {
	private, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, _ := x509.MarshalPKCS8PrivateKey(private)
	var header string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header = r.Header.Get("Authorization")
		w.Write([]byte(`{"data":[]}`))
	}))
	defer server.Close()
	client, err := appstore.New(appstore.Key{KeyID: "KEY", IssuerID: "ISSUER", PrivateKey: pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_800_000_000, 0)
	client.BaseURL, client.Now = server.URL, func() time.Time { return now }
	if _, err := client.Devices(context.Background()); err != nil {
		t.Fatal(err)
	}
	token, ok := strings.CutPrefix(header, "Bearer ")
	if !ok {
		t.Fatalf("authorization = %q", header)
	}
	claims, err := appstore.VerifyJWT(token, &private.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	if claims["iss"] != "ISSUER" || claims["aud"] != "appstoreconnect-v1" || claims["iat"].(float64) != float64(now.Unix()) {
		t.Fatalf("claims = %v", claims)
	}
	// Apple rejects tokens valid for more than 20 minutes.
	if lifetime := claims["exp"].(float64) - claims["iat"].(float64); lifetime > 20*60 || lifetime <= 0 {
		t.Fatalf("lifetime = %v s", lifetime)
	}
}

func TestRejectsInvalidKeys(t *testing.T) {
	if _, err := appstore.New(appstore.Key{KeyID: "K", IssuerID: "I", PrivateKey: []byte("not a key")}); err == nil {
		t.Fatal("expected an error for a non-PEM key")
	}
	if _, err := appstore.New(appstore.Key{IssuerID: "I"}); err == nil {
		t.Fatal("expected an error without key id")
	}
}

func TestPaginationAndExactIdentifier(t *testing.T) {
	server := appstoretest.New(t)
	server.PageSize = 1
	server.AddBundle("com.example.app.widgets")
	server.AddBundle("com.example.app")
	for range 3 {
		server.AddDevice("iPhone")
	}
	client := server.Client(t)
	bundle, err := client.FindBundleID(context.Background(), "com.example.app")
	if err != nil || bundle == nil || bundle.Identifier != "com.example.app" || bundle.SeedID != "ABCDE12345" {
		t.Fatalf("bundle = %+v, %v", bundle, err)
	}
	devices, err := client.Devices(context.Background())
	if err != nil || len(devices) != 3 {
		t.Fatalf("devices = %d, %v", len(devices), err)
	}
	missing, err := client.FindBundleID(context.Background(), "com.example")
	if err != nil || missing != nil {
		t.Fatalf("a prefix must not match: %+v, %v", missing, err)
	}
}

func TestRetriesRateLimitedRequests(t *testing.T) {
	server := appstoretest.New(t)
	server.Fail["GET /v1/devices"] = appstoretest.Failure{Status: http.StatusTooManyRequests, Times: 2, RetryAfter: "7"}
	client := server.Client(t)
	var waits []time.Duration
	client.Sleep = func(_ context.Context, d time.Duration) error {
		waits = append(waits, d)
		return nil
	}
	if _, err := client.Devices(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(waits) != 2 || waits[0] != 7*time.Second {
		t.Fatalf("waits = %v", waits)
	}

	server.Fail["GET /v1/devices"] = appstoretest.Failure{Status: http.StatusTooManyRequests}
	client.MaxAttempts = 3
	_, err := client.Devices(context.Background())
	if err == nil || !strings.Contains(err.Error(), "429") {
		t.Fatalf("expected the 429 after the last attempt, got %v", err)
	}
}

func TestWritesAreNotRetriedOnServerErrors(t *testing.T) {
	server := appstoretest.New(t)
	bundle := server.AddBundle("com.example.app")
	server.Fail["POST /v1/bundleIdCapabilities"] = appstoretest.Failure{Status: http.StatusInternalServerError, Times: 1}
	client := server.Client(t)
	_, err := client.EnableCapability(context.Background(), bundle.ID, "PUSH_NOTIFICATIONS", nil)
	if err == nil {
		t.Fatal("expected the 500")
	}
	if writes := server.Writes(); len(writes) != 1 {
		t.Fatalf("a failed POST may have been applied and must not be retried: %v", writes)
	}
}

func TestErrorsAndIdempotentDelete(t *testing.T) {
	server := appstoretest.New(t)
	client := server.Client(t)
	if err := client.DeleteProfile(context.Background(), "gone"); err != nil {
		t.Fatalf("deleting a missing profile must succeed: %v", err)
	}
	bundle := server.AddBundle("com.example.app")
	certificate := server.AddCertificate("DISTRIBUTION", 300)
	server.AddProfile(bundle, "Taken", appstore.ProfileIOSAppStore, appstore.ProfileStateActive, 100, []string{certificate.ID}, nil)
	_, err := client.CreateProfile(context.Background(), appstore.CreateProfileRequest{Name: "Taken", Type: appstore.ProfileIOSAppStore, BundleID: bundle.ID, CertificateIDs: []string{certificate.ID}})
	if !appstore.IsConflict(err) || !strings.Contains(err.Error(), "Multiple profiles found") {
		t.Fatalf("expected a conflict, got %v", err)
	}
}

func TestPaginationStaysOnTheAPIHost(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":[],"links":{"next":"https://attacker.example/v1/devices"}}`))
	}))
	defer server.Close()
	fake := appstoretest.New(t)
	client := fake.Client(t)
	client.BaseURL = server.URL
	if _, err := client.Devices(context.Background()); err == nil || !strings.Contains(err.Error(), "refusing to follow") {
		t.Fatalf("expected refusal, got %v", err)
	}
}
