package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPreviewHandlerServesOnlySelectedAPK(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.apk")
	if err := os.WriteFile(path, []byte("test apk"), 0600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(previewHandler(file, info, "/secret/app.apk", "tixnow-bar-20261116-120000-utc.apk"))
	defer server.Close()
	for _, tc := range []struct {
		method string
		path   string
		status int
		body   string
	}{
		{http.MethodGet, "/secret/app.apk", http.StatusOK, "test apk"},
		{http.MethodHead, "/secret/app.apk", http.StatusOK, ""},
		{http.MethodGet, "/wrong/app.apk", http.StatusNotFound, ""},
		{http.MethodPost, "/secret/app.apk", http.StatusNotFound, ""},
	} {
		req, err := http.NewRequest(tc.method, server.URL+tc.path, nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != tc.status || (tc.status == http.StatusOK && string(body) != tc.body) {
			t.Fatalf("%s %s: status %d, body %q", tc.method, tc.path, resp.StatusCode, body)
		}
		if tc.status == http.StatusOK && resp.Header.Get("Content-Disposition") != `attachment; filename="tixnow-bar-20261116-120000-utc.apk"` {
			t.Fatalf("unexpected download name: %q", resp.Header.Get("Content-Disposition"))
		}
	}
}

func TestPreviewFilename(t *testing.T) {
	built := time.Date(2026, 11, 16, 9, 30, 45, 0, time.FixedZone("test", -3*60*60))
	for _, tc := range []struct {
		name string
		want string
	}{
		{"TixNow Bar", "tixnow-bar-20261116-123045-utc.apk"},
		{"Café & Bar", "cafe-bar-20261116-123045-utc.apk"},
		{"../\"evil\r\nX: yes", "evil-x-yes-20261116-123045-utc.apk"},
		{"🍹", "app-20261116-123045-utc.apk"},
	} {
		if got := previewFilename(tc.name, built); got != tc.want {
			t.Errorf("previewFilename(%q) = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestPreviewRejectsInvalidOptionsAndStopsOnCancellation(t *testing.T) {
	for _, args := range [][]string{{}, {"-host", "0.0.0.0"}, {"-host", "127.0.0.1", "-duration", "2h"}} {
		if err := runPreview(context.Background(), args); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
	path := filepath.Join(t.TempDir(), "test.apk")
	if err := os.WriteFile(path, []byte("apk"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := runPreview(ctx, []string{"-host", "127.0.0.1", "-apk", path, "-duration", time.Second.String()}); err != nil && !strings.Contains(err.Error(), "closed network connection") {
		t.Fatal(err)
	}
}
