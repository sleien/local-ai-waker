# netboot

The kernel, the initrd and `boot-ai.ipxe` live here. The directory is mounted
read-only into the container and served at `/netboot/`. Everything except this
file and the example script is git-ignored, the images are too big for a repo.

Expected layout after a build:

```
netboot/
├── boot-ai.ipxe   # copied from boot-ai.ipxe.example, {{BASE}} kept as is
├── bzImage
└── initrd
```

Check what the workstation will actually receive:

```bash
curl http://localhost:8080/boot.ipxe
```

## Building the image with NixOS

`nixos-generators -f netboot` or a flake output based on
`modules/installer/netboot/netboot-minimal.nix` produces exactly these files
plus a `netboot.ipxe`. The `init=/nix/store/...` path in that generated script
changes with every rebuild, so copy the new one into `boot-ai.ipxe` each time.

The image needs: NVIDIA driver (open kernel modules) plus CUDA userspace,
Ollama listening on `0.0.0.0:11434` with `OLLAMA_KEEP_ALIVE=5m`, the idle
watchdog, sshd, and a mount for the model partition on the local NVMe. Never
pull models over the network at boot: 20 GB over 2.5 GbE takes over a minute,
from NVMe it takes seconds.
