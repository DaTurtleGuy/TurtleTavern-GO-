package config

import (
	"flag"
	"strconv"
	"strings"
)

// CLIOptions holds the command-line flag values that override config.yaml and env vars.
// Flag names match src/command-line.js exactly.
type CLIOptions struct {
	Global                      *bool
	ConfigPath                  string
	DataRoot                    string
	Port                        int
	Listen                      *bool
	EnableIPv6                  string
	EnableIPv4                  string
	DNSPreferIPv6               *bool
	SSL                         *bool
	CertPath                    string
	KeyPath                     string
	KeyPassphrase               string
	Whitelist                   *bool
	BasicAuthMode               *bool
	CorsProxy                   *bool
	DisableCsrf                 *bool
	ListenAddressIPv4           string
	ListenAddressIPv6           string
	HeartbeatInterval           int
	RequestProxyEnabled         *bool
	RequestProxyURL             string
	RequestProxyBypass          string
	SecurityOverride            *bool
	BrowserLaunchEnabled        *bool
	BrowserLaunchHostname       string
	BrowserLaunchPort           int
	BrowserLaunchAvoidLocalhost *bool
	Autorun                     *bool
	AutorunHostname             string
	AutorunPortOverride         int
	AvoidLocalhost              *bool
	parsedEnableIPv6            bool
	parsedEnableIPv4            bool
	parsedDNSPreferIPv6         bool
	parsedSSL                   bool
	parsedWhitelist             bool
	parsedBasicAuth             bool
	parsedCorsProxy             bool
	parsedDisableCsrf           bool
	parsedListen                bool
	parsedProxyEnabled          bool
	parsedSecurityOverride      bool
	parsedBrowserEnabled        bool
	parsedBrowserAvoid          bool
	parsedAutorun               bool
	parsedAvoidLocalhost        bool
	parsedGlobal                bool
}

// ParseCLI parses command-line flags matching the Node.js CLI and returns CLIOptions.
func ParseCLI() *CLIOptions {
	opts := &CLIOptions{}

	global := flag.Bool("global", false, "Use global data and config paths instead of the server directory")
	opts.Global = global
	opts.parsedGlobal = true
	flag.StringVar(&opts.ConfigPath, "configPath", "", "Path to the config file (only for standalone mode)")
	flag.StringVar(&opts.EnableIPv6, "enableIPv6", "", "Enables IPv6 protocol")
	flag.StringVar(&opts.EnableIPv4, "enableIPv4", "", "Enables IPv4 protocol")
	flag.IntVar(&opts.Port, "port", 0, "Server port")
	dnsPrefer := flag.Bool("dnsPreferIPv6", false, "Prefers IPv6 for DNS")
	opts.DNSPreferIPv6 = dnsPrefer
	opts.parsedDNSPreferIPv6 = true
	browserEnabled := flag.Bool("browserLaunchEnabled", false, "Automatically launch TurtleTavern (Go) in the browser")
	opts.BrowserLaunchEnabled = browserEnabled
	opts.parsedBrowserEnabled = true
	flag.StringVar(&opts.BrowserLaunchHostname, "browserLaunchHostname", "", "Sets the browser launch hostname")
	flag.IntVar(&opts.BrowserLaunchPort, "browserLaunchPort", 0, "Overrides the port for browser launch")
	browserAvoid := flag.Bool("browserLaunchAvoidLocalhost", false, "Avoids using 'localhost' for browser launch")
	opts.BrowserLaunchAvoidLocalhost = browserAvoid
	opts.parsedBrowserAvoid = true
	listen := flag.Bool("listen", false, "Whether to listen on all network interfaces")
	opts.Listen = listen
	opts.parsedListen = true
	flag.StringVar(&opts.ListenAddressIPv6, "listenAddressIPv6", "", "Specific IPv6 address to listen to")
	flag.StringVar(&opts.ListenAddressIPv4, "listenAddressIPv4", "", "Specific IPv4 address to listen to")
	corsProxy := flag.Bool("corsProxy", false, "Enables CORS proxy")
	opts.CorsProxy = corsProxy
	opts.parsedCorsProxy = true
	disableCsrf := flag.Bool("disableCsrf", false, "Disables CSRF protection - NOT RECOMMENDED")
	opts.DisableCsrf = disableCsrf
	opts.parsedDisableCsrf = true
	ssl := flag.Bool("ssl", false, "Enables SSL")
	opts.SSL = ssl
	opts.parsedSSL = true
	flag.StringVar(&opts.CertPath, "certPath", "", "Path to SSL certificate file")
	flag.StringVar(&opts.KeyPath, "keyPath", "", "Path to SSL private key file")
	flag.StringVar(&opts.KeyPassphrase, "keyPassphrase", "", "Passphrase for the SSL private key")
	whitelist := flag.Bool("whitelist", false, "Enables whitelist mode")
	opts.Whitelist = whitelist
	opts.parsedWhitelist = true
	flag.StringVar(&opts.DataRoot, "dataRoot", "", "Root directory for data storage (only for standalone mode)")
	basicAuth := flag.Bool("basicAuthMode", false, "Enables basic authentication")
	opts.BasicAuthMode = basicAuth
	opts.parsedBasicAuth = true
	proxyEnabled := flag.Bool("requestProxyEnabled", false, "Enables a use of proxy for outgoing requests")
	opts.RequestProxyEnabled = proxyEnabled
	opts.parsedProxyEnabled = true
	flag.StringVar(&opts.RequestProxyURL, "requestProxyUrl", "", "Request proxy URL (HTTP or SOCKS protocols)")
	flag.StringVar(&opts.RequestProxyBypass, "requestProxyBypass", "", "Request proxy bypass list (space separated list of hosts)")
	flag.IntVar(&opts.HeartbeatInterval, "heartbeatInterval", 0, "Heartbeat interval in seconds")
	securityOverride := flag.Bool("securityOverride", false, "Disable startup security checks")
	opts.SecurityOverride = securityOverride
	opts.parsedSecurityOverride = true
	autorun := flag.Bool("autorun", false, "DEPRECATED: Use browserLaunchEnabled instead")
	opts.Autorun = autorun
	opts.parsedAutorun = true
	flag.StringVar(&opts.AutorunHostname, "autorunHostname", "", "DEPRECATED: Use browserLaunchHostname instead")
	flag.IntVar(&opts.AutorunPortOverride, "autorunPortOverride", 0, "DEPRECATED: Use browserLaunchPort instead")
	avoidLocalhost := flag.Bool("avoidLocalhost", false, "DEPRECATED: Use browserLaunchAvoidLocalhost instead")
	opts.AvoidLocalhost = avoidLocalhost
	opts.parsedAvoidLocalhost = true

	flag.Parse()

	visited := map[string]bool{}
	flag.Visit(func(f *flag.Flag) { visited[f.Name] = true })
	opts.parsedEnableIPv6 = visited["enableIPv6"]
	opts.parsedEnableIPv4 = visited["enableIPv4"]
	if !visited["dnsPreferIPv6"] {
		opts.parsedDNSPreferIPv6 = false
	}
	if !visited["ssl"] {
		opts.parsedSSL = false
	}
	if !visited["whitelist"] {
		opts.parsedWhitelist = false
	}
	if !visited["basicAuthMode"] {
		opts.parsedBasicAuth = false
	}
	if !visited["corsProxy"] {
		opts.parsedCorsProxy = false
	}
	if !visited["disableCsrf"] {
		opts.parsedDisableCsrf = false
	}
	if !visited["listen"] {
		opts.parsedListen = false
	}
	if !visited["requestProxyEnabled"] {
		opts.parsedProxyEnabled = false
	}
	if !visited["securityOverride"] {
		opts.parsedSecurityOverride = false
	}
	if !visited["browserLaunchEnabled"] {
		opts.parsedBrowserEnabled = false
	}
	if !visited["browserLaunchAvoidLocalhost"] {
		opts.parsedBrowserAvoid = false
	}
	if !visited["autorun"] {
		opts.parsedAutorun = false
	}
	if !visited["avoidLocalhost"] {
		opts.parsedAvoidLocalhost = false
	}
	if !visited["global"] {
		opts.parsedGlobal = false
	}
	return opts
}

func parseBoolAuto(s string) (any, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "true", "1", "yes":
		return true, true
	case "false", "0", "no":
		return false, true
	case "auto":
		return "auto", true
	default:
		return nil, false
	}
}

// ApplyCLI applies CLI flag values to the Config.
// Only explicitly provided flags override existing config, matching Node's ?? semantics.
func ApplyCLI(cfg *Config, opts *CLIOptions) {
	if opts.DataRoot != "" {
		cfg.DataRoot = opts.DataRoot
	}
	if opts.Port != 0 {
		cfg.Port = opts.Port
	}
	if opts.parsedListen && *opts.Listen {
		cfg.Listen = true
	}
	if opts.parsedEnableIPv6 {
		if v, ok := parseBoolAuto(opts.EnableIPv6); ok {
			cfg.Protocol.IPv6 = v
		}
	}
	if opts.parsedEnableIPv4 {
		if v, ok := parseBoolAuto(opts.EnableIPv4); ok {
			cfg.Protocol.IPv4 = v
		}
	}
	if opts.parsedDNSPreferIPv6 && *opts.DNSPreferIPv6 {
		cfg.DNSPreferIPv6 = true
	}
	if opts.parsedSSL && *opts.SSL {
		cfg.SSL.Enabled = true
	}
	if opts.CertPath != "" {
		cfg.SSL.CertPath = opts.CertPath
	}
	if opts.KeyPath != "" {
		cfg.SSL.KeyPath = opts.KeyPath
	}
	if opts.KeyPassphrase != "" {
		cfg.SSL.KeyPassphrase = opts.KeyPassphrase
	}
	if opts.parsedWhitelist && *opts.Whitelist {
		cfg.WhitelistMode = true
	}
	if opts.parsedBasicAuth && *opts.BasicAuthMode {
		cfg.BasicAuthMode = true
	}
	if opts.parsedCorsProxy && *opts.CorsProxy {
		cfg.EnableCorsProxy = true
	}
	if opts.parsedDisableCsrf && *opts.DisableCsrf {
		cfg.DisableCsrfProtection = true
	}
	if opts.ListenAddressIPv4 != "" {
		cfg.ListenAddress.IPv4 = opts.ListenAddressIPv4
	}
	if opts.ListenAddressIPv6 != "" {
		cfg.ListenAddress.IPv6 = opts.ListenAddressIPv6
	}
	if opts.HeartbeatInterval != 0 {
		cfg.HeartbeatInterval = opts.HeartbeatInterval
	}
	if opts.parsedProxyEnabled && *opts.RequestProxyEnabled {
		cfg.RequestProxy.Enabled = true
	}
	if opts.RequestProxyURL != "" {
		cfg.RequestProxy.URL = opts.RequestProxyURL
	}
	if opts.RequestProxyBypass != "" {
		cfg.RequestProxy.Bypass = strings.Fields(opts.RequestProxyBypass)
	}
	if opts.parsedSecurityOverride && *opts.SecurityOverride {
		cfg.SecurityOverride = true
	}
	if opts.parsedBrowserEnabled && *opts.BrowserLaunchEnabled {
		cfg.BrowserLaunch.Enabled = true
	}
	if opts.parsedAutorun && *opts.Autorun {
		cfg.BrowserLaunch.Enabled = true
	}
	if opts.BrowserLaunchHostname != "" {
		cfg.BrowserLaunch.Hostname = opts.BrowserLaunchHostname
	} else if opts.AutorunHostname != "" {
		cfg.BrowserLaunch.Hostname = opts.AutorunHostname
	}
	if opts.BrowserLaunchPort != 0 {
		cfg.BrowserLaunch.Port = opts.BrowserLaunchPort
	} else if opts.AutorunPortOverride != 0 {
		cfg.BrowserLaunch.Port = opts.AutorunPortOverride
	}
	if opts.parsedBrowserAvoid && *opts.BrowserLaunchAvoidLocalhost {
		cfg.BrowserLaunch.AvoidLocalhost = true
	}
	if opts.parsedAvoidLocalhost && *opts.AvoidLocalhost {
		cfg.BrowserLaunch.AvoidLocalhost = true
	}
}

// ListenAddr returns the address to listen on based on the config.
func ListenAddr(cfg *Config) string {
	addr := cfg.ListenAddress.IPv4
	if !cfg.Protocol.IPv4Enabled() && cfg.Protocol.IPv6Enabled() {
		addr = cfg.ListenAddress.IPv6
	}
	if addr == "" {
		addr = "0.0.0.0"
	}
	return addr + ":" + strconv.Itoa(cfg.Port)
}
