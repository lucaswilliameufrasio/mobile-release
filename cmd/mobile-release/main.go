package main

import (
	"context"
	"flag"
	"fmt"
	"github.com/lucaswilliameufrasio/mobile-release/internal/config"
	"github.com/lucaswilliameufrasio/mobile-release/internal/project"
	"github.com/lucaswilliameufrasio/mobile-release/internal/release"
	"github.com/lucaswilliameufrasio/mobile-release/internal/runner"
	"github.com/lucaswilliameufrasio/mobile-release/internal/secrets"
	"os"
	"time"
)

func main() {
	cfgPath := flag.String("config", "release.toml", "config path")
	platform := flag.String("platform", "all", "android, ios, or all")
	flag.Parse()
	if flag.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "usage: mobile-release [flags] <doctor|qa|internal>")
		os.Exit(2)
	}
	r := runner.Exec{Dir: ".", Out: os.Stdout, Err: os.Stderr}
	fatal := func(name string, fn func() error) {
		if e := fn(); e != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", name, e)
			os.Exit(1)
		}
	}
	c := config.Defaults()
	fatal("load config", func() error {
		var e error
		c, e = config.Load(*cfgPath)
		return e
	})
	c = config.Resolve(c)
	if c.ProjectType == "" {
		c.ProjectType = project.Detect(".")
	}
	fatal("validate config", func() error { return config.Validate(c) })
	tmp, e := os.MkdirTemp("", "mobile-release-secrets-")
	if e != nil {
		fmt.Fprintf(os.Stderr, "temp dir: %v\n", e)
		os.Exit(1)
	}
	defer os.RemoveAll(tmp)
	fatal("materialize secrets", func() error { return secrets.ApplyAll(tmp) })
	if flag.Arg(0) == "doctor" {
		for _, x := range []string{"git", "bundle"} {
			if e = r.LookPath(x); e != nil {
				fmt.Printf("missing: %s\n", x)
			} else {
				fmt.Printf("ok: %s\n", x)
			}
		}
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
	defer cancel()
	s := release.Service{R: r, C: c}
	switch flag.Arg(0) {
	case "qa":
		e = s.QA(ctx)
	case "internal":
		e = s.Internal(ctx, *platform)
	default:
		e = fmt.Errorf("unknown command")
	}
	if e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}
