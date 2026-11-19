package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"proxy-dev/vpn-manager/internal/httpserver"
	"proxy-dev/vpn-manager/internal/importurl"
	"proxy-dev/vpn-manager/internal/liveauth"
	"proxy-dev/vpn-manager/internal/profile"
	"proxy-dev/vpn-manager/internal/ruleset"
	"proxy-dev/vpn-manager/internal/server"
	"proxy-dev/vpn-manager/internal/subscription"
	"proxy-dev/vpn-manager/internal/telegrambot"
	"proxy-dev/vpn-manager/internal/users"
)

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "live-auth":
			runLiveAuth(os.Args[2:])
			return
		case "sync-users":
			syncUsers(os.Args[2:])
			return
		case "links":
			printLinks(os.Args[2:])
			return
		case "telegram-bot":
			runTelegramBot(os.Args[2:])
			return
		}
	}
	serve(os.Args[1:])
}

func runTelegramBot(args []string) {
	fs := flag.NewFlagSet("telegram-bot", flag.ExitOnError)
	var configFile string
	fs.StringVar(&configFile, "config", "/etc/vpn-manager/telegram.json", "telegram bot config path")
	_ = fs.Parse(args)
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	cfg, err := telegrambot.LoadConfig(configFile)
	if err != nil {
		log.Error("invalid telegram config", "error", err)
		os.Exit(1)
	}
	bot, err := telegrambot.New(cfg, log)
	if err != nil {
		log.Error("telegram bot init failed", "error", err)
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := bot.Run(ctx); err != nil {
		log.Error("telegram bot stopped with error", "error", err)
		os.Exit(1)
	}
}

func syncUsers(args []string) {
	fs := flag.NewFlagSet("sync-users", flag.ExitOnError)
	var stateFile, usersFile string
	fs.StringVar(&stateFile, "state", "/etc/proxy-manager/state.json", "proxy-manager state path")
	fs.StringVar(&usersFile, "users-config", "/etc/vpn-manager/users.yaml", "users config path")
	_ = fs.Parse(args)

	if err := users.SyncFromProxyState(usersFile, stateFile); err != nil {
		fmt.Fprintf(os.Stderr, "sync users: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("subscription users synchronized: %s\n", usersFile)
}

func printLinks(args []string) {
	fs := flag.NewFlagSet("links", flag.ExitOnError)
	var usersFile, baseURL, profileName, routing string
	fs.StringVar(&usersFile, "users-config", "/etc/vpn-manager/users.yaml", "users config path")
	fs.StringVar(&baseURL, "base-url", "", "public HTTPS base URL")
	fs.StringVar(&profileName, "profile-name", "Proxy VPN", "remote profile display name")
	fs.StringVar(&routing, "routing", "", "routing mode: bypass-ru, blocked or full (empty preserves legacy URLs)")
	_ = fs.Parse(args)
	if fs.NArg() != 1 || baseURL == "" {
		fmt.Fprintln(os.Stderr, "usage: vpn-manager links --base-url https://vpn.example.com [--users-config path] USER")
		os.Exit(2)
	}
	if routing != "" && routing != "bypass-ru" && routing != "blocked" && routing != "full" {
		fmt.Fprintln(os.Stderr, "routing must be bypass-ru, blocked or full")
		os.Exit(2)
	}
	name := fs.Arg(0)
	u, err := users.FindByNameFile(usersFile, name)
	if err != nil {
		fmt.Fprintf(os.Stderr, "links: %v\n", err)
		os.Exit(1)
	}
	baseURL = strings.TrimRight(baseURL, "/")
	variants := []struct{ label, suffix, title string }{
		{"Universal", "", profileName},
		{"Android", "/android", profileName + " Android"},
		{"iPhone / iPad", "/ios", profileName + " iOS"},
		{"macOS", "/macos", profileName + " macOS"},
		{"Linux", "/linux", profileName + " Linux"},
		{"Android TV / IPTV", "/tv", profileName + " TV"},
		{"OpenWRT", "/openwrt", profileName + " OpenWRT"},
	}
	fmt.Printf("Subscription links for %s:\n", name)
	if !u.Enabled {
		fmt.Println("Status: disabled")
	}
	for _, v := range variants {
		if routing != "" {
			if v.suffix == "" {
				v.suffix = "/universal"
			}
			v.suffix += "/" + routing
			v.title += " " + routing
		}
		sub := fmt.Sprintf("%s/sub/%s%s", baseURL, u.Token, v.suffix)
		fmt.Printf("\n%s:\n%s\nImport:\n%s\n", v.label, sub, importurl.Build(sub, v.title))
	}
}

func serve(args []string) {
	fs := flag.NewFlagSet("vpn-manager", flag.ExitOnError)
	var serverFile, usersFile, listen string
	rulesDir := fs.String("rules-dir", "/var/lib/vpn-manager/rules", "persistent rule snapshot directory")
	rulesConfig := fs.String("rules-config", "/etc/vpn-manager/rules.json", "upstream rule sources JSON")
	rulesInput := fs.String("rules-input-dir", "/etc/vpn-manager/rules.d", "directory containing local SRS input files")
	singBox := fs.String("sing-box", "sing-box", "Sing-box validator executable")
	fs.StringVar(&serverFile, "server-config", "/etc/vpn-manager/server.yaml", "server config path")
	fs.StringVar(&usersFile, "users-config", "/etc/vpn-manager/users.yaml", "users config path")
	fs.StringVar(&listen, "listen", "127.0.0.1:8080", "listen address")
	_ = fs.Parse(args)

	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	sp, err := server.NewFileProvider(serverFile)
	if err != nil {
		log.Error("invalid server config", "error", err)
		os.Exit(1)
	}
	us, err := users.NewFileStore(usersFile)
	if err != nil {
		log.Error("invalid users config", "error", err)
		os.Exit(1)
	}
	var rc ruleset.Config
	if data, err := os.ReadFile(*rulesConfig); err == nil {
		if err = json.Unmarshal(data, &rc); err != nil {
			log.Error("invalid rule source configuration", "error", err)
			os.Exit(1)
		}
	} else if !os.IsNotExist(err) {
		log.Error("read rule sources", "error", err)
		os.Exit(1)
	}
	rc.LocalDir = *rulesInput
	rm, err := ruleset.New(*rulesDir, *singBox, rc)
	if err != nil {
		log.Error("load rule snapshot", "error", err)
		os.Exit(1)
	}
	generator := profile.New()
	generator.Rules = rm
	svc := subscription.New(us, sp, generator)
	hs := httpserver.New(listen, svc, log, rm)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go rm.Run(ctx, log)
	go func() {
		<-ctx.Done()
		c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := hs.HTTP().Shutdown(c); err != nil {
			log.Error("graceful shutdown failed", "error", err)
		}
	}()
	log.Info("vpn-manager started", "listen", listen)
	if err := hs.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Error("http server failed", "error", err)
		os.Exit(1)
	}
	log.Info("vpn-manager stopped")
}

// Authentication has its own loopback listener and systemd unit so a subscription
// or Telegram upgrade cannot interrupt new Hysteria connections.
func runLiveAuth(args []string) {
	fs := flag.NewFlagSet("live-auth", flag.ExitOnError)
	path := fs.String("users", "/etc/hysteria/auth-users.json", "credential snapshot")
	_ = fs.Parse(args)
	srv := &http.Server{Addr: "127.0.0.1:18081", Handler: liveauth.Handler(*path), ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(c)
	}()
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
