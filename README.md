# local-ai-waker

Weckt die Workstation per Wake on LAN, sobald eine AI-Anfrage kommt, und
leitet die Anfrage an Ollama weiter. Zusätzlich entscheidet der Dienst per
iPXE, welches Betriebssystem startet: das schlanke AI-Image oder das
produktive Linux.

```
Client ──► Traefik ──► ai-waker ──┬─ TCP-Probe :11434 ─► Workstation
                                  ├─ Magic Packet (UDP 9/7)
                                  └─ Reverse Proxy (streaming)

Workstation-Firmware ──► proxy-DHCP ──► iPXE ──► ai-waker /boot.ipxe
                                                   ├─ mode=ai   ► Netboot AI-Image
                                                   └─ mode=work ► exit, lokale NVMe
```

Das Herunterfahren nach fünf Minuten Leerlauf macht der Watchdog im AI-Image,
nicht dieser Dienst.

## Endpunkte

Zwei Listener im Container, **keine** veröffentlichten Ports. Alles von aussen
kommt über Traefik, alles von innen über den Containernamen.

| Port | Endpunkt | Erreichbar über | Auth |
|---|---|---|---|
| 8080 | `/api/*`, `/v1/*` | `https://ai.$DOMAIN` | `WAKER_API_KEY` |
| 8080 | `/boot.ipxe`, `/netboot/*` | Traefik-Entrypoint `netboot`, Klartext-HTTP | keine, LAN only |
| 8080 | `/healthz` | intern | keine |
| 8081 | `/` | `https://waker.$DOMAIN` | Authentik |
| 8081 | `/status` | intern, z. B. `http://ai-waker:8081/status` | optional Token |
| 8081 | `/mode`, `/wake` | wie oben | wie oben |

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

## Warum ein Klartext-Port, wenn Traefik da ist

Für den AI-Pfad braucht es keinen: Clients gehen über `https://ai.$DOMAIN`,
Container im selben Netz über `http://ai-waker:8080`. Der offene Port existiert
allein für iPXE. Die Firmware kann kein TLS, folgt keinem 301 auf HTTPS und
soll beim Booten nicht von DNS abhängen, holt das Skript also unter
`http://192.168.1.10:8080/boot.ipxe`.

Drei Wege, das zu lösen, in dieser Reihenfolge:

1. **Eigener Traefik-Entrypoint** (so eingerichtet). In die statische Konfiguration:

   ```yaml
   entryPoints:
     netboot:
       address: ":8080"
   ```

   Am Traefik-Container `192.168.1.10:8080:8080` veröffentlichen, also an die
   LAN-Adresse gebunden statt an `0.0.0.0`. Ein Ingress, ein Logfile, und die
   Regel des Netboot-Routers deckt nur `/boot.ipxe` und `/netboot/` ab.
2. **Über den bestehenden `web`-Entrypoint**, falls dein HTTPS-Redirect als
   Router-Middleware hängt und nicht am Entrypoint. Dann kommt gar kein neuer
   Port dazu: Netboot-Router auf `web`, ohne die Redirect-Middleware. Bei
   `entryPoints.web.http.redirections` geht das nicht, das gilt für alle Router
   des Entrypoints.
3. **Port am Waker-Container** veröffentlichen. Der auskommentierte
   `ports`-Block in `docker-compose.yml` bindet ihn an `WAKER_SERVER_IP`.
   Nachteil: umgeht Traefik, und `/api/*` wäre dort nur durch
   `WAKER_API_KEY` geschützt.

Ohne Netboot, also solange du beim aktuellen Bootloader bleibst, braucht keine
der drei Varianten einen Port.

## Setup

```bash
cp .env.example .env
# WAKER_TARGET_HOST, WAKER_MAC, WAKER_WOL_TARGETS, DOMAIN eintragen
docker compose up -d --build
```

Test ohne Netboot, nur Wecken und Weiterleiten, direkt im Container:

```bash
docker compose exec ai-waker wget -qO- --post-data '{"model":"llama3.1","prompt":"hi"}' http://127.0.0.1:8080/api/generate
```

Der erste Aufruf blockiert, bis die Workstation antwortet (Boot plus Laden des
Modells, typisch 45 bis 90 Sekunden). Die Antwort trägt dann
`X-Waker-Cold-Start: 1`.

### Netboot aktivieren

```bash
./scripts/fetch-ipxe.sh          # iPXE-Binaries nach ./tftp
# LAN-Adressen in dnsmasq/pxe.conf anpassen
docker compose --profile netboot up -d
```

Danach das AI-Image bauen und Kernel, initrd und `boot-ai.ipxe` nach
`./netboot/` legen, siehe [netboot/README.md](netboot/README.md).

Im BIOS des X870E: Wake on LAN einschalten, ErP ausschalten, UEFI Network
Stack einschalten, Netzwerk vor der NVMe in die Bootreihenfolge.

## Wichtige Details

**Broadcast aus dem Container.** Der Dienst hängt im Traefik-Bridge-Netz und
schickt das Magic Packet an die Subnetz-Broadcast-Adresse (`192.168.1.255`),
nicht an `255.255.255.255`. Der Host leitet das als Link-Layer-Broadcast ins
LAN weiter. Kommt nichts an, ist der schnellste Gegentest `network_mode: host`
für den Container; dann braucht Traefik allerdings einen File-Provider statt
der Docker-Labels.

**Modus ist einmalig.** Nach einem `work`-Boot setzt der Dienst den Modus
zurück auf `ai` (`WAKER_RESET_MODE_AFTER_BOOT`). Ein späteres automatisches
Wecken durch eine Anfrage startet also wieder das AI-Image, nicht den Desktop.

**Timeouts.** Traefik v3 mit Standardwerten reicht. Getestet mit 3.7: 75 s
ohne Antwort (wie ein Kaltstart) und ein 80 s langer Stream kommen vollständig
durch. Entscheidend ist `writeTimeout`, Default `0`, also unbegrenzt. Steht in
deiner Konfiguration ein Wert, kappt Traefik jede längere Antwort: Mit
`writeTimeout: 20s` brach der Stream nach 20 s ab und der Kaltstart bekam eine
leere Antwort. `readTimeout` hat im selben Test mit 10 s nichts abgebrochen.

`FlushInterval: -1` im Proxy sorgt dafür, dass Streaming-Antworten sofort
durchgereicht werden statt gepuffert.

**Auth.** `ai.$DOMAIN` läuft ohne Authentik, weil API-Clients keinen
Browser-Login machen können; stattdessen `WAKER_API_KEY` setzen und als
`Authorization: Bearer ...` oder `X-Api-Key` mitschicken. Die Router-Regel
dort deckt nur `/api` und `/v1` ab, das Boot-Skript ist über den öffentlichen
Namen also nicht abrufbar. `waker.$DOMAIN`
liegt hinter Authentik. Wer zusätzlich `WAKER_ADMIN_TOKEN` setzt, lässt es von
Traefik injizieren, siehe die auskommentierte Middleware in
`docker-compose.yml`.

**WoL nur per Magic Packet.** Der Waker prüft die Workstation per TCP auf
Port 11434, bei jeder Anfrage, bei jeder `/status`-Abfrage von Homepage und
alle `WAKER_CATALOG_REFRESH`. Wacht die Netzwerkkarte auch auf Unicast- oder
ARP-Pakete auf, holt schon diese Prüfung den Rechner aus dem Schlaf. In
NetworkManager deshalb `802-3-ethernet.wake-on-lan magic` und nichts
zusätzlich.

**Arbeitsbetrieb.** Läuft auf dem produktiven Linux ebenfalls ein Ollama auf
11434, findet der Proxy es direkt vor und weckt gar nichts. Der Watchdog
existiert nur im AI-Image, der Arbeitsrechner schläft also nicht unter dir weg.

## Fehlersuche

| Symptom | Ansatz |
|---|---|
| Kein Aufwachen | `docker compose logs ai-waker` zeigt jedes gesendete Paket. Gegenprobe vom Host aus mit `wakeonlan`. Sonst BIOS: ErP aus, WoL an. |
| Weckt, aber Timeout | `WAKER_WAKE_TIMEOUT` hoch, im Image prüfen, ob Ollama auf `0.0.0.0` hört. |
| iPXE lädt nichts | `docker compose --profile netboot logs -f dnsmasq-pxe`, dort steht der angefragte Dateiname. Bei "file not found" die Alternativzeile in `dnsmasq/pxe.conf` nehmen. |
| Client sieht keine Modelle | `scripts/smoke-openai.sh` zeigt, ob `/models` live, aus dem Cache oder mit Fehler antwortet. |
| PC wacht ohne Anfrage auf | WoL-Modus der Netzwerkkarte auf `magic` beschränken, siehe oben. |
| Falsches OS startet | `docker compose exec ai-waker wget -qO- http://127.0.0.1:8080/boot.ipxe` zeigt, was die Firmware bekommt. |
| Nach Update kein WoL | In beiden Systemen `nmcli con modify <con> 802-3-ethernet.wake-on-lan magic`. |

## Nächste Schritte

Das AI-Image selbst (NixOS-Netboot mit NVIDIA, Ollama und dem
Idle-Watchdog) ist noch nicht Teil dieses Repos.
