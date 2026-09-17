package main

import (
	"context"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	log.SetFlags(log.LstdFlags | log.LUTC)

	cfg, err := loadConfig()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	if len(os.Args) > 1 && os.Args[1] == "wol-relay" {
		if cfg.WOLRelay == "" {
			log.Fatal("config: WAKER_WOL_RELAY is required for wol-relay")
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		if err := runRelay(ctx, cfg, cfg.WOLRelay); err != nil {
			log.Fatalf("wol relay: %v", err)
		}
		return
	}

	waker := newWaker(cfg, newCatalog(cfg.CatalogFile))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go waker.catalogLoop(ctx)

	if cfg.WOLRelay != "" {
		log.Printf("target %s, mac %s, magic packets via relay %s", cfg.TargetAddr(), cfg.MAC, cfg.WOLRelay)
	} else {
		log.Printf("target %s, mac %s, wol targets %v", cfg.TargetAddr(), cfg.MAC, cfg.WOLTargets)
	}
	if cfg.APIKey == "" {
		log.Print("WARNING: WAKER_API_KEY is empty, the API accepts requests without authentication")
	}

	// Proxy listener: OpenAI and Ollama API clients, reached through Traefik.
	pub := http.NewServeMux()
	pub.HandleFunc("/healthz", handleHealthz)
	pub.Handle("/", waker.apiKeyGuard(waker.proxyHandler()))

	// Admin listener: status and wake button, behind Authentik.
	adm := http.NewServeMux()
	adm.HandleFunc("/healthz", handleHealthz)
	adm.Handle("/status", waker.adminGuard(http.HandlerFunc(waker.handleStatus)))
	adm.Handle("/wake", waker.adminGuard(http.HandlerFunc(waker.handleWake)))
	adm.Handle("/", waker.adminGuard(http.HandlerFunc(handleIndex)))

	servers := []*http.Server{
		{
			Addr:     cfg.Listen,
			Handler:  logRequests("api", pub),
			ErrorLog: log.Default(),
			// No write timeout: streamed generations run long.
			ReadHeaderTimeout: 30 * time.Second,
		},
		{
			Addr:              cfg.AdminListen,
			Handler:           logRequests("admin", adm),
			ErrorLog:          log.Default(),
			ReadHeaderTimeout: 30 * time.Second,
			WriteTimeout:      60 * time.Second,
		},
	}

	for _, srv := range servers {
		s := srv
		go func() {
			log.Printf("listening on %s", s.Addr)
			if err := s.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Fatalf("listen %s: %v", s.Addr, err)
			}
		}()
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop
	log.Print("shutting down")
	cancel()

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	for _, srv := range servers {
		_ = srv.Shutdown(shutdownCtx)
	}
}

func handleHealthz(rw http.ResponseWriter, _ *http.Request) {
	rw.Header().Set("Content-Type", "text/plain; charset=utf-8")
	io.WriteString(rw, "ok\n")
}

func handleIndex(rw http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(rw, r)
		return
	}
	rw.Header().Set("Content-Type", "text/html; charset=utf-8")
	io.WriteString(rw, adminIndex)
}

func logRequests(tag string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(rw, r)
		log.Printf("%s %s %s %s %s", tag, clientIP(r), r.Method, r.URL.Path, time.Since(start).Round(time.Millisecond))
	})
}

func clientIP(r *http.Request) string {
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}
