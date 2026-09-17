package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Clients poll these to list models or check health: Open WebUI on every page
// load, IDE plugins every few minutes. Answering them from the last known state
// keeps a sleeping workstation asleep; only requests that use a model wake it.
var catalogPaths = []string{"/v1/models", "/api/tags", "/api/version"}

const maxCatalogBody = 1 << 20

func isDiscoveryPath(path string) bool {
	if path == "/" || path == "/api/ps" || strings.HasPrefix(path, "/v1/models/") {
		return true
	}
	return isCatalogPath(path)
}

func isCatalogPath(path string) bool {
	for _, p := range catalogPaths {
		if path == p {
			return true
		}
	}
	return false
}

// Catalog holds the last successful responses of the catalog paths.
type Catalog struct {
	path    string
	mu      sync.RWMutex
	entries map[string]catalogEntry
}

type catalogEntry struct {
	Body        json.RawMessage `json:"body"`
	ContentType string          `json:"content_type"`
	Fetched     time.Time       `json:"fetched"`
}

func newCatalog(path string) *Catalog {
	c := &Catalog{path: path, entries: map[string]catalogEntry{}}
	if b, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(b, &c.entries); err != nil {
			log.Printf("catalog: ignoring unreadable %s: %v", path, err)
			c.entries = map[string]catalogEntry{}
		}
	}
	return c
}

func (c *Catalog) Get(path string) (catalogEntry, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	e, ok := c.entries[path]
	return e, ok
}

func (c *Catalog) Put(path string, body []byte, contentType string) {
	if !json.Valid(body) {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	prev, had := c.entries[path]
	c.entries[path] = catalogEntry{
		Body:        append(json.RawMessage(nil), body...),
		ContentType: contentType,
		Fetched:     time.Now(),
	}
	if had && bytes.Equal(prev.Body, body) {
		return // only the timestamp moved, not worth a disk write
	}
	if c.path == "" {
		return
	}
	b, err := json.Marshal(c.entries)
	if err == nil {
		err = writeFileAtomic(c.path, b)
	}
	if err != nil {
		log.Printf("catalog: persist: %v", err)
	}
}

// Model looks up one entry of the cached /v1/models list. known is false when
// no list has been cached yet, so the caller can fall back to waking.
func (c *Catalog) Model(id string) (model json.RawMessage, known, found bool) {
	entry, ok := c.Get("/v1/models")
	if !ok {
		return nil, false, false
	}
	var list struct {
		Data []json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(entry.Body, &list); err != nil {
		return nil, false, false
	}

	// Ollama resolves a bare name to its :latest tag, so do the same.
	candidates := []string{id}
	if !strings.Contains(id, ":") {
		candidates = append(candidates, id+":latest")
	}
	for _, raw := range list.Data {
		var m struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(raw, &m) != nil {
			continue
		}
		for _, want := range candidates {
			if m.ID == want {
				return raw, true, true
			}
		}
	}
	return nil, true, false
}

// serveDiscovery answers discovery requests while the workstation is not
// reachable. It returns false when the request has to go upstream: the
// workstation is up, or nothing has been cached yet.
func (w *Waker) serveDiscovery(rw http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}
	path := r.URL.Path
	if !isDiscoveryPath(path) || w.Online() {
		return false
	}

	switch {
	case path == "/":
		// Same body as Ollama's root handler, which health checks look for.
		writeCached(rw, "text/plain; charset=utf-8", []byte("Ollama is running"), time.Time{})
	case path == "/api/ps":
		// A sleeping machine has nothing loaded.
		writeCached(rw, "application/json; charset=utf-8", []byte(`{"models":[]}`), time.Time{})
	case strings.HasPrefix(path, "/v1/models/"):
		id := strings.TrimPrefix(path, "/v1/models/")
		model, known, found := w.catalog.Model(id)
		if !known {
			return false
		}
		if !found {
			// Same wording and type as Ollama, so clients see no difference.
			name := id
			if !strings.Contains(name, ":") {
				name += ":latest"
			}
			writeAPIError(rw, r, http.StatusNotFound, "model '"+name+"' not found", "not_found_error", "")
			return true
		}
		list, _ := w.catalog.Get("/v1/models")
		writeCached(rw, list.ContentType, model, list.Fetched)
	default:
		entry, ok := w.catalog.Get(path)
		if !ok {
			return false
		}
		writeCached(rw, entry.ContentType, entry.Body, entry.Fetched)
	}
	return true
}

func writeCached(rw http.ResponseWriter, contentType string, body []byte, fetched time.Time) {
	if contentType == "" {
		contentType = "application/json; charset=utf-8"
	}
	rw.Header().Set("Content-Type", contentType)
	rw.Header().Set("Content-Length", strconv.Itoa(len(body)))
	rw.Header().Set("X-Waker-Cached", "1")
	if !fetched.IsZero() {
		rw.Header().Set("X-Waker-Cached-At", fetched.UTC().Format(time.RFC3339))
	}
	rw.WriteHeader(http.StatusOK)
	_, _ = rw.Write(body)
}

// captureDiscovery copies successful catalog responses into the cache on
// their way to the client.
func (w *Waker) captureDiscovery(resp *http.Response) error {
	req := resp.Request
	if req.Method != http.MethodGet || resp.StatusCode != http.StatusOK ||
		!isCatalogPath(req.URL.Path) || resp.Header.Get("Content-Encoding") != "" {
		return nil
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxCatalogBody+1))
	if err != nil {
		return err
	}
	if len(body) > maxCatalogBody {
		// Too big to cache: the client still gets all of it, read part first.
		resp.Body = struct {
			io.Reader
			io.Closer
		}{io.MultiReader(bytes.NewReader(body), resp.Body), resp.Body}
		return nil
	}
	_ = resp.Body.Close()
	resp.Body = io.NopCloser(bytes.NewReader(body))
	w.catalog.Put(req.URL.Path, body, resp.Header.Get("Content-Type"))
	return nil
}

// refreshCatalog reads the catalog paths directly, so the cache also learns
// about models pulled or removed locally on the workstation.
func (w *Waker) refreshCatalog(ctx context.Context) {
	client := &http.Client{Timeout: 10 * time.Second}
	for _, path := range catalogPaths {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+w.cfg.TargetAddr()+path, nil)
		if err != nil {
			return
		}
		resp, err := client.Do(req)
		if err != nil {
			log.Printf("catalog: refresh %s: %v", path, err)
			return
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, maxCatalogBody))
		_ = resp.Body.Close()
		if err == nil && resp.StatusCode == http.StatusOK {
			w.catalog.Put(path, body, resp.Header.Get("Content-Type"))
		}
	}
}

func (w *Waker) catalogLoop(ctx context.Context) {
	if w.cfg.CatalogRefresh <= 0 {
		return
	}
	ticker := time.NewTicker(w.cfg.CatalogRefresh)
	defer ticker.Stop()
	for {
		if w.Online() {
			w.refreshCatalog(ctx)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
