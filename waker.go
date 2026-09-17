package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"sync"
	"time"
)

// Waker owns the wake state machine. Concurrent requests that arrive while the
// workstation resumes share a single wake attempt instead of spamming packets.
type Waker struct {
	cfg     Config
	catalog *Catalog

	mu       sync.Mutex
	waking   bool
	done     chan struct{}
	wakeErr  error
	lastWake time.Time
}

func newWaker(cfg Config, catalog *Catalog) *Waker {
	return &Waker{cfg: cfg, catalog: catalog}
}

// Online reports whether the Ollama port on the workstation accepts connections.
func (w *Waker) Online() bool {
	conn, err := net.DialTimeout("tcp", w.cfg.TargetAddr(), w.cfg.ProbeTimeout)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// Status reports whether a wake attempt is running and when the last one started.
func (w *Waker) Status() (waking bool, lastWake time.Time) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.waking, w.lastWake
}

// StartWake launches the wake loop unless one is already running. The returned
// channel closes when that attempt ends.
func (w *Waker) StartWake() <-chan struct{} {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.waking {
		w.waking = true
		w.lastWake = time.Now()
		w.done = make(chan struct{})
		go w.wakeLoop()
	}
	return w.done
}

// EnsureOnline returns once the workstation answers, waking it if needed.
// cold is true when this request had to wait for the resume.
func (w *Waker) EnsureOnline(ctx context.Context) (cold bool, err error) {
	if w.Online() {
		return false, nil
	}

	done := w.StartWake()
	select {
	case <-done:
		w.mu.Lock()
		err = w.wakeErr
		w.mu.Unlock()
		return true, err
	case <-ctx.Done():
		return true, ctx.Err()
	}
}

func (w *Waker) wakeLoop() {
	started := time.Now()
	var werr error

	defer func() {
		w.mu.Lock()
		w.waking = false
		w.wakeErr = werr
		close(w.done)
		w.mu.Unlock()
	}()

	deadline := started.Add(w.cfg.WakeTimeout)
	var lastSend time.Time

	for time.Now().Before(deadline) {
		// UDP gives no delivery guarantee, so keep sending until the box answers.
		if time.Since(lastSend) >= w.cfg.WOLRepeat {
			log.Printf("wol: sending magic packet for %s to %v", w.cfg.MAC, w.cfg.WOLTargets)
			if err := sendMagicPacket(w.cfg.MAC, w.cfg.WOLTargets); err != nil {
				log.Printf("wake: %v", err)
			}
			lastSend = time.Now()
		}
		if w.Online() {
			log.Printf("wake: workstation up after %s", time.Since(started).Round(time.Second))
			go w.refreshCatalog(context.Background())
			return
		}
		time.Sleep(w.cfg.ProbeInterval)
	}

	werr = fmt.Errorf("workstation %s did not answer within %s", w.cfg.TargetAddr(), w.cfg.WakeTimeout)
	log.Printf("wake: %v", werr)
}

// ollamaPS returns the currently loaded models, if the workstation is up.
func (w *Waker) ollamaPS(ctx context.Context) (json.RawMessage, error) {
	url := fmt.Sprintf("http://%s/api/ps", w.cfg.TargetAddr())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ollama /api/ps: %s", resp.Status)
	}
	return json.RawMessage(body), nil
}
