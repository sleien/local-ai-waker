package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"strings"
	"time"
)

// A container on a Docker bridge network cannot wake anything on the LAN: Linux
// does not forward directed broadcasts (bc_forwarding=0), the limited broadcast
// never leaves the bridge, and unicast needs an ARP answer a sleeping machine
// does not give. The relay runs with network_mode: host, where the broadcast is
// a plain local send, and the waker reaches it over a unix socket on a shared
// volume. No port is opened.

const relayOK = "ok"

// runRelay answers every connection on socketPath by sending the magic packets
// from its own config. The socket carries no data worth trusting.
func runRelay(ctx context.Context, cfg Config, socketPath string) error {
	if err := os.Remove(socketPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove stale socket: %w", err)
	}
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		return err
	}
	if err := os.Chmod(socketPath, 0o600); err != nil {
		ln.Close()
		return err
	}
	go func() {
		<-ctx.Done()
		ln.Close()
	}()
	log.Printf("wol relay: listening on %s, mac %s, targets %v", socketPath, cfg.MAC, cfg.WOLTargets)

	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		go func(c net.Conn) {
			defer c.Close()
			_ = c.SetDeadline(time.Now().Add(10 * time.Second))
			reply := relayOK
			log.Printf("wol relay: sending magic packet for %s to %v", cfg.MAC, cfg.WOLTargets)
			if err := sendMagicPacket(cfg.MAC, cfg.WOLTargets); err != nil {
				log.Printf("wol relay: %v", err)
				reply = "error: " + err.Error()
			}
			_, _ = fmt.Fprintln(c, reply)
		}(conn)
	}
}

// requestRelay asks the relay to send the magic packets and waits for its answer.
func requestRelay(socketPath string) error {
	conn, err := net.DialTimeout("unix", socketPath, 2*time.Second)
	if err != nil {
		return fmt.Errorf("wol relay unreachable: %w", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))

	reply, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		return fmt.Errorf("wol relay: %w", err)
	}
	if reply = strings.TrimSpace(reply); reply != relayOK {
		return errors.New("wol relay: " + reply)
	}
	return nil
}
