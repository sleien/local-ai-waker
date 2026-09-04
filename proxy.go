package main

import (
	"context"
	"crypto/subtle"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type ctxKey string

const ctxColdStart ctxKey = "waker.cold"

// proxyHandler forwards every request to Ollama on the workstation, waking the
// machine first when it is powered off.
func (w *Waker) proxyHandler() http.Handler {
	target := &url.URL{Scheme: "http", Host: w.cfg.TargetAddr()}

	rp := httputil.NewSingleHostReverseProxy(target)
	// -1 flushes immediately, which is what token streaming needs.
	rp.FlushInterval = -1
	rp.Transport = &http.Transport{
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		// Loading a 20 GB model from NVMe into VRAM takes a while; the
		// first response header can be minutes out on a cold model.
		ResponseHeaderTimeout: 15 * time.Minute,
		IdleConnTimeout:       90 * time.Second,
		ExpectContinueTimeout: time.Second,
		MaxIdleConnsPerHost:   8,
	}
	rp.ErrorHandler = func(rw http.ResponseWriter, r *http.Request, err error) {
		log.Printf("proxy: %s %s: %v", r.Method, r.URL.Path, err)
		http.Error(rw, "upstream error: "+err.Error(), http.StatusBadGateway)
	}
	rp.ModifyResponse = func(resp *http.Response) error {
		if cold, _ := resp.Request.Context().Value(ctxColdStart).(bool); cold {
			resp.Header.Set("X-Waker-Cold-Start", "1")
		}
		return nil
	}

	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		wakeCtx, cancel := context.WithTimeout(r.Context(), w.cfg.WakeTimeout+30*time.Second)
		cold, err := w.EnsureOnline(wakeCtx)
		cancel()
		if err != nil {
			log.Printf("proxy: wake failed for %s %s: %v", r.Method, r.URL.Path, err)
			rw.Header().Set("Retry-After", strconv.Itoa(int(w.cfg.WakeTimeout.Seconds())))
			http.Error(rw, "workstation unavailable: "+err.Error(), http.StatusServiceUnavailable)
			return
		}
		// The proxied request itself stays untimed: generation can be long.
		r = r.WithContext(context.WithValue(r.Context(), ctxColdStart, cold))
		rp.ServeHTTP(rw, r)
	})
}

// apiKeyGuard protects the LAN listener when WAKER_API_KEY is set. Without a
// key the endpoint is open, which is fine for a trusted VLAN but not for
// anything published through Traefik.
func (w *Waker) apiKeyGuard(next http.Handler) http.Handler {
	key := w.cfg.APIKey
	if key == "" {
		return next
	}
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if !tokenMatches(key, bearerToken(r), r.Header.Get("X-Api-Key")) {
			http.Error(rw, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(rw, r)
	})
}

func bearerToken(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	if len(auth) > 7 && strings.EqualFold(auth[:7], "bearer ") {
		return strings.TrimSpace(auth[7:])
	}
	return ""
}

func tokenMatches(want string, candidates ...string) bool {
	for _, got := range candidates {
		if got != "" && subtle.ConstantTimeCompare([]byte(want), []byte(got)) == 1 {
			return true
		}
	}
	return false
}
