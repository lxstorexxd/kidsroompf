package svc

import (
	"fmt"
	"net"
	"os/exec"
	"strconv"
	"strings"

	"kidsroom-agent/internal/config"
	"kidsroom-agent/internal/paths"
)

// Rule names, used both to create and to remove the rules.
const (
	rulePublic  = "kidsroom-agent: страница трансляции"
	ruleControl = "kidsroom-agent: control API"
)

// EnsureFirewall opens the public port to everyone and the control port only to
// the local subnet and the allowed CIDRs. The two rules are rebuilt from
// scratch so a settings change cannot leave a stale wider rule behind.
func EnsureFirewall(logf func(string, ...any)) error {
	cfg, _, err := config.Load(paths.ConfigFile())
	if err != nil {
		return err
	}

	_, controlPort, err := net.SplitHostPort(cfg.Control.Addr)
	if err != nil {
		return fmt.Errorf("control.addr без порта: %w", err)
	}

	removeRule(rulePublic)
	removeRule(ruleControl)

	if err := addRule(rulePublicArgs(cfg.ListenPort)); err != nil {
		return err
	}
	logf("брандмауэр: TCP %d открыт для всех (страница трансляции)", cfg.ListenPort)

	remote := remoteIPList(cfg.Control.AllowCIDRs)
	if err := addRule(ruleControlArgs(controlPort, remote)); err != nil {
		return err
	}
	logf("брандмауэр: TCP %s открыт только для %s (control API)", controlPort, remote)
	return nil
}

// RemoveFirewall deletes both rules.
func RemoveFirewall(logf func(string, ...any)) error {
	removeRule(rulePublic)
	removeRule(ruleControl)
	logf("правила брандмауэра удалены")
	return nil
}

func rulePublicArgs(port int) []string {
	return []string{
		"advfirewall", "firewall", "add", "rule",
		"name=" + rulePublic,
		"dir=in", "action=allow", "protocol=TCP",
		"localport=" + strconv.Itoa(port),
		"profile=any",
		"description=Публичная страница трансляции детской (kidsroom-agent)",
	}
}

func ruleControlArgs(port, remote string) []string {
	return []string{
		"advfirewall", "firewall", "add", "rule",
		"name=" + ruleControl,
		"dir=in", "action=allow", "protocol=TCP",
		"localport=" + port,
		"remoteip=" + remote,
		"profile=any",
		"description=API настроек kidsroom-agent, только локальная сеть и VPN",
	}
}

// remoteIPList turns the allowlist into a netsh remoteip value. Loopback is
// dropped (it never crosses the firewall) and LocalSubnet is always included,
// so the rule keeps working when the VPN hands out a different range.
func remoteIPList(cidrs []string) string {
	out := []string{"LocalSubnet"}
	for _, c := range cidrs {
		ip, _, err := net.ParseCIDR(c)
		if err != nil || ip.IsLoopback() {
			continue
		}
		out = append(out, c)
	}
	return strings.Join(out, ",")
}

func addRule(args []string) error {
	cmd := exec.Command("netsh", args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("netsh: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func removeRule(name string) {
	_ = exec.Command("netsh", "advfirewall", "firewall", "delete", "rule", "name="+name).Run()
}
