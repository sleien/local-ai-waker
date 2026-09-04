#!/usr/bin/env bash
# Downloads the iPXE binaries the proxy-DHCP service hands out via TFTP.
set -euo pipefail

dest="$(cd "$(dirname "$0")/.." && pwd)/tftp"
mkdir -p "$dest"

fetch() {
	local url="$1" out="$2"
	echo "-> $out"
	curl -fSL --retry 3 -o "$dest/$out" "$url"
}

# snponly.efi uses the firmware's own NIC driver and is the safest choice on
# modern boards; ipxe.efi carries its own drivers, keep it as a fallback.
fetch https://boot.ipxe.org/snponly.efi snponly.efi
fetch https://boot.ipxe.org/ipxe.efi ipxe.efi
fetch https://boot.ipxe.org/undionly.kpxe undionly.kpxe

echo
echo "Files in $dest:"
ls -la "$dest"
echo
echo "If ipxe.efi does not get a link on the RTL8126, point pxe.conf at snponly.efi."
