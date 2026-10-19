//go:build linux

package svc

import (
	"fmt"
	"net"
	"strings"

	"kidsroom-agent/internal/config"
	"kidsroom-agent/internal/paths"
)

// EnsureFirewall prints what has to be open instead of opening it.
//
// That is a decision, not a gap: a Linux machine may carry ufw, firewalld,
// nftables rules written by hand, rules pushed by something else entirely, or
// no filtering at all. A tool that guesses wrong here can cut the hub off from
// the very site it was configuring, and undoing that needs physical access. The
// ports are the same as on Windows: the page to everyone, the control API only
// to the allowlist — which the agent enforces itself regardless of the
// firewall.
func EnsureFirewall(logf func(string, ...any)) error {
	cfg, _, err := config.Load(paths.ConfigFile())
	if err != nil {
		return err
	}
	_, controlPort, err := net.SplitHostPort(cfg.Control.Addr)
	if err != nil {
		return fmt.Errorf("control.addr без порта: %w", err)
	}
	remote := routableCIDRs(cfg.Control.AllowCIDRs)

	logf("брандмауэр агент не настраивает. Открыть нужно:")
	logf("  TCP %d — всем (страница трансляции)", cfg.ListenPort)
	if len(remote) == 0 {
		logf("  TCP %s — никому, кроме самой машины (control API)", controlPort)
		return nil
	}
	logf("  TCP %s — только из %s (control API)", controlPort, strings.Join(remote, ", "))
	logf("если на машине ufw:")
	logf("  ufw allow %d/tcp", cfg.ListenPort)
	for _, c := range remote {
		logf("  ufw allow from %s to any port %s proto tcp", c, controlPort)
	}
	return nil
}

// RemoveFirewall has nothing to undo, because EnsureFirewall changed nothing.
func RemoveFirewall(logf func(string, ...any)) error {
	logf("правила брандмауэра агент не создавал — снимать нечего")
	return nil
}

// routableCIDRs drops loopback from the allowlist: traffic to it never reaches
// the firewall, so a rule for it would be noise in an instruction meant to be
// followed literally.
func routableCIDRs(cidrs []string) []string {
	out := make([]string, 0, len(cidrs))
	for _, c := range cidrs {
		ip, _, err := net.ParseCIDR(c)
		if err != nil || ip.IsLoopback() {
			continue
		}
		out = append(out, c)
	}
	return out
}
