//go:build darwin

package proxy

import (
	"fmt"
	"net"
	"strconv"
	"strings"
)

type proxyEndpointState struct {
	Enabled       bool
	Server        string
	Port          int
	Authenticated bool
	Username      string
	Password      string
}

type autoProxyURLState struct {
	Enabled bool
	URL     string
}

type proxyServiceState struct {
	Service       string
	Web           proxyEndpointState
	Secure        proxyEndpointState
	Socks         proxyEndpointState
	AutoProxyURL  autoProxyURLState
	AutoDiscovery bool
	BypassDomains []string
}

// SystemProxy manages macOS system proxy settings using networksetup.
type SystemProxy struct {
	originalStates map[string]proxyServiceState
	currentProxy   string
}

// NewSystemProxy creates a new SystemProxy instance.
func NewSystemProxy() *SystemProxy {
	return &SystemProxy{
		originalStates: make(map[string]proxyServiceState),
	}
}

// Enable enables web and secure web proxies for all active network services.
func (sp *SystemProxy) Enable(proxyAddress string) error {
	host, portText, err := net.SplitHostPort(proxyAddress)
	if err != nil {
		return fmt.Errorf("invalid proxy address %q: %w", proxyAddress, err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		return fmt.Errorf("invalid proxy port %q: %w", portText, err)
	}

	services, err := listDarwinNetworkServices()
	if err != nil {
		return err
	}
	if len(services) == 0 {
		return fmt.Errorf("no active macOS network services found")
	}

	states := make(map[string]proxyServiceState, len(services))
	commands := make([][]string, 0, len(services)*6)
	for _, service := range services {
		state, err := readDarwinProxyState(service)
		if err != nil {
			return err
		}
		states[service] = state

		commands = append(commands,
			[]string{"/usr/sbin/networksetup", "-setwebproxy", service, host, portText, "off"},
			[]string{"/usr/sbin/networksetup", "-setsecurewebproxy", service, host, portText, "off"},
			[]string{"/usr/sbin/networksetup", "-setsocksfirewallproxystate", service, "off"},
			[]string{"/usr/sbin/networksetup", "-setautoproxystate", service, "off"},
			[]string{"/usr/sbin/networksetup", "-setproxyautodiscovery", service, "off"},
			[]string{"/usr/sbin/networksetup", "-setproxybypassdomains", service, "localhost", "127.0.0.1", "::1"},
		)
	}

	if err := runDarwinPrivilegedCommands(commands...); err != nil {
		return err
	}

	sp.originalStates = states
	sp.currentProxy = net.JoinHostPort(host, strconv.Itoa(port))
	return nil
}

// Disable restores the previous proxy state when possible.
func (sp *SystemProxy) Disable() error {
	if len(sp.originalStates) == 0 {
		sp.currentProxy = ""
		return nil
	}

	commands := make([][]string, 0, len(sp.originalStates)*8)
	for _, state := range sp.originalStates {
		commands = append(commands, buildRestoreCommands(state)...)
	}

	if err := runDarwinPrivilegedCommands(commands...); err != nil {
		return err
	}

	sp.originalStates = make(map[string]proxyServiceState)
	sp.currentProxy = ""
	return nil
}

// IsEnabled returns true when any active network service has our proxy enabled.
func (sp *SystemProxy) IsEnabled() bool {
	services, err := listDarwinNetworkServices()
	if err != nil {
		return false
	}

	for _, service := range services {
		state, err := readDarwinProxyState(service)
		if err != nil {
			continue
		}
		if state.Web.Enabled || state.Secure.Enabled || state.Socks.Enabled {
			return true
		}
	}

	return false
}

// GetCurrentProxy returns the first enabled proxy endpoint discovered.
func (sp *SystemProxy) GetCurrentProxy() string {
	services, err := listDarwinNetworkServices()
	if err != nil {
		return ""
	}

	for _, service := range services {
		state, err := readDarwinProxyState(service)
		if err != nil {
			continue
		}
		for _, endpoint := range []proxyEndpointState{state.Web, state.Secure, state.Socks} {
			if endpoint.Enabled && endpoint.Server != "" && endpoint.Port > 0 {
				return net.JoinHostPort(endpoint.Server, strconv.Itoa(endpoint.Port))
			}
		}
	}

	return ""
}

func buildRestoreCommands(state proxyServiceState) [][]string {
	commands := make([][]string, 0, 12)
	commands = append(commands, buildRestoreEndpointCommands(
		state.Service,
		state.Web,
		"-setwebproxy",
		"-setwebproxystate",
	)...)
	commands = append(commands, buildRestoreEndpointCommands(
		state.Service,
		state.Secure,
		"-setsecurewebproxy",
		"-setsecurewebproxystate",
	)...)
	commands = append(commands, buildRestoreEndpointCommands(
		state.Service,
		state.Socks,
		"-setsocksfirewallproxy",
		"-setsocksfirewallproxystate",
	)...)
	commands = append(commands, buildRestoreAutoProxyURLCommands(state.Service, state.AutoProxyURL)...)

	if state.AutoDiscovery {
		commands = append(commands, []string{"/usr/sbin/networksetup", "-setproxyautodiscovery", state.Service, "on"})
	} else {
		commands = append(commands, []string{"/usr/sbin/networksetup", "-setproxyautodiscovery", state.Service, "off"})
	}

	if len(state.BypassDomains) == 0 {
		commands = append(commands, []string{"/usr/sbin/networksetup", "-setproxybypassdomains", state.Service, "Empty"})
	} else {
		command := []string{"/usr/sbin/networksetup", "-setproxybypassdomains", state.Service}
		command = append(command, state.BypassDomains...)
		commands = append(commands, command)
	}

	return commands
}

func buildRestoreEndpointCommands(service string, endpoint proxyEndpointState, setCommand, stateCommand string) [][]string {
	if endpoint.Server == "" || endpoint.Port <= 0 {
		return [][]string{{"/usr/sbin/networksetup", stateCommand, service, "off"}}
	}

	command := []string{
		"/usr/sbin/networksetup",
		setCommand,
		service,
		endpoint.Server,
		strconv.Itoa(endpoint.Port),
		darwinOnOff(endpoint.Authenticated),
	}
	if endpoint.Authenticated && endpoint.Username != "" && endpoint.Password != "" {
		command = append(command, endpoint.Username, endpoint.Password)
	}

	return [][]string{
		command,
		{"/usr/sbin/networksetup", stateCommand, service, darwinOnOff(endpoint.Enabled)},
	}
}

func buildRestoreAutoProxyURLCommands(service string, state autoProxyURLState) [][]string {
	if state.URL == "" {
		return [][]string{{"/usr/sbin/networksetup", "-setautoproxystate", service, "off"}}
	}

	return [][]string{
		{"/usr/sbin/networksetup", "-setautoproxyurl", service, state.URL},
		{"/usr/sbin/networksetup", "-setautoproxystate", service, darwinOnOff(state.Enabled)},
	}
}

func darwinOnOff(enabled bool) string {
	if enabled {
		return "on"
	}
	return "off"
}

func listDarwinNetworkServices() ([]string, error) {
	output, err := runDarwinCommand("/usr/sbin/networksetup", "-listallnetworkservices")
	if err != nil {
		return nil, fmt.Errorf("failed to enumerate network services: %w", err)
	}
	return parseDarwinNetworkServices(output), nil
}

func readDarwinProxyState(service string) (proxyServiceState, error) {
	webOutput, err := runDarwinCommand("/usr/sbin/networksetup", "-getwebproxy", service)
	if err != nil {
		return proxyServiceState{}, fmt.Errorf("failed to read web proxy for %s: %w", service, err)
	}
	secureOutput, err := runDarwinCommand("/usr/sbin/networksetup", "-getsecurewebproxy", service)
	if err != nil {
		return proxyServiceState{}, fmt.Errorf("failed to read secure web proxy for %s: %w", service, err)
	}
	socksOutput, err := runDarwinCommand("/usr/sbin/networksetup", "-getsocksfirewallproxy", service)
	if err != nil {
		return proxyServiceState{}, fmt.Errorf("failed to read SOCKS proxy for %s: %w", service, err)
	}
	autoProxyOutput, err := runDarwinCommand("/usr/sbin/networksetup", "-getautoproxyurl", service)
	if err != nil {
		return proxyServiceState{}, fmt.Errorf("failed to read auto proxy URL for %s: %w", service, err)
	}
	autoDiscoveryOutput, err := runDarwinCommand("/usr/sbin/networksetup", "-getproxyautodiscovery", service)
	if err != nil {
		return proxyServiceState{}, fmt.Errorf("failed to read auto discovery for %s: %w", service, err)
	}
	bypassOutput, err := runDarwinCommand("/usr/sbin/networksetup", "-getproxybypassdomains", service)
	if err != nil {
		return proxyServiceState{}, fmt.Errorf("failed to read bypass domains for %s: %w", service, err)
	}

	return proxyServiceState{
		Service:       service,
		Web:           parseDarwinProxyEndpoint(webOutput),
		Secure:        parseDarwinProxyEndpoint(secureOutput),
		Socks:         parseDarwinProxyEndpoint(socksOutput),
		AutoProxyURL:  parseDarwinAutoProxyURL(autoProxyOutput),
		AutoDiscovery: parseDarwinAutoDiscovery(autoDiscoveryOutput),
		BypassDomains: parseDarwinBypassDomains(bypassOutput),
	}, nil
}

func parseDarwinNetworkServices(output string) []string {
	lines := strings.Split(output, "\n")
	services := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		switch {
		case line == "":
			continue
		case strings.HasPrefix(line, "An asterisk"):
			continue
		case strings.HasPrefix(line, "*"):
			continue
		default:
			services = append(services, line)
		}
	}
	return services
}

func parseDarwinProxyEndpoint(output string) proxyEndpointState {
	values := parseDarwinKeyValueOutput(output)
	return proxyEndpointState{
		Enabled:       parseDarwinBool(values["Enabled"]),
		Server:        values["Server"],
		Port:          parseDarwinPort(values["Port"]),
		Authenticated: parseDarwinBool(values["Authenticated Proxy Enabled"]),
		Username:      firstDarwinValue(values, "Username", "User", "Authenticated Proxy Username"),
		Password:      firstDarwinValue(values, "Password", "Authenticated Proxy Password"),
	}
}

func parseDarwinAutoProxyURL(output string) autoProxyURLState {
	values := parseDarwinKeyValueOutput(output)
	return autoProxyURLState{
		Enabled: parseDarwinBool(values["Enabled"]),
		URL:     values["URL"],
	}
}

func parseDarwinAutoDiscovery(output string) bool {
	values := parseDarwinKeyValueOutput(output)
	for _, key := range []string{"Auto Proxy Discovery", "Enabled"} {
		if value, ok := values[key]; ok {
			return parseDarwinBool(value)
		}
	}
	return false
}

func parseDarwinBypassDomains(output string) []string {
	lines := strings.Split(output, "\n")
	domains := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		lower := strings.ToLower(line)
		if strings.Contains(lower, "there aren't any bypass domains") {
			return nil
		}
		domains = append(domains, line)
	}
	return domains
}

func parseDarwinKeyValueOutput(output string) map[string]string {
	result := map[string]string{}
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])
		result[key] = value
	}
	return result
}

func firstDarwinValue(values map[string]string, keys ...string) string {
	for _, key := range keys {
		if value := values[key]; value != "" {
			return value
		}
	}
	return ""
}

func parseDarwinBool(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "yes", "on", "1", "true":
		return true
	default:
		return false
	}
}

func parseDarwinPort(value string) int {
	port, _ := strconv.Atoi(strings.TrimSpace(value))
	return port
}
