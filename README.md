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

Zwei Listener, damit die Steuerung nicht am LAN-Port hängt.

| Listener | Port | Endpunkt | Zweck |
|---|---|---|---|
| LAN | 8080 | `/boot.ipxe` | iPXE-Skript je nach Modus, ohne Auth (Firmware kann kein TLS) |
| LAN | 8080 | `/netboot/*` | Kernel und initrd |
| LAN | 8080 | alles andere | Reverse Proxy auf Ollama, weckt bei Bedarf |
| LAN | 8080 | `/healthz` | Liveness |
| Admin | 8081 | `/` | kleine Weboberfläche mit den Start-Buttons |
| Admin | 8081 | `/status` | JSON für Homepage |
| Admin | 8081 | `/mode` | GET liest, POST setzt `ai` oder `work` |
| Admin | 8081 | `/wake` | POST: Modus setzen und Magic Packet senden |

Port 8081 wird bewusst **nicht** im Compose veröffentlicht. Erreichbar ist er
über Traefik (mit Authentik davor) und über das Docker-Netz, etwa für
Homepage.

## Setup

```bash
cp .env.example .env
# WAKER_TARGET_HOST, WAKER_MAC, WAKER_WOL_TARGETS, DOMAIN eintragen
docker compose up -d --build
```

Test ohne Netboot, nur Wecken und Weiterleiten:

```bash
curl -X POST "http://localhost:8080/api/generate" -d '{"model":"llama3.1","prompt":"hi"}'
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

**Timeouts.** Ein Kaltstart dauert länger als jeder Standard-Timeout. In der
statischen Traefik-Konfiguration:

```yaml
entryPoints:
  websecure:
    transport:
      respondingTimeouts:
        readTimeout: 0
        writeTimeout: 0
        idleTimeout: 180s
```

`FlushInterval: -1` im Proxy sorgt dafür, dass Streaming-Antworten sofort
durchgereicht werden statt gepuffert.

**Auth.** `ai.$DOMAIN` läuft ohne Authentik, weil API-Clients keinen
Browser-Login machen können; stattdessen `WAKER_API_KEY` setzen und als
`Authorization: Bearer ...` oder `X-Api-Key` mitschicken. `waker.$DOMAIN`
liegt hinter Authentik. Wer zusätzlich `WAKER_ADMIN_TOKEN` setzt, lässt es von
Traefik injizieren, siehe die auskommentierte Middleware in
`docker-compose.yml`.

**Arbeitsbetrieb.** Läuft auf dem produktiven Linux ebenfalls ein Ollama auf
11434, findet der Proxy es direkt vor und weckt gar nichts. Der Watchdog
existiert nur im AI-Image, der Arbeitsrechner schläft also nicht unter dir weg.

## Fehlersuche

| Symptom | Ansatz |
|---|---|
| Kein Aufwachen | `docker compose logs ai-waker` zeigt jedes gesendete Paket. Gegenprobe vom Host aus mit `wakeonlan`. Sonst BIOS: ErP aus, WoL an. |
| Weckt, aber Timeout | `WAKER_WAKE_TIMEOUT` hoch, im Image prüfen, ob Ollama auf `0.0.0.0` hört. |
| iPXE lädt nichts | `docker compose --profile netboot logs -f dnsmasq-pxe`, dort steht der angefragte Dateiname. Bei "file not found" die Alternativzeile in `dnsmasq/pxe.conf` nehmen. |
| Falsches OS startet | `curl http://localhost:8080/boot.ipxe` zeigt, was die Firmware bekommt. |
| Nach Update kein WoL | In beiden Systemen `nmcli con modify <con> 802-3-ethernet.wake-on-lan magic`. |

## Nächste Schritte

Das AI-Image selbst (NixOS-Netboot mit NVIDIA, Ollama und dem
Idle-Watchdog) ist noch nicht Teil dieses Repos.
