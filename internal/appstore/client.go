// Package appstore is a small App Store Connect API client for the
// provisioning resources (bundle ids, capabilities, profiles, certificates
// and devices). It signs requests with an ES256 JWT made from the API key
// (.p8), retries rate-limited requests and never logs the key or token.
package appstore

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// DefaultBaseURL is the App Store Connect API.
const DefaultBaseURL = "https://api.appstoreconnect.apple.com"

// Key is an App Store Connect API key.
type Key struct {
	KeyID    string
	IssuerID string
	// PrivateKey is the .p8 file (PKCS#8 PEM).
	PrivateKey []byte
}

// Client calls the App Store Connect API.
type Client struct {
	BaseURL string
	HTTP    *http.Client
	// MaxAttempts bounds retries of rate-limited (429) and failed (5xx)
	// requests. Default 5.
	MaxAttempts int
	// Now and Sleep are replaced in tests.
	Now   func() time.Time
	Sleep func(context.Context, time.Duration) error

	keyID    string
	issuerID string
	private  *ecdsa.PrivateKey

	mu       sync.Mutex
	token    string
	tokenExp time.Time
}

// New parses key and returns a client for the public API.
func New(key Key) (*Client, error) {
	if strings.TrimSpace(key.KeyID) == "" || strings.TrimSpace(key.IssuerID) == "" {
		return nil, errors.New("App Store Connect API key id and issuer id are required")
	}
	private, err := parsePrivateKey(key.PrivateKey)
	if err != nil {
		return nil, err
	}
	return &Client{
		BaseURL:  DefaultBaseURL,
		HTTP:     &http.Client{Timeout: 60 * time.Second},
		keyID:    strings.TrimSpace(key.KeyID),
		issuerID: strings.TrimSpace(key.IssuerID),
		private:  private,
	}, nil
}

func parsePrivateKey(data []byte) (*ecdsa.PrivateKey, error) {
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, errors.New("App Store Connect private key: not a PEM (.p8) file")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("App Store Connect private key: %w", err)
	}
	private, ok := parsed.(*ecdsa.PrivateKey)
	if !ok || private.Curve.Params().BitSize != 256 {
		return nil, errors.New("App Store Connect private key: expected a P-256 EC key")
	}
	return private, nil
}

func (c *Client) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *Client) sleep(ctx context.Context, d time.Duration) error {
	if c.Sleep != nil {
		return c.Sleep(ctx, d)
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// bearer returns a cached token, renewed a minute before it expires. Apple
// accepts tokens valid for at most 20 minutes.
func (c *Client) bearer() (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	if c.token != "" && now.Add(time.Minute).Before(c.tokenExp) {
		return c.token, nil
	}
	expires := now.Add(19 * time.Minute)
	token, err := signJWT(c.private, c.keyID, c.issuerID, now, expires)
	if err != nil {
		return "", err
	}
	c.token, c.tokenExp = token, expires
	return token, nil
}

func signJWT(private *ecdsa.PrivateKey, keyID, issuerID string, issued, expires time.Time) (string, error) {
	header, _ := json.Marshal(map[string]string{"alg": "ES256", "kid": keyID, "typ": "JWT"})
	claims, _ := json.Marshal(map[string]any{
		"iss": issuerID,
		"iat": issued.Unix(),
		"exp": expires.Unix(),
		"aud": "appstoreconnect-v1",
	})
	encode := base64.RawURLEncoding.EncodeToString
	signing := encode(header) + "." + encode(claims)
	digest := sha256.Sum256([]byte(signing))
	r, s, err := ecdsa.Sign(rand.Reader, private, digest[:])
	if err != nil {
		return "", err
	}
	// JWS ES256 signatures are R || S, each left-padded to 32 bytes.
	signature := make([]byte, 64)
	r.FillBytes(signature[:32])
	s.FillBytes(signature[32:])
	return signing + "." + encode(signature), nil
}

// VerifyJWT checks an ES256 token against public; used by tests and the
// fake server.
func VerifyJWT(token string, public *ecdsa.PublicKey) (map[string]any, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, errors.New("malformed token")
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || len(signature) != 64 {
		return nil, errors.New("malformed signature")
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	r, s := new(big.Int).SetBytes(signature[:32]), new(big.Int).SetBytes(signature[32:])
	if !ecdsa.Verify(public, digest[:], r, s) {
		return nil, errors.New("bad signature")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, err
	}
	claims := map[string]any{}
	return claims, json.Unmarshal(payload, &claims)
}

// APIError is an error response of the API.
type APIError struct {
	Status int
	Method string
	Path   string
	Code   string
	Title  string
	Detail string
}

func (e *APIError) Error() string {
	message := strings.TrimSpace(e.Title + ": " + e.Detail)
	if message == ":" {
		message = http.StatusText(e.Status)
	}
	return fmt.Sprintf("App Store Connect %s %s: %d %s", e.Method, e.Path, e.Status, message)
}

// IsConflict reports a 409 (e.g. a profile name already in use).
func IsConflict(err error) bool {
	var apiError *APIError
	return errors.As(err, &apiError) && apiError.Status == http.StatusConflict
}

// IsNotFound reports a 404.
func IsNotFound(err error) bool {
	var apiError *APIError
	return errors.As(err, &apiError) && apiError.Status == http.StatusNotFound
}

// do sends a request and decodes the JSON response into out (if not nil).
// path is relative to BaseURL or, for pagination, an absolute URL on the
// same host.
func (c *Client) do(ctx context.Context, method, path string, body any, out any) error {
	target, err := c.resolve(path)
	if err != nil {
		return err
	}
	var payload []byte
	if body != nil {
		if payload, err = json.Marshal(body); err != nil {
			return err
		}
	}
	attempts := c.MaxAttempts
	if attempts <= 0 {
		attempts = 5
	}
	for attempt := 1; ; attempt++ {
		token, err := c.bearer()
		if err != nil {
			return err
		}
		request, err := http.NewRequestWithContext(ctx, method, target.String(), bytes.NewReader(payload))
		if err != nil {
			return err
		}
		request.Header.Set("Authorization", "Bearer "+token)
		request.Header.Set("Accept", "application/json")
		if body != nil {
			request.Header.Set("Content-Type", "application/json")
		}
		response, err := c.HTTP.Do(request)
		if err != nil {
			// Writes are not retried on transport errors: they may have
			// been applied.
			if method == http.MethodGet && attempt < attempts && ctx.Err() == nil {
				if sleepErr := c.sleep(ctx, backoff(attempt, "")); sleepErr != nil {
					return sleepErr
				}
				continue
			}
			return fmt.Errorf("App Store Connect %s %s: %w", method, target.Path, err)
		}
		data, readErr := io.ReadAll(io.LimitReader(response.Body, 32<<20))
		response.Body.Close()
		if readErr != nil {
			return readErr
		}
		retryable := response.StatusCode == http.StatusTooManyRequests ||
			(response.StatusCode >= 500 && (method == http.MethodGet || method == http.MethodDelete))
		if retryable && attempt < attempts {
			if err := c.sleep(ctx, backoff(attempt, response.Header.Get("Retry-After"))); err != nil {
				return err
			}
			continue
		}
		if response.StatusCode >= 300 {
			return decodeError(response.StatusCode, method, target.Path, data)
		}
		if out == nil || len(data) == 0 {
			return nil
		}
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("App Store Connect %s %s: decode: %w", method, target.Path, err)
		}
		return nil
	}
}

// resolve keeps every request on the API host, so pagination links can
// never send the token elsewhere.
func (c *Client) resolve(path string) (*url.URL, error) {
	base, err := url.Parse(strings.TrimRight(c.BaseURL, "/"))
	if err != nil {
		return nil, err
	}
	if !strings.HasPrefix(path, "http://") && !strings.HasPrefix(path, "https://") {
		return url.Parse(base.String() + path)
	}
	target, err := url.Parse(path)
	if err != nil {
		return nil, err
	}
	if target.Scheme != base.Scheme || target.Host != base.Host {
		return nil, fmt.Errorf("refusing to follow a link to %s", target.Host)
	}
	return target, nil
}

func backoff(attempt int, retryAfter string) time.Duration {
	if seconds, err := strconv.Atoi(strings.TrimSpace(retryAfter)); err == nil && seconds >= 0 {
		return min(time.Duration(seconds)*time.Second, 2*time.Minute)
	}
	return min(time.Duration(1<<attempt)*time.Second, time.Minute)
}

func decodeError(status int, method, path string, data []byte) error {
	var envelope struct {
		Errors []struct {
			Code   string `json:"code"`
			Title  string `json:"title"`
			Detail string `json:"detail"`
		} `json:"errors"`
	}
	apiError := &APIError{Status: status, Method: method, Path: path}
	if json.Unmarshal(data, &envelope) == nil && len(envelope.Errors) > 0 {
		apiError.Code = envelope.Errors[0].Code
		apiError.Title = envelope.Errors[0].Title
		apiError.Detail = envelope.Errors[0].Detail
	}
	return apiError
}
