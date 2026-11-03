package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/proxy/proxy-manager/internal/api"
	"github.com/proxy/proxy-manager/internal/audit"
	"github.com/proxy/proxy-manager/internal/auth"
	"github.com/proxy/proxy-manager/internal/config"
	"github.com/proxy/proxy-manager/internal/database"
	"github.com/proxy/proxy-manager/internal/links"
	service "github.com/proxy/proxy-manager/internal/service"
	"github.com/proxy/proxy-manager/internal/users"
	xrayapi "github.com/proxy/proxy-manager/internal/xray"
)

func fatalf(f string, a ...any) { fmt.Fprintf(os.Stderr, f+"\n", a...); os.Exit(1) }
func setup(cfgPath string) (config.Config, *database.Store, *service.UserService, *auth.Manager, xrayapi.GRPC, audit.Logger) {
	c, err := config.Load(cfgPath)
	if err != nil {
		fatalf("config: %v", err)
	}
	st, err := database.Open(c.Database)
	if err != nil {
		fatalf("database: %v", err)
	}
	if err = st.Migrate(context.Background()); err != nil {
		fatalf("migrate: %v", err)
	}
	xr := xrayapi.GRPC{Addr: c.Xray.APIAddress, Tag: c.Xray.InboundTag, Flow: c.Xray.Flow}
	au := audit.Logger{DB: st.DB}
	us := &service.UserService{Store: st, Xray: xr, Audit: au, Flow: c.Xray.Flow}
	am := auth.New(st.DB, c.SessionTTL)
	return c, st, us, am, xr, au
}
func main() {
	cfgPath := "/etc/proxy-manager/config.yaml"
	if v := os.Getenv("PROXY_MANAGER_CONFIG"); v != "" {
		cfgPath = v
	}
	args := os.Args[1:]
	if len(args) > 1 && args[0] == "--config" {
		cfgPath = args[1]
		args = args[2:]
	}
	if len(args) == 0 {
		serve(cfgPath)
		return
	}
	switch args[0] {
	case "serve":
		serve(cfgPath)
	case "init-admin":
		initAdmin(cfgPath, args[1:])
	case "migrate-legacy":
		migrateLegacy(cfgPath, args[1:])
	case "user":
		userCmd(cfgPath, args[1:])
	case "xray":
		protocolCmd(cfgPath, "xray", args[1:])
	case "hysteria":
		protocolCmd(cfgPath, "hysteria", args[1:])
	case "links":
		linksCmd(cfgPath, args[1:])
	case "admin":
		adminCmd(cfgPath, args[1:])
	case "reconcile":
		_, _, us, _, _, _ := setup(cfgPath)
		if err := us.Reconcile(context.Background()); err != nil {
			fatalf("reconcile: %v", err)
		}
	default:
		fatalf("unknown command %q", args[0])
	}
}

func serve(cfgPath string) {
	c, st, us, am, xr, au := setup(cfgPath)
	defer st.DB.Close()
	if !am.AdminConfigured(context.Background()) {
		fatalf("admin is not initialized; run proxy-manager --config %s init-admin", cfgPath)
	}
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	s := &api.Server{Config: c, Store: st, Users: us, Auth: am, Xray: xr, Audit: au, Log: log}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	if err := us.Reconcile(ctx); err != nil {
		log.Warn("initial xray reconciliation failed", "error", err)
	}
	go func() {
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if err := us.Reconcile(ctx); err != nil {
					log.Warn("periodic xray reconciliation failed", "error", err)
				}
			}
		}
	}()
	mainSrv := &http.Server{Addr: c.Listen, Handler: s.MainHandler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second}
	internalSrv := &http.Server{Addr: c.InternalListen, Handler: s.InternalHandler(), ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second}
	errs := make(chan error, 2)
	go func() { log.Info("proxy manager listening", "addr", c.Listen); errs <- mainSrv.ListenAndServe() }()
	if c.InternalListen != c.Listen {
		go func() {
			log.Info("internal auth listening", "addr", c.InternalListen)
			errs <- internalSrv.ListenAndServe()
		}()
	} else {
		log.Warn("internal and admin listeners are identical; do not expose /internal through a reverse proxy")
	}
	select {
	case <-ctx.Done():
	case err := <-errs:
		if err != nil && err != http.ErrServerClosed {
			log.Error("server stopped", "error", err)
		}
	}
	shutdown, _ := context.WithTimeout(context.Background(), 5*time.Second)
	_ = mainSrv.Shutdown(shutdown)
	if c.InternalListen != c.Listen {
		_ = internalSrv.Shutdown(shutdown)
	}
}
func initAdmin(cfg string, args []string) {
	fs := flag.NewFlagSet("init-admin", flag.ExitOnError)
	user := fs.String("username", "admin", "admin username")
	pass := fs.String("password", "", "admin password")
	passFile := fs.String("password-file", "", "read admin password from file")
	_ = fs.Parse(args)
	if *pass == "" && *passFile != "" {
		b, err := os.ReadFile(*passFile)
		if err != nil {
			fatalf("password file: %v", err)
		}
		*pass = strings.TrimSpace(string(b))
	}
	if *pass == "" {
		fatalf("--password or --password-file is required")
	}
	if len(*pass) < 14 {
		fatalf("password must be at least 14 characters")
	}
	_, st, _, am, _, _ := setup(cfg)
	defer st.DB.Close()
	if err := am.SetAdmin(context.Background(), *user, *pass); err != nil {
		fatalf("init admin: %v", err)
	}
	fmt.Println("admin credentials initialized")
}
func migrateLegacy(cfg string, args []string) {
	fs := flag.NewFlagSet("migrate-legacy", flag.ExitOnError)
	xp := fs.String("xray-config", "/usr/local/etc/xray/config.json", "")
	hp := fs.String("hysteria-config", "/etc/hysteria/config.yaml", "")
	_ = fs.Parse(args)
	c, st, us, _, _, _ := setup(cfg)
	defer st.DB.Close()
	if err := service.Backup(*xp, *hp, c.Database); err != nil {
		fatalf("backup: %v", err)
	}
	r, err := service.DetectLegacy(*xp, *hp, c.Xray.InboundTag)
	if err != nil {
		fatalf("detect: %v", err)
	}
	if err = service.ImportLegacy(context.Background(), st, r); err != nil {
		fatalf("import: %v", err)
	}
	_ = us
	fmt.Println("legacy account imported; remove the migrated static Xray client from config, restart Xray, then run reconcile")
}

func userCmd(cfg string, args []string) {
	if len(args) == 0 {
		fatalf("user subcommand required")
	}
	_, st, us, _, _, _ := setup(cfg)
	defer st.DB.Close()
	ctx := context.Background()
	jsonOut := false
	for _, a := range args {
		if a == "--json" {
			jsonOut = true
		}
	}
	switch args[0] {
	case "add":
		if len(args) < 2 {
			fatalf("name required")
		}
		afs := flag.NewFlagSet("user add", flag.ExitOnError)
		xEnabled := afs.Bool("xray", true, "enable Xray profile")
		hEnabled := afs.Bool("hysteria", true, "enable Hysteria profile")
		_ = afs.Parse(args[2:])
		u, err := us.Create(ctx, "cli", users.CreateRequest{Name: args[1], XrayEnabled: *xEnabled, HysteriaEnabled: *hEnabled})
		if err != nil {
			fatalf("add: %v", err)
		}
		printUser(u, jsonOut)
	case "list":
		xs, err := us.List(ctx)
		if err != nil {
			fatalf("list: %v", err)
		}
		if jsonOut {
			_ = json.NewEncoder(os.Stdout).Encode(xs)
		} else {
			for _, u := range xs {
				fmt.Printf("%-24s account=%-8s xray=%-8s hysteria=%-8s\n", u.Name, on(u.Enabled), on(u.Xray.Enabled), on(u.Hysteria.Enabled))
			}
		}
	case "show":
		need(args, 2)
		u, err := us.Get(ctx, args[1])
		if err != nil {
			fatalf("show: %v", err)
		}
		printUser(u, jsonOut)
	case "enable":
		need(args, 2)
		must(us.Enable(ctx, "cli", args[1]))
	case "disable":
		need(args, 2)
		must(us.Disable(ctx, "cli", args[1]))
	case "delete":
		need(args, 2)
		must(us.Delete(ctx, "cli", args[1]))
	default:
		fatalf("unknown user subcommand")
	}
}
func protocolCmd(cfg, proto string, args []string) {
	need(args, 2)
	_, st, us, _, _, _ := setup(cfg)
	defer st.DB.Close()
	if args[0] == "regenerate" {
		var err error
		if proto == "xray" {
			_, err = us.RegenerateXray(context.Background(), "cli", args[1])
		} else {
			_, err = us.RegenerateHysteria(context.Background(), "cli", args[1])
		}
		must(err)
		return
	}
	enable := args[0] == "enable"
	if args[0] != "enable" && args[0] != "disable" {
		fatalf("expected enable, disable or regenerate")
	}
	var err error
	if proto == "xray" {
		err = us.SetXray(context.Background(), "cli", args[1], enable)
	} else {
		err = us.SetHysteria(context.Background(), "cli", args[1], enable)
	}
	must(err)
}

func adminCmd(cfg string, args []string) {
	if len(args) == 0 || args[0] != "set-password" {
		fatalf("usage: admin set-password --password-file FILE [--username admin]")
	}
	fs := flag.NewFlagSet("admin set-password", flag.ExitOnError)
	user := fs.String("username", "admin", "admin username")
	file := fs.String("password-file", "", "password file")
	_ = fs.Parse(args[1:])
	if *file == "" {
		fatalf("--password-file is required")
	}
	b, err := os.ReadFile(*file)
	if err != nil {
		fatalf("password file: %v", err)
	}
	pass := strings.TrimSpace(string(b))
	if len(pass) < 14 {
		fatalf("password must be at least 14 characters")
	}
	_, st, _, am, _, au := setup(cfg)
	defer st.DB.Close()
	if err := am.SetAdmin(context.Background(), *user, pass); err != nil {
		fatalf("set password: %v", err)
	}
	_ = au.Record(context.Background(), "cli", "admin_password_changed", "admin", *user, nil)
	fmt.Println("admin password updated")
}
func linksCmd(cfg string, args []string) {
	need(args, 1)
	c, st, us, _, _, _ := setup(cfg)
	defer st.DB.Close()
	u, err := us.Get(context.Background(), args[0])
	if err != nil {
		fatalf("links: %v", err)
	}
	l := links.ForUser(c, u)
	fmt.Printf("User: %s\nStatus: %s\n\nXray:\n  Status: %s\n  %s\n\nHysteria:\n  Status: %s\n  %s\n", u.Name, on(u.Enabled), on(u.Xray.Enabled), l.VLESS, on(u.Hysteria.Enabled), l.Hysteria)
}
func printUser(u users.User, j bool) {
	if j {
		_ = json.NewEncoder(os.Stdout).Encode(u)
		return
	}
	fmt.Printf("User: %s\nStatus: %s\nXray: %s\nHysteria: %s\n", u.Name, on(u.Enabled), on(u.Xray.Enabled), on(u.Hysteria.Enabled))
}
func on(v bool) string {
	if v {
		return "enabled"
	}
	return "disabled"
}
func need(a []string, n int) {
	if len(a) < n {
		fatalf("not enough arguments")
	}
}
func must(err error) {
	if err != nil {
		fatalf("operation failed: %v", err)
	}
}

var _ = strings.TrimSpace
