# local-ai-waker

Weckt die Workstation per Wake on LAN aus dem Standby, sobald eine AI-Anfrage
kommt, und leitet sie an Ollama weiter. OpenAI-kompatibel, Streaming inklusive.

```
Client ──► Traefik ──► ai-waker ──┬─ TCP-Probe :11434 ───► Workstation (Nobara, Ollama)
                                  ├─ Magic Packet (UDP 9/7)
                                  └─ Reverse Proxy (streaming)
```

Schlafen legt sich die Workstation selbst. Der Idle-Watchdog aus dem
Nobara-Setup ruft `systemctl suspend`, sobald Ollama kein Modell mehr geladen
hat (5 Minuten nach der letzten Anfrage), niemand am Rechner aktiv ist und kein
Programm den Schlaf blockiert. Der Waker weckt nur.

## Endpunkte

Zwei Listener im Container, keine veröffentlichten Ports. Von aussen geht alles
über Traefik, von innen über den Containernamen.

| Port | Endpunkt | Erreichbar über | Auth |
|---|---|---|---|
| 8080 | `/v1/*`, `/api/*` | `https://ai.$DOMAIN` | `WAKER_API_KEY` |
| 8080 | `/healthz` | intern | keine |
| 8081 | `/` | `https://waker.$DOMAIN` | Authentik |
| 8081 | `/status` | intern, `http://ai-waker:8081/status` | optional `WAKER_ADMIN_TOKEN` |
| 8081 | `/wake` (POST) | wie `/status` | wie `/status` |

## Setup

### Server

Voraussetzungen im bestehenden Stack:

- Docker Compose v2 und `git` auf dem Docker-Host, BuildKit klont das Repo damit
- das externe Traefik-Netz (Name in `TRAEFIK_NETWORK`, Standard `proxy`)
- DNS für `ai.<domain>` und `waker.<domain>` auf Traefik, etwa als Rewrite in
  AdGuard Home
- in Authentik ein Forward-Auth-Provider, der `waker.<domain>` abdeckt: Im
  Domain-Modus passiert das automatisch, im Single-Application-Modus braucht es
  eine eigene Application
- der Server im selben LAN-Segment wie die Workstation, sonst kommt der
  Broadcast nicht an

Der Server braucht keinen Clone. `docker-compose.yml` baut direkt aus diesem
GitHub-Repo: BuildKit fragt bei jedem Build den aktuellen Commit von `main` ab
und baut nur neu, wenn sich etwas geändert hat. Auf den Server gehören zwei
Dateien, im Verzeichnis deiner Compose-Stacks:

```bash
mkdir ai-waker && cd ai-waker
curl -fsSLO https://raw.githubusercontent.com/sleien/local-ai-waker/main/docker-compose.yml
curl -fsSL -o .env https://raw.githubusercontent.com/sleien/local-ai-waker/main/.env.example
chmod 600 .env
openssl rand -hex 32        # Ergebnis als WAKER_API_KEY eintragen
# WAKER_TARGET_HOST, WAKER_MAC, WAKER_WOL_TARGETS und DOMAIN anpassen
docker compose up -d --build
```

Auf den neusten Stand von `main` bringt dich wieder `docker compose up -d
--build`. Für reproduzierbare Deployments statt `#main` einen Tag oder Commit
in `build.context` eintragen. Lokale Änderungen sieht der Server-Build erst nach
einem Push; auf dem eigenen Rechner testest du mit `docker build -t
local-ai-waker .`.

Nach Änderungen an `.env` wieder `docker compose up -d`. `docker compose
restart` liest die Datei nicht neu ein, der Container liefe mit den alten Werten
weiter.

Ist `WAKER_API_KEY` leer, ist `https://ai.$DOMAIN` ohne Anmeldung offen: Jeder
kann die GPU benutzen und den PC wecken. Der Waker warnt dann beim Start im Log.

### Workstation

Das Setup-Repo `linux_setup` richtet die Workstation ein (Module `10-power-s3-wol`
und `20-ollama`). Hier zum Nachprüfen:

- BIOS: Wake on LAN an, ErP aus
- Kernel-Parameter `mem_sleep_default=deep`, damit echtes S3 statt s2idle greift
- `nvidia-suspend` und `nvidia-resume` aktiv
- WoL nur per Magic Packet: `nmcli con modify <con> 802-3-ethernet.wake-on-lan magic`
- Ollama mit `OLLAMA_HOST=0.0.0.0:11434` und `OLLAMA_KEEP_ALIVE=5m`
- Idle-Watchdog als systemd-Timer: Standby, wenn kein Modell geladen ist, keine
  Anfrage läuft und niemand am Rechner arbeitet. Eine gesperrte Sitzung gilt als
  abwesend, die automatische Bildschirmsperre muss also an bleiben.
- `ollama-sleep.service`: stoppt Ollama vor dem Standby, lädt beim Aufwachen
  `nvidia_uvm` neu und startet Ollama erst dann. Ohne den Reload rechnet Ollama
  nach einem Resume oft auf der CPU. Der Port öffnet erst, wenn die GPU wieder da
  ist, der Waker wartet so lange.

`OLLAMA_CONTEXT_LENGTH` setzt das Setup-Repo nicht, siehe Kontextlänge unten.

Vor dem ersten Einsatz einmal von Hand testen:

```bash
sudo rtcwake -m no -s 60 && sudo systemctl suspend
```

Nach dem Aufwachen eine Anfrage schicken, dann muss `ollama ps` in der Spalte
PROCESSOR `100% GPU` zeigen. Danach über `https://waker.<domain>` wecken statt
per RTC. `rtcwake -m mem` eignet sich nicht, weil es systemd und damit die
NVIDIA-Dienste umgeht. Klemmt das Aufwachen aus S3 grundsätzlich, im Watchdog
`systemctl poweroff` statt `systemctl suspend` eintragen. Der Waker braucht dafür
keine Änderung, das Aufwachen dauert dann nur eine Minute statt Sekunden.

## OpenAI-kompatible API

Ollama bringt unter `/v1` eine OpenAI-kompatible API mit, der Waker reicht sie
durch. Client-Konfiguration:

- Base URL: `https://ai.$DOMAIN/v1`
- API Key: Wert von `WAKER_API_KEY`
- Modell: der Name, wie Ollama ihn listet (`qwen2.5:32b`), nicht `gpt-4o`

Getestet mit dem offiziellen `openai` Python SDK gegen Ollama 0.34.1:
`models.list`, `models.retrieve`, Chat Completions mit und ohne Streaming,
Embeddings (mit einem Embedding-Modell), Responses API, falscher Key als
`AuthenticationError`. Streaming kommt tokenweise an, der Proxy puffert nicht.

Gegen das echte Deployment, am besten einmal bei schlafender Workstation:

```bash
scripts/smoke-openai.sh https://ai.example.ch/v1 "$WAKER_API_KEY" qwen2.5:32b
```

**Wer weckt, wer nicht.** Nur Anfragen, die ein Modell benutzen, wecken die
Workstation. Schläft sie, beantwortet der Waker die Abfragen, mit denen Clients
Modelle auflisten oder die Erreichbarkeit prüfen, selbst:

| Endpunkt | Antwort im Schlaf |
|---|---|
| `GET /v1/models`, `/v1/models/{id}` | letzte bekannte Liste |
| `GET /api/tags`, `/api/version` | letzter bekannter Stand |
| `GET /api/ps` | leere Liste, im Schlaf ist nichts geladen |
| `GET /` | `Ollama is running` |

Solche Antworten tragen `X-Waker-Cached: 1`. Der Cache liegt in
`/data/catalog.json`, übersteht Neustarts und wird bei laufender Workstation
alle `WAKER_CATALOG_REFRESH` sowie nach jedem Aufwachen aufgefrischt. Ist noch
nichts gecacht, weckt die erste Abfrage einmal. Ohne Cache würde Open WebUI den
Rechner bei jedem Seitenaufruf wecken, weil es dabei die Modellliste lädt.

Im echten LAN kommt die gecachte Antwort nach dem Probe-Timeout
(`WAKER_PROBE_TIMEOUT`, 2 s): Ein schlafender Host lehnt die Verbindung nicht
ab, er schweigt.

**Fehler** kommen unter `/v1` im OpenAI-Format, unter `/api` im Ollama-Format,
damit die Client-Bibliotheken typisierte Fehler werfen. Ein unbekanntes Modell
sieht im Schlaf genauso aus wie bei laufender Workstation. Den API-Key entfernt
der Waker vor dem Weiterleiten.

**Kontextlänge.** OpenAI-Clients können `num_ctx` nicht mitschicken, Ollama
wählt die Länge dann selbst anhand des VRAM. Auf der Workstation fest setzen,
etwa `OLLAMA_CONTEXT_LENGTH=32768`. Zu lange Prompts kürzt Ollama sonst ohne
Fehlermeldung.

## Wichtige Details

**Broadcast aus dem Container.** Der Dienst hängt im Traefik-Bridge-Netz und
schickt das Magic Packet an die Subnetz-Broadcast-Adresse (`192.168.1.255`),
nicht an `255.255.255.255`. Der Host leitet das als Link-Layer-Broadcast ins
LAN weiter. Kommt nichts an, ist der schnellste Gegentest `network_mode: host`
für den Container; dann braucht Traefik allerdings einen File-Provider statt
der Docker-Labels.

**WoL nur per Magic Packet.** Der Waker prüft die Workstation per TCP auf
Port 11434, bei jeder Anfrage, bei jeder `/status`-Abfrage von Homepage und
alle `WAKER_CATALOG_REFRESH`. Wacht die Netzwerkkarte auch auf Unicast- oder
ARP-Pakete auf, holt schon diese Prüfung den Rechner aus dem Schlaf. In
NetworkManager deshalb `802-3-ethernet.wake-on-lan magic` und nichts
zusätzlich.

**Timeouts.** Traefik v3 mit Standardwerten reicht. Getestet mit 3.7: 75 s
ohne Antwort und ein 80 s langer Stream kommen vollständig durch. Entscheidend
ist `writeTimeout`, Default `0`, also unbegrenzt. Steht in deiner Konfiguration
ein Wert, kappt Traefik jede längere Antwort: Mit `writeTimeout: 20s` brach der
Stream nach 20 s ab, und die späte Antwort kam leer an. `readTimeout` hat im
selben Test mit 10 s nichts abgebrochen.

**Auth.** `ai.$DOMAIN` läuft ohne Authentik, weil API-Clients keinen
Browser-Login machen können. Der Key geht als `Authorization: Bearer ...` oder
`X-Api-Key` mit; OpenAI-SDKs und Open WebUI senden ihn als Bearer. Die
Router-Regel deckt nur `/api` und `/v1` ab. `waker.$DOMAIN` liegt hinter
Authentik. `WAKER_ADMIN_TOKEN` ist eine optionale zweite Sperre: Traefik hängt den
Token nach Authentik an jede Anfrage, der Browser merkt davon nichts. Aussen vor
bleibt nur, wer Port 8081 ohne Traefik erreicht, etwa andere Container im
Proxy-Netz. Das Homepage-Widget gehört dazu und braucht den Token dann als Header
`X-Waker-Token`.

**Ein Rechner für alles.** Anfragen landen im laufenden Desktop, auch während
du arbeitest oder spielst. Spiel und Modell teilen sich dann die 24 GB VRAM.
Passt beides nicht hinein, legt Ollama einen Teil des Modells auf die CPU und
wird deutlich langsamer. Der Watchdog legt den Rechner nicht schlafen, solange
du aktiv bist.

## Fehlersuche

| Symptom | Ansatz |
|---|---|
| Kein Aufwachen | `docker compose logs ai-waker` zeigt jedes gesendete Paket. Gegenprobe vom Server mit `wakeonlan <mac>`. Sonst BIOS: ErP aus, WoL an. |
| Weckt, aber Timeout | Auf der Workstation prüfen, ob Ollama nach dem Resume läuft und auf `0.0.0.0` hört. |
| Langsam nach dem Aufwachen | `ollama ps` zeigt CPU statt GPU: `journalctl -b -t ollama-idle` sagt, ob `nvidia_uvm` neu geladen wurde oder belegt war. |
| Client sieht keine Modelle | `scripts/smoke-openai.sh` zeigt, ob `/models` live, aus dem Cache oder mit Fehler antwortet. |
| PC wacht ohne Anfrage auf | WoL-Modus auf `magic` beschränken, siehe oben. |
| PC schläft nie ein | `sudo ollama-idle-watchdog --dry-run` auf der Workstation listet jede Bedingung, die den Standby verhindert. |
| Nach Update kein WoL | `nmcli con modify <con> 802-3-ethernet.wake-on-lan magic` erneut setzen. |
