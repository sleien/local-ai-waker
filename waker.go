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

// Waker owns the wake state machine. Concurrent requests that arrive during a
// cold start share a single wake attempt instead of spamming magic packets.
type Waker struct {
	cfg   Config
	state *State

	mu        sync.Mutex
	waking    bool
	done      chan struct{}
	wakeErr   error
	lastWake  time.Time
	lastWakeD time.Duration
}

func newWaker(cfg Config, state *State) *Waker {
	return &Waker{cfg: cfg, state: state}
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

func (w *Waker) Waking() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.waking
}

// EnsureOnline returns once the workstation answers, waking it if needed.
// cold is true when this request had to wait for a boot.
func (w *Waker) EnsureOnline(ctx context.Context) (cold bool, err error) {
	if w.Online() {
		return false, nil
	}

	w.mu.Lock()
	if !w.waking {
		w.waking = true
		w.done = make(chan struct{})
		go w.wakeLoop()
	}
	done := w.done
	w.mu.Unlock()

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

// Wake sends a magic packet without waiting, used by the admin endpoints.
func (w *Waker) Wake() error {
	w.mu.Lock()
	w.lastWake = time.Now()
	w.mu.Unlock()
	log.Printf("wol: sending magic packet for %s to %v", w.cfg.MAC, w.cfg.WOLTargets)
	return sendMagicPacket(w.cfg.MAC, w.cfg.WOLTargets)
}

func (w *Waker) wakeLoop() {
	started := time.Now()
	var werr error

	defer func() {
		w.mu.Lock()
		w.waking = false
		w.wakeErr = werr
		w.lastWakeD = time.Since(started)
		close(w.done)
		w.mu.Unlock()
	}()

	// A wake triggered by an AI request must boot the AI image, never the desktop.
	if err := w.state.Set(ModeAI); err != nil {
		log.Printf("wake: could not persist ai mode: %v", err)
	}

	deadline := started.Add(w.cfg.WakeTimeout)
	var lastSend time.Time

	for time.Now().Before(deadline) {
		if time.Since(lastSend) >= w.cfg.WOLRepeat {
			if err := w.Wake(); err != nil {
				log.Printf("wake: %v", err)
			}
			lastSend = time.Now()
		}
		if w.Online() {
			log.Printf("wake: workstation up after %s", time.Since(started).Round(time.Second))
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
