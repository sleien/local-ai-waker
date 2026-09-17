package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"time"
)

type statusResponse struct {
	Online      bool            `json:"online"`
	Waking      bool            `json:"waking"`
	LastWake    *time.Time      `json:"last_wake,omitempty"`
	Target      string          `json:"target"`
	MAC         string          `json:"mac"`
	Models      json.RawMessage `json:"models,omitempty"`
	LoadedCount int             `json:"loaded_models"`
}

func (w *Waker) handleStatus(rw http.ResponseWriter, r *http.Request) {
	online := w.Online()
	waking, lastWake := w.Status()

	resp := statusResponse{
		Online: online,
		Waking: waking,
		Target: w.cfg.TargetAddr(),
		MAC:    w.cfg.MAC.String(),
	}
	if !lastWake.IsZero() {
		resp.LastWake = &lastWake
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

// handleWake is the "wake my PC" button. It starts the same wake loop a request
// would, without waiting for the result; /status shows the progress.
func (w *Waker) handleWake(rw http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		rw.Header().Set("Allow", "POST")
		http.Error(rw, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if w.Online() {
		writeJSON(rw, http.StatusOK, map[string]any{"online": true, "waking": false})
		return
	}
	log.Printf("admin: wake requested by %s", clientIP(r))
	w.StartWake()
	writeJSON(rw, http.StatusAccepted, map[string]any{"online": false, "waking": true})
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
  body { margin:0; padding:2rem 1rem; background:#14161a; color:#e6e8eb;
         font:15px/1.5 system-ui,-apple-system,Segoe UI,sans-serif; }
  main { max-width:34rem; margin:0 auto; }
  h1 { font-size:1.2rem; margin:0 0 1.5rem; }
  .card { background:#1c1f25; border:1px solid #2a2f38; border-radius:10px; padding:1rem 1.25rem; margin-bottom:1rem; }
  .row { display:flex; justify-content:space-between; gap:1rem; padding:.35rem 0; }
  .row span:first-child { color:#98a2b3; }
  .dot { display:inline-block; width:.6rem; height:.6rem; border-radius:50%; margin-right:.4rem; }
  .up { background:#3ddc84; } .down { background:#5b616e; } .busy { background:#f5a623; }
  button { font:inherit; padding:.6rem 1rem; border-radius:8px; border:1px solid #3b5bdb;
           background:#3b5bdb; color:#fff; cursor:pointer; }
  button:hover { background:#4c6ef5; }
  button:disabled { opacity:.5; cursor:default; }
  pre { margin:0; white-space:pre-wrap; word-break:break-word; color:#98a2b3; font-size:13px; }
</style>
<main>
  <h1>AI Waker</h1>
  <div class="card">
    <div class="row"><span>Workstation</span><strong id="state">...</strong></div>
    <div class="row"><span>Ziel</span><strong id="target">...</strong></div>
    <div class="row"><span>Geladene Modelle</span><strong id="models">...</strong></div>
    <div class="row"><span>Zuletzt geweckt</span><strong id="last">...</strong></div>
  </div>
  <div class="card"><button id="wake">Aufwecken</button></div>
  <div class="card"><pre id="log">bereit</pre></div>
</main>
<script>
  var logEl = document.getElementById("log");
  var wakeBtn = document.getElementById("wake");
  function log(msg) { logEl.textContent = new Date().toLocaleTimeString() + "  " + msg; }

  function refresh() {
    fetch("status", { headers: { "Accept": "application/json" } })
      .then(function (r) { return r.json(); })
      .then(function (s) {
        var cls = s.online ? "up" : (s.waking ? "busy" : "down");
        var txt = s.online ? "wach" : (s.waking ? "wird geweckt ..." : "schlaeft");
        document.getElementById("state").innerHTML = '<span class="dot ' + cls + '"></span>' + txt;
        document.getElementById("target").textContent = s.target;
        document.getElementById("models").textContent = s.online ? String(s.loaded_models) : "-";
        document.getElementById("last").textContent = s.last_wake ? new Date(s.last_wake).toLocaleString() : "-";
        wakeBtn.disabled = s.online || s.waking;
      })
      .catch(function (e) { log("Statusabfrage fehlgeschlagen: " + e); });
  }

  wakeBtn.addEventListener("click", function () {
    log("Magic Packet wird gesendet ...");
    fetch("wake", { method: "POST" })
      .then(function (r) { return r.json(); })
      .then(function (res) {
        log(res.online ? "Workstation ist schon wach" : "Weckvorgang laeuft, Status aktualisiert sich");
        refresh();
      })
      .catch(function (e) { log("Fehler: " + e); });
  });

  refresh();
  setInterval(refresh, 3000);
</script>
`
