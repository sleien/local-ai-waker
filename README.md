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
`Authorization: Bearer ...` oder `X-Api-Key` mitschicken. Die Router-Regel
dort deckt nur `/api` und `/v1` ab, das Boot-Skript ist über den öffentlichen
Namen also nicht abrufbar. `waker.$DOMAIN`
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
| Falsches OS startet | `docker compose exec ai-waker wget -qO- http://127.0.0.1:8080/boot.ipxe` zeigt, was die Firmware bekommt. |
| Nach Update kein WoL | In beiden Systemen `nmcli con modify <con> 802-3-ethernet.wake-on-lan magic`. |

## Nächste Schritte

Das AI-Image selbst (NixOS-Netboot mit NVIDIA, Ollama und dem
Idle-Watchdog) ist noch nicht Teil dieses Repos.
