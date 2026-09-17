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
	director := rp.Director
	rp.Director = func(req *http.Request) {
		director(req)
		if w.cfg.APIKey != "" {
			// The key authenticates against the waker; keep it out of upstream logs.
			req.Header.Del("Authorization")
			req.Header.Del("X-Api-Key")
		}
	}
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
		writeAPIError(rw, r, http.StatusBadGateway, "upstream error: "+err.Error(), "server_error", "upstream_error")
	}
	rp.ModifyResponse = func(resp *http.Response) error {
		if cold, _ := resp.Request.Context().Value(ctxColdStart).(bool); cold {
			resp.Header.Set("X-Waker-Cold-Start", "1")
		}
		return w.captureDiscovery(resp)
	}

	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if w.serveDiscovery(rw, r) {
			return
		}

		wakeCtx, cancel := context.WithTimeout(r.Context(), w.cfg.WakeTimeout+30*time.Second)
		cold, err := w.EnsureOnline(wakeCtx)
		cancel()
		if err != nil {
			log.Printf("proxy: wake failed for %s %s: %v", r.Method, r.URL.Path, err)
			rw.Header().Set("Retry-After", strconv.Itoa(int(w.cfg.WakeTimeout.Seconds())))
			writeAPIError(rw, r, http.StatusServiceUnavailable, "workstation unavailable: "+err.Error(), "server_error", "workstation_unavailable")
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
			writeAPIError(rw, r, http.StatusUnauthorized, "invalid or missing API key", "invalid_request_error", "invalid_api_key")
			return
		}
		next.ServeHTTP(rw, r)
	})
}

// writeAPIError answers in the shape the caller's client library parses:
// OpenAI's error object under /v1, Ollama's {"error": "..."} everywhere else.
// An empty code is sent as null, like Ollama does.
func writeAPIError(rw http.ResponseWriter, r *http.Request, status int, message, errType, code string) {
	if strings.HasPrefix(r.URL.Path, "/v1/") {
		var codeField any
		if code != "" {
			codeField = code
		}
		writeJSON(rw, status, map[string]any{
			"error": map[string]any{"message": message, "type": errType, "param": nil, "code": codeField},
		})
		return
	}
	writeJSON(rw, status, map[string]string{"error": message})
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
