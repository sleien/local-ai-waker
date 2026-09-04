package main

import (
	"context"
	"fmt"
	"net"
	"strings"
	"syscall"
	"time"
)

// sendMagicPacket sends the WoL payload to every configured target. Broadcast
// needs SO_BROADCAST, which Go does not set on its own, hence the ListenConfig.
func sendMagicPacket(mac net.HardwareAddr, targets []string) error {
	packet := make([]byte, 0, 102)
	for i := 0; i < 6; i++ {
		packet = append(packet, 0xFF)
	}
	for i := 0; i < 16; i++ {
		packet = append(packet, mac...)
	}

	lc := net.ListenConfig{
		Control: func(_, _ string, c syscall.RawConn) error {
			var sockErr error
			err := c.Control(func(fd uintptr) {
				sockErr = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_BROADCAST, 1)
			})
			if err != nil {
				return err
			}
			return sockErr
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, err := lc.ListenPacket(ctx, "udp4", ":0")
	if err != nil {
		return fmt.Errorf("open udp socket: %w", err)
	}
	defer conn.Close()

	var failures []string
	sent := 0
	for _, target := range targets {
		addr, err := net.ResolveUDPAddr("udp4", target)
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", target, err))
			continue
		}
		if _, err := conn.WriteTo(packet, addr); err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", target, err))
			continue
		}
		sent++
	}

	if sent == 0 {
		return fmt.Errorf("no magic packet sent (%s)", strings.Join(failures, "; "))
	}
	if len(failures) > 0 {
		return fmt.Errorf("partial send, %d ok, failures: %s", sent, strings.Join(failures, "; "))
	}
	return nil
}
