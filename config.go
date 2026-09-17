package main

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	ModeAI   = "ai"
	ModeWork = "work"
)

// Config holds everything the waker needs, sourced from environment variables.
type Config struct {
	Listen      string // LAN listener: iPXE boot script, netboot files, AI proxy
	AdminListen string // internal listener: status, mode switch, wake button

	TargetHost string // workstation address
	TargetPort int    // Ollama port on the workstation

	MAC        net.HardwareAddr
	WOLTargets []string // pre-built "host:port" list for the magic packets

	WakeTimeout   time.Duration // give up if the box is not up by then
	ProbeInterval time.Duration // TCP probe cadence while waking
	ProbeTimeout  time.Duration // per-probe dial timeout
	WOLRepeat     time.Duration // resend the magic packet this often

	StateFile      string
	CatalogFile    string
	CatalogRefresh time.Duration // how often to re-read the model list while online

	NetbootDir  string
	NetbootBase string // URL prefix iPXE uses to fetch kernel/initrd

	APIKey     string // optional bearer token for the AI proxy
	AdminToken string // optional token for the admin listener

	DefaultMode    string
	ResetAfterBoot bool // after serving a "work" boot, fall back to ai mode
}

func loadConfig() (Config, error) {
	c := Config{
		Listen:         env("WAKER_LISTEN", ":8080"),
		AdminListen:    env("WAKER_ADMIN_LISTEN", ":8081"),
		TargetHost:     env("WAKER_TARGET_HOST", ""),
		StateFile:      env("WAKER_STATE_FILE", "/data/state.json"),
		CatalogFile:    env("WAKER_CATALOG_FILE", "/data/catalog.json"),
		NetbootDir:     env("WAKER_NETBOOT_DIR", "/netboot"),
		NetbootBase:    strings.TrimRight(env("WAKER_NETBOOT_BASE_URL", ""), "/"),
		APIKey:         env("WAKER_API_KEY", ""),
		AdminToken:     env("WAKER_ADMIN_TOKEN", ""),
		DefaultMode:    env("WAKER_DEFAULT_MODE", ModeAI),
		ResetAfterBoot: envBool("WAKER_RESET_MODE_AFTER_BOOT", true),
	}

	if c.TargetHost == "" {
		return c, fmt.Errorf("WAKER_TARGET_HOST is required")
	}
	if c.DefaultMode != ModeAI && c.DefaultMode != ModeWork {
		return c, fmt.Errorf("WAKER_DEFAULT_MODE must be %q or %q", ModeAI, ModeWork)
	}

	var err error
	if c.TargetPort, err = envInt("WAKER_TARGET_PORT", 11434); err != nil {
		return c, err
	}
	if c.MAC, err = net.ParseMAC(env("WAKER_MAC", "")); err != nil {
		return c, fmt.Errorf("WAKER_MAC: %w", err)
	}
	if c.WakeTimeout, err = envDur("WAKER_WAKE_TIMEOUT", 3*time.Minute); err != nil {
		return c, err
	}
	if c.ProbeInterval, err = envDur("WAKER_PROBE_INTERVAL", 2*time.Second); err != nil {
		return c, err
	}
	if c.ProbeTimeout, err = envDur("WAKER_PROBE_TIMEOUT", 2*time.Second); err != nil {
		return c, err
	}
	if c.WOLRepeat, err = envDur("WAKER_WOL_REPEAT", 15*time.Second); err != nil {
		return c, err
	}
	if c.CatalogRefresh, err = envDur("WAKER_CATALOG_REFRESH", 5*time.Minute); err != nil {
		return c, err
	}

	// Magic packets go to every broadcast address x every port. Unicast
	// addresses work too (useful when a static ARP entry exists).
	hosts := splitList(env("WAKER_WOL_TARGETS", "255.255.255.255"))
	ports := splitList(env("WAKER_WOL_PORTS", "9,7"))
	if len(hosts) == 0 || len(ports) == 0 {
		return c, fmt.Errorf("WAKER_WOL_TARGETS and WAKER_WOL_PORTS must not be empty")
	}
	for _, h := range hosts {
		for _, p := range ports {
			c.WOLTargets = append(c.WOLTargets, net.JoinHostPort(h, p))
		}
	}

	if c.NetbootBase == "" {
		c.NetbootBase = fmt.Sprintf("http://%s%s/netboot", env("WAKER_SERVER_IP", "127.0.0.1"), c.Listen)
	}
	return c, nil
}

func (c Config) TargetAddr() string {
	return net.JoinHostPort(c.TargetHost, strconv.Itoa(c.TargetPort))
}

func env(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	return def
}

func envInt(key string, def int) (int, error) {
	raw := env(key, "")
	if raw == "" {
		return def, nil
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	return v, nil
}

func envDur(key string, def time.Duration) (time.Duration, error) {
	raw := env(key, "")
	if raw == "" {
		return def, nil
	}
	v, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	return v, nil
}

func envBool(key string, def bool) bool {
	raw := env(key, "")
	if raw == "" {
		return def
	}
	v, err := strconv.ParseBool(raw)
	if err != nil {
		return def
	}
	return v
}

func splitList(raw string) []string {
	var out []string
	for _, part := range strings.Split(raw, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}
