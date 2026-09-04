package main

import (
	"context"
	"errors"
	"io"
	"log"
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

	state := newState(cfg.StateFile, cfg.DefaultMode)
	waker := newWaker(cfg, state)

	log.Printf("target %s, mac %s, wol targets %v", cfg.TargetAddr(), cfg.MAC, cfg.WOLTargets)
	log.Printf("boot mode is %q, netboot base %s", state.Mode(), cfg.NetbootBase)

	// LAN listener: iPXE and AI clients talk to this one.
	pub := http.NewServeMux()
	pub.HandleFunc("/healthz", handleHealthz)
	pub.HandleFunc("/boot.ipxe", waker.handleBoot)
	pub.Handle("/netboot/", http.StripPrefix("/netboot/", http.FileServer(http.Dir(cfg.NetbootDir))))
	pub.Handle("/", waker.apiKeyGuard(waker.proxyHandler()))

	// Admin listener: not published to the LAN, reached through Traefik.
	adm := http.NewServeMux()
	adm.HandleFunc("/healthz", handleHealthz)
	adm.Handle("/status", waker.adminGuard(http.HandlerFunc(waker.handleStatus)))
	adm.Handle("/mode", waker.adminGuard(http.HandlerFunc(waker.handleMode)))
	adm.Handle("/wake", waker.adminGuard(http.HandlerFunc(waker.handleWake)))
	adm.Handle("/", waker.adminGuard(http.HandlerFunc(handleIndex)))

	servers := []*http.Server{
		{
			Addr:     cfg.Listen,
			Handler:  logRequests("lan", pub),
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

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, srv := range servers {
		_ = srv.Shutdown(ctx)
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
