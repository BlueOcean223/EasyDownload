//go:build darwin

package proxy

import (
	"strings"
	"testing"
)

func TestParseDarwinNetworkServices(t *testing.T) {
	output := `
An asterisk (*) denotes that a network service is disabled.
Wi-Fi
USB 10/100/1000 LAN
*Thunderbolt Bridge
`

	services := parseDarwinNetworkServices(output)
	if len(services) != 2 {
		t.Fatalf("expected 2 services, got %d", len(services))
	}
	if services[0] != "Wi-Fi" || services[1] != "USB 10/100/1000 LAN" {
		t.Fatalf("unexpected services: %#v", services)
	}
}

func TestParseDarwinProxyEndpoint(t *testing.T) {
	output := `
Enabled: Yes
Server: 127.0.0.1
Port: 8899
Authenticated Proxy Enabled: 0
`

	state := parseDarwinProxyEndpoint(output)
	if !state.Enabled {
		t.Fatal("expected endpoint to be enabled")
	}
	if state.Server != "127.0.0.1" {
		t.Fatalf("unexpected server: %q", state.Server)
	}
	if state.Port != 8899 {
		t.Fatalf("unexpected port: %d", state.Port)
	}
}

func TestParseDarwinBypassDomains(t *testing.T) {
	output := `
localhost
127.0.0.1
::1
`

	domains := parseDarwinBypassDomains(output)
	if len(domains) != 3 {
		t.Fatalf("expected 3 domains, got %d", len(domains))
	}
}

func TestParseDarwinAutoDiscovery(t *testing.T) {
	if !parseDarwinAutoDiscovery("Auto Proxy Discovery: On") {
		t.Fatal("expected auto discovery to be enabled")
	}
	if parseDarwinAutoDiscovery("Auto Proxy Discovery: Off") {
		t.Fatal("expected auto discovery to be disabled")
	}
}

func TestDisableWithoutOriginalStateIsNoop(t *testing.T) {
	sp := NewSystemProxy()
	sp.currentProxy = "127.0.0.1:8899"

	if err := sp.Disable(); err != nil {
		t.Fatalf("Disable returned error: %v", err)
	}
	if sp.currentProxy != "" {
		t.Fatalf("expected current proxy to be cleared, got %q", sp.currentProxy)
	}
}

func TestBuildDarwinPrivilegedScriptUsesFailFastChaining(t *testing.T) {
	script := buildDarwinPrivilegedScript(
		[]string{"/usr/sbin/networksetup", "-setwebproxystate", "Wi-Fi", "off"},
		[]string{"/usr/sbin/networksetup", "-setsecurewebproxystate", "Wi-Fi", "off"},
	)

	if script == "" {
		t.Fatal("expected script")
	}
	if !strings.Contains(script, " && ") {
		t.Fatalf("expected fail-fast command chaining, got %q", script)
	}
	if strings.Contains(script, " ; ") {
		t.Fatalf("expected no semicolon chaining, got %q", script)
	}
}
