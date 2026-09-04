package main

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

const bootScriptName = "boot-ai.ipxe"

// handleBoot answers the iPXE chainload. In work mode it hands control back to
// the firmware so the local NVMe boots; in ai mode it serves the netboot script.
func (w *Waker) handleBoot(rw http.ResponseWriter, r *http.Request) {
	mode := w.state.Mode()
	log.Printf("boot: %s requested boot script, mode=%s", clientIP(r), mode)

	rw.Header().Set("Content-Type", "text/plain; charset=utf-8")
	rw.Header().Set("Cache-Control", "no-store")

	if mode == ModeWork {
		// One-shot: the next unattended wake goes back to the AI image.
		if w.cfg.ResetAfterBoot {
			if err := w.state.Set(ModeAI); err != nil {
				log.Printf("boot: could not reset mode: %v", err)
			}
		}
		io.WriteString(rw, "#!ipxe\n"+
			"echo Waker: work mode, handing back to local boot order\n"+
			"exit\n")
		return
	}

	path := filepath.Join(w.cfg.NetbootDir, bootScriptName)
	raw, err := os.ReadFile(path)
	if err != nil {
		log.Printf("boot: %s missing (%v), falling back to local boot", path, err)
		io.WriteString(rw, "#!ipxe\n"+
			"echo Waker: no AI image configured, booting local disk\n"+
			"sleep 3\n"+
			"exit\n")
		return
	}

	script := strings.ReplaceAll(string(raw), "{{BASE}}", w.cfg.NetbootBase)
	if !strings.HasPrefix(script, "#!ipxe") {
		script = "#!ipxe\n" + script
	}
	io.WriteString(rw, script)
}

func clientIP(r *http.Request) string {
	if host, _, err := splitHostPortSafe(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

func splitHostPortSafe(addr string) (string, string, error) {
	i := strings.LastIndex(addr, ":")
	if i < 0 {
		return "", "", fmt.Errorf("no port in %q", addr)
	}
	return strings.Trim(addr[:i], "[]"), addr[i+1:], nil
}
