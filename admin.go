package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"time"
)

type statusResponse struct {
	Mode        string          `json:"mode"`
	ModeUpdated time.Time       `json:"mode_updated"`
	Online      bool            `json:"online"`
	Waking      bool            `json:"waking"`
	Target      string          `json:"target"`
	MAC         string          `json:"mac"`
	Models      json.RawMessage `json:"models,omitempty"`
	LoadedCount int             `json:"loaded_models"`
}

func (w *Waker) handleStatus(rw http.ResponseWriter, r *http.Request) {
	online := w.Online()
	mode, updated := w.state.Snapshot()

	resp := statusResponse{
		Mode:        mode,
		ModeUpdated: updated,
		Online:      online,
		Waking:      w.Waking(),
		Target:      w.cfg.TargetAddr(),
		MAC:         w.cfg.MAC.String(),
	}

	if online {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		if raw, err := w.ollamaPS(ctx); err == nil {
			resp.Models = raw
			var ps struct {
				Models []struct {
					Name string `json:"name"`
				} `json:"models"`
			}
			if json.Unmarshal(raw, &ps) == nil {
				resp.LoadedCount = len(ps.Models)
			}
		}
	}

	writeJSON(rw, http.StatusOK, resp)
}

func (w *Waker) handleMode(rw http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		mode, updated := w.state.Snapshot()
		writeJSON(rw, http.StatusOK, map[string]any{"mode": mode, "mode_updated": updated})
	case http.MethodPost, http.MethodPut:
		mode := r.URL.Query().Get("mode")
		if mode == "" {
			mode = r.FormValue("mode")
		}
		if err := w.state.Set(mode); err != nil {
			writeJSON(rw, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		log.Printf("admin: mode set to %s by %s", mode, clientIP(r))
		writeJSON(rw, http.StatusOK, map[string]string{"mode": mode})
	default:
		rw.Header().Set("Allow", "GET, POST")
		http.Error(rw, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleWake is the "start my PC" button: pick the mode, then send the packet.
func (w *Waker) handleWake(rw http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		rw.Header().Set("Allow", "POST")
		http.Error(rw, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	mode := r.URL.Query().Get("mode")
	if mode == "" {
		mode = r.FormValue("mode")
	}
	if mode == "" {
		mode = ModeWork // the button exists mainly to boot the desktop
	}
	if err := w.state.Set(mode); err != nil {
		writeJSON(rw, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	if w.Online() {
		writeJSON(rw, http.StatusOK, map[string]any{"mode": mode, "online": true, "sent": false})
		return
	}

	log.Printf("admin: wake requested by %s, mode=%s", clientIP(r), mode)
	if err := w.Wake(); err != nil {
		writeJSON(rw, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(rw, http.StatusOK, map[string]any{"mode": mode, "online": false, "sent": true})
}

// adminGuard is a second lock next to whatever sits in front (Authentik).
// Traefik can inject the header, see the labels in docker-compose.yml.
func (w *Waker) adminGuard(next http.Handler) http.Handler {
	token := w.cfg.AdminToken
	if token == "" {
		return next
	}
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if !tokenMatches(token, bearerToken(r), r.Header.Get("X-Waker-Token"), r.URL.Query().Get("token")) {
			http.Error(rw, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(rw, r)
	})
}

func writeJSON(rw http.ResponseWriter, code int, body any) {
	rw.Header().Set("Content-Type", "application/json; charset=utf-8")
	rw.Header().Set("Cache-Control", "no-store")
	rw.WriteHeader(code)
	_ = json.NewEncoder(rw).Encode(body)
}

const adminIndex = `<!doctype html>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>AI Waker</title>
<style>
  :root { color-scheme: dark; }
  body { margin:0; padding:2rem; background:#14161a; color:#e6e8eb;
         font:15px/1.5 system-ui,-apple-system,Segoe UI,sans-serif; }
  main { max-width:34rem; margin:0 auto; }
  h1 { font-size:1.2rem; margin:0 0 1.5rem; }
  .card { background:#1c1f25; border:1px solid #2a2f38; border-radius:10px; padding:1rem 1.25rem; margin-bottom:1rem; }
  .row { display:flex; justify-content:space-between; gap:1rem; padding:.35rem 0; }
  .row span:first-child { color:#98a2b3; }
  .dot { display:inline-block; width:.6rem; height:.6rem; border-radius:50%; margin-right:.4rem; }
  .up { background:#3ddc84; } .down { background:#5b616e; } .busy { background:#f5a623; }
  button { font:inherit; padding:.6rem 1rem; border-radius:8px; border:1px solid #2a2f38;
           background:#252a32; color:#e6e8eb; cursor:pointer; }
  button:hover { background:#2d333d; }
  button.primary { background:#3b5bdb; border-color:#3b5bdb; }
  button.primary:hover { background:#4c6ef5; }
  .actions { display:flex; gap:.6rem; flex-wrap:wrap; }
  pre { margin:0; white-space:pre-wrap; word-break:break-word; color:#98a2b3; font-size:13px; }
</style>
<main>
  <h1>AI Waker</h1>
  <div class="card">
    <div class="row"><span>Status</span><strong id="state">...</strong></div>
    <div class="row"><span>Boot-Modus</span><strong id="mode">...</strong></div>
    <div class="row"><span>Ziel</span><strong id="target">...</strong></div>
    <div class="row"><span>Geladene Modelle</span><strong id="models">...</strong></div>
  </div>
  <div class="card actions">
    <button class="primary" data-mode="work">Fuer Arbeit starten</button>
    <button data-mode="ai">Fuer AI starten</button>
    <button id="ai-only">Nur Modus auf AI setzen</button>
  </div>
  <div class="card"><pre id="log">bereit</pre></div>
</main>
<script>
  var logEl = document.getElementById("log");
  function log(msg) { logEl.textContent = new Date().toLocaleTimeString() + "  " + msg; }

  function refresh() {
    fetch("status", { headers: { "Accept": "application/json" } })
      .then(function (r) { return r.json(); })
      .then(function (s) {
        var cls = s.online ? "up" : (s.waking ? "busy" : "down");
        var txt = s.online ? "online" : (s.waking ? "startet ..." : "offline");
        document.getElementById("state").innerHTML = '<span class="dot ' + cls + '"></span>' + txt;
        document.getElementById("mode").textContent = s.mode;
        document.getElementById("target").textContent = s.target;
        document.getElementById("models").textContent = s.online ? String(s.loaded_models) : "-";
      })
      .catch(function (e) { log("Statusabfrage fehlgeschlagen: " + e); });
  }

  document.querySelectorAll("button[data-mode]").forEach(function (btn) {
    btn.addEventListener("click", function () {
      var mode = btn.getAttribute("data-mode");
      log("Magic Packet fuer Modus " + mode + " ...");
      fetch("wake?mode=" + mode, { method: "POST" })
        .then(function (r) { return r.json(); })
        .then(function (res) {
          log(res.error ? ("Fehler: " + res.error)
                        : (res.sent ? "Magic Packet gesendet, Modus " + res.mode
                                    : "PC laeuft bereits, Modus " + res.mode));
          refresh();
        })
        .catch(function (e) { log("Fehler: " + e); });
    });
  });

  document.getElementById("ai-only").addEventListener("click", function () {
    fetch("mode?mode=ai", { method: "POST" })
      .then(function (r) { return r.json(); })
      .then(function () { log("Modus auf ai gesetzt"); refresh(); });
  });

  refresh();
  setInterval(refresh, 5000);
</script>
`
