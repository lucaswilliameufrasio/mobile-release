package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/lucaswilliameufrasio/mobile-release/v2/internal/credentials"
	"github.com/skip2/go-qrcode"
)

func runPreview(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("preview", flag.ContinueOnError)
	var common commonFlags
	common.register(flags)
	apk := flags.String("apk", "", "existing APK; omit to build Android QA")
	envName := flags.String("env", "", "configured QA environment")
	mode := flags.String("mode", "", "Flutter build mode: debug, profile or release")
	host := flags.String("host", "", "explicit local IP reachable from the phone")
	port := flags.Int("port", 0, "TCP port (0 chooses an available port)")
	duration := flags.Duration("duration", 15*time.Minute, "session lifetime (maximum 1 hour)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *duration <= 0 || *duration > time.Hour || *port < 0 || *port > 65535 {
		return fmt.Errorf("invalid arguments, port or session duration")
	}
	ip := net.ParseIP(*host)
	if ip == nil || ip.IsUnspecified() || ip.IsMulticast() {
		return fmt.Errorf("-host must be an explicit local IP address")
	}
	path := *apk
	if path != "" && *mode != "" {
		return fmt.Errorf("-mode cannot be used with an existing APK")
	}
	if path == "" {
		a, err := newApp(common)
		if err != nil {
			return err
		}
		if err := a.loadConfig(); err != nil {
			return err
		}
		if !a.config.AndroidEnabled() {
			return fmt.Errorf("Android is not enabled")
		}
		service, cleanup, err := a.service(credentials.NewSet(), false)
		if err != nil {
			return err
		}
		defer cleanup()
		result, err := service.QAWithAndroidMode(ctx, *envName, true, false, *mode)
		if err != nil {
			return err
		}
		if len(result.Artifacts) != 1 {
			return fmt.Errorf("expected one Android APK from QA")
		}
		path = filepath.Join(a.root, result.Artifacts[0].Path)
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || !strings.EqualFold(filepath.Ext(info.Name()), ".apk") {
		return fmt.Errorf("selected artifact must be a regular APK file")
	}
	listener, err := net.Listen("tcp", net.JoinHostPort(ip.String(), strconv.Itoa(*port)))
	if err != nil {
		return err
	}
	defer listener.Close()
	token := make([]byte, 32)
	if _, err := rand.Read(token); err != nil {
		return err
	}
	route := "/" + hex.EncodeToString(token) + "/app.apk"
	address := listener.Addr().(*net.TCPAddr)
	link := (&url.URL{Scheme: "http", Host: net.JoinHostPort(ip.String(), strconv.Itoa(address.Port)), Path: route}).String()
	qr, err := qrcode.New(link, qrcode.Medium)
	if err != nil {
		return err
	}
	handler := previewHandler(file, info, route)
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	fmt.Printf("Only use a trusted network; anyone with this link can download the APK.\n%s\n%s\nExpires in %s. Press Ctrl+C to stop.\n", qr.ToSmallString(false), link, *duration)
	session, cancel := context.WithTimeout(ctx, *duration)
	defer cancel()
	go func() {
		<-session.Done()
		_ = server.Close()
	}()
	err = server.Serve(listener)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func previewHandler(file *os.File, info os.FileInfo, route string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != route || (r.Method != http.MethodGet && r.Method != http.MethodHead) {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/vnd.android.package-archive")
		w.Header().Set("Content-Disposition", `attachment; filename="app.apk"`)
		w.Header().Set("Cache-Control", "no-store")
		http.ServeContent(w, r, "app.apk", info.ModTime(), io.NewSectionReader(file, 0, info.Size()))
	})
}
