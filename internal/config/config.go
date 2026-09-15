package config

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/TurtleTavern/turtletavern/internal/util"
	"gopkg.in/yaml.v3"
)

// Config mirrors the full TurtleTavern config.yaml structure.
type Config struct {
	DataRoot string `yaml:"dataRoot"`

	Listen          bool   `yaml:"listen"`
	ListenAddress   ListenAddress `yaml:"listenAddress"`
	Protocol        Protocol      `yaml:"protocol"`
	DNSPreferIPv6   bool          `yaml:"dnsPreferIPv6"`
	BrowserLaunch   BrowserLaunch `yaml:"browserLaunch"`
	Port            int           `yaml:"port"`
	HeartbeatInterval int         `yaml:"heartbeatInterval"`

	SSL SSLConfig `yaml:"ssl"`

	WhitelistMode           bool     `yaml:"whitelistMode"`
	EnableForwardedWhitelist bool    `yaml:"enableForwardedWhitelist"`
	Whitelist               []string `yaml:"whitelist"`
	WhitelistDockerHosts    bool     `yaml:"whitelistDockerHosts"`

	BasicAuthMode bool         `yaml:"basicAuthMode"`
	BasicAuthUser BasicAuthUser `yaml:"basicAuthUser"`

	EnableCorsProxy bool    `yaml:"enableCorsProxy"`
	CORS            CORSConfig `yaml:"cors"`

	RequestProxy RequestProxy `yaml:"requestProxy"`

	EnableUserAccounts  bool `yaml:"enableUserAccounts"`
	EnableDiscreetLogin bool `yaml:"enableDiscreetLogin"`
	PerUserBasicAuth    bool `yaml:"perUserBasicAuth"`

	SSO SSOConfig `yaml:"sso"`

	HostWhitelist HostWhitelistConfig `yaml:"hostWhitelist"`

	SessionTimeout         int  `yaml:"sessionTimeout"`
	DisableCsrfProtection  bool `yaml:"disableCsrfProtection"`
	SecurityOverride       bool `yaml:"securityOverride"`

	Logging    LoggingConfig    `yaml:"logging"`
	RateLimiting RateLimitingConfig `yaml:"rateLimiting"`

	Backups BackupConfig `yaml:"backups"`

	Thumbnails ThumbnailsConfig `yaml:"thumbnails"`

	Performance PerformanceConfig `yaml:"performance"`

	CacheBuster CacheBusterConfig `yaml:"cacheBuster"`

	AllowKeysExposure bool     `yaml:"allowKeysExposure"`
	SkipContentCheck  bool     `yaml:"skipContentCheck"`
	WhitelistImportDomains []string `yaml:"whitelistImportDomains"`

	RequestOverrides []RequestOverride `yaml:"requestOverrides"`

	Extensions ExtensionsConfig `yaml:"extensions"`

	Git GitConfig `yaml:"git"`

	EnableDownloadableTokenizers bool   `yaml:"enableDownloadableTokenizers"`
	PromptPlaceholder            string `yaml:"promptPlaceholder"`

	OpenAI OpenAIConfig `yaml:"openai"`
	DeepL  DeepLConfig  `yaml:"deepl"`
	Mistral MistralConfig `yaml:"mistral"`
	Ollama OllamaConfig `yaml:"ollama"`
	Claude ClaudeConfig `yaml:"claude"`
	Gemini GeminiConfig `yaml:"gemini"`

	EnableServerPlugins           bool `yaml:"enableServerPlugins"`
	EnableServerPluginsAutoUpdate bool `yaml:"enableServerPluginsAutoUpdate"`
}

type ListenAddress struct {
	IPv4 string `yaml:"ipv4"`
	IPv6 string `yaml:"ipv6"`
}

type Protocol struct {
	IPv4 interface{} `yaml:"ipv4"`
	IPv6 interface{} `yaml:"ipv6"`
}

// ProtocolEnabled parses the protocol field which can be "auto", true, or false.
func (p Protocol) IPv4Enabled() bool {
	switch v := p.IPv4.(type) {
	case bool:
		return v
	case string:
		return strings.EqualFold(v, "auto") // auto-detect, assume available
	default:
		return true
	}
}

func (p Protocol) IPv6Enabled() bool {
	switch v := p.IPv6.(type) {
	case bool:
		return v
	case string:
		return strings.EqualFold(v, "auto")
	default:
		return false
	}
}

type BrowserLaunch struct {
	Enabled        bool   `yaml:"enabled"`
	Browser        string `yaml:"browser"`
	Hostname       string `yaml:"hostname"`
	Port           int    `yaml:"port"`
	AvoidLocalhost bool   `yaml:"avoidLocalhost"`
}

type SSLConfig struct {
	Enabled      bool   `yaml:"enabled"`
	CertPath     string `yaml:"certPath"`
	KeyPath      string `yaml:"keyPath"`
	KeyPassphrase string `yaml:"keyPassphrase"`
}

type BasicAuthUser struct {
	Username string `yaml:"username"`
	Password string `yaml:"password"`
}

type CORSConfig struct {
	Enabled         bool       `yaml:"enabled"`
	Origin          []string   `yaml:"origin"`
	Methods         []string   `yaml:"methods"`
	AllowedHeaders  []string   `yaml:"allowedHeaders"`
	ExposedHeaders  []string   `yaml:"exposedHeaders"`
	Credentials     bool       `yaml:"credentials"`
	MaxAge          *int       `yaml:"maxAge"`
}

type RequestProxy struct {
	Enabled bool     `yaml:"enabled"`
	URL     string   `yaml:"url"`
	Bypass  []string `yaml:"bypass"`
}

type SSOConfig struct {
	AutheliaAuth  bool `yaml:"autheliaAuth"`
	AuthentikAuth bool `yaml:"authentikAuth"`
}

type HostWhitelistConfig struct {
	Enabled bool     `yaml:"enabled"`
	Scan    bool     `yaml:"scan"`
	Hosts   []string `yaml:"hosts"`
}

type LoggingConfig struct {
	EnableAccessLog bool `yaml:"enableAccessLog"`
	MinLogLevel     int  `yaml:"minLogLevel"`
}

type RateLimitingConfig struct {
	PreferRealIPHeader bool `yaml:"preferRealIpHeader"`
}

type BackupConfig struct {
	AllowFullDataBackup bool         `yaml:"allowFullDataBackup"`
	Common              BackupCommon `yaml:"common"`
	Chat                BackupChat   `yaml:"chat"`
}

type BackupCommon struct {
	NumberOfBackups int `yaml:"numberOfBackups"`
}

type BackupChat struct {
	Enabled          bool `yaml:"enabled"`
	CheckIntegrity   bool `yaml:"checkIntegrity"`
	MaxTotalBackups  int  `yaml:"maxTotalBackups"`
	ThrottleInterval int  `yaml:"throttleInterval"`
}

type ThumbnailsConfig struct {
	Enabled    bool                     `yaml:"enabled"`
	Format     string                   `yaml:"format"`
	Quality    int                      `yaml:"quality"`
	Dimensions map[string][2]int        `yaml:"dimensions"`
}

type PerformanceConfig struct {
	LazyLoadCharacters  bool                   `yaml:"lazyLoadCharacters"`
	MemoryCacheCapacity string                 `yaml:"memoryCacheCapacity"`
	UseDiskCache        bool                   `yaml:"useDiskCache"`
	UseCharacterIndex   bool                   `yaml:"useCharacterIndex"`
	RequestCompression  RequestCompressionConfig `yaml:"requestCompression"`
}

type RequestCompressionConfig struct {
	Enabled        bool   `yaml:"enabled"`
	MinPayloadSize string `yaml:"minPayloadSize"`
	MaxPayloadSize string `yaml:"maxPayloadSize"`
	Timeout        int    `yaml:"timeout"`
}

type CacheBusterConfig struct {
	Enabled         bool   `yaml:"enabled"`
	UserAgentPattern string `yaml:"userAgentPattern"`
}

type RequestOverride struct {
	Hosts   []string          `yaml:"hosts"`
	Headers map[string]string `yaml:"headers"`
}

type ExtensionsConfig struct {
	Enabled    bool              `yaml:"enabled"`
	AutoUpdate bool              `yaml:"autoUpdate"`
	Models     ExtensionsModels  `yaml:"models"`
}

type ExtensionsModels struct {
	AutoDownload  bool   `yaml:"autoDownload"`
	Classification string `yaml:"classification"`
	Captioning    string `yaml:"captioning"`
	Embedding     string `yaml:"embedding"`
	SpeechToText  string `yaml:"speechToText"`
	TextToSpeech  string `yaml:"textToSpeech"`
}

type GitConfig struct {
	Backend string `yaml:"backend"`
}

type OpenAIConfig struct {
	RandomizeUserID    bool   `yaml:"randomizeUserId"`
	CaptionSystemPrompt string `yaml:"captionSystemPrompt"`
}

type DeepLConfig struct {
	Formality string `yaml:"formality"`
}

type MistralConfig struct {
	EnablePrefix bool `yaml:"enablePrefix"`
}

type OllamaConfig struct {
	KeepAlive int `yaml:"keepAlive"`
	BatchSize int `yaml:"batchSize"`
}

type ClaudeConfig struct {
	EnableSystemPromptCache bool `yaml:"enableSystemPromptCache"`
	CachingAtDepth          int  `yaml:"cachingAtDepth"`
	ExtendedTTL             bool `yaml:"extendedTTL"`
	EnableAdaptiveThinking  bool `yaml:"enableAdaptiveThinking"`
}

type GeminiConfig struct {
	APIVersion              string         `yaml:"apiVersion"`
	ThoughtSignatures       bool           `yaml:"thoughtSignatures"`
	EnableSystemPromptCache bool           `yaml:"enableSystemPromptCache"`
	Image                   GeminiImage    `yaml:"image"`
}

type GeminiImage struct {
	PersonGeneration string `yaml:"personGeneration"`
}

// DefaultConfig returns a Config matching the default config.yaml values.
func DefaultConfig() *Config {
	maxAge := 0
	return &Config{
		DataRoot:         "./data",
		Listen:           false,
		ListenAddress:    ListenAddress{IPv4: "0.0.0.0", IPv6: "[::]"},
		Protocol:         Protocol{IPv4: true, IPv6: false},
		DNSPreferIPv6:    false,
		BrowserLaunch: BrowserLaunch{
			Enabled:        true,
			Browser:        "default",
			Hostname:       "auto",
			Port:           -1,
			AvoidLocalhost: false,
		},
		Port:              8000,
		HeartbeatInterval: 0,

		SSL: SSLConfig{
			Enabled:      false,
			CertPath:     "./certs/cert.pem",
			KeyPath:      "./certs/privkey.pem",
			KeyPassphrase: "",
		},

		WhitelistMode:            true,
		EnableForwardedWhitelist: true,
		Whitelist:                []string{"::1", "127.0.0.1"},
		WhitelistDockerHosts:     true,

		BasicAuthMode: false,
		BasicAuthUser: BasicAuthUser{Username: "user", Password: "password"},

		EnableCorsProxy: false,
		CORS: CORSConfig{
			Enabled:        true,
			Origin:         []string{"null"},
			Methods:        []string{"OPTIONS"},
			AllowedHeaders: []string{},
			ExposedHeaders: []string{},
			Credentials:    false,
			MaxAge:         &maxAge,
		},

		RequestProxy: RequestProxy{
			Enabled: false,
			URL:     "socks5://username:password@example.com:1080",
			Bypass:  []string{"localhost", "127.0.0.1"},
		},

		EnableUserAccounts:  false,
		EnableDiscreetLogin: false,
		PerUserBasicAuth:    false,

		SSO: SSOConfig{AutheliaAuth: false, AuthentikAuth: false},

		HostWhitelist: HostWhitelistConfig{Enabled: false, Scan: true, Hosts: []string{}},

		SessionTimeout:        -1,
		DisableCsrfProtection: false,
		SecurityOverride:      false,

		Logging:     LoggingConfig{EnableAccessLog: true, MinLogLevel: 0},
		RateLimiting: RateLimitingConfig{PreferRealIPHeader: false},

		Backups: BackupConfig{
			AllowFullDataBackup: true,
			Common:              BackupCommon{NumberOfBackups: 50},
			Chat: BackupChat{
				Enabled:          true,
				CheckIntegrity:   true,
				MaxTotalBackups:  -1,
				ThrottleInterval: 10000,
			},
		},

		Thumbnails: ThumbnailsConfig{
			Enabled: true,
			Format:  "jpg",
			Quality: 95,
			Dimensions: map[string][2]int{
				"bg":      {160, 90},
				"avatar":  {96, 144},
				"persona": {96, 144},
			},
		},

		Performance: PerformanceConfig{
			LazyLoadCharacters:  false,
			MemoryCacheCapacity: "100mb",
			UseDiskCache:        true,
			UseCharacterIndex:   true,
			RequestCompression: RequestCompressionConfig{
				Enabled:        false,
				MinPayloadSize: "256kb",
				MaxPayloadSize: "8mb",
				Timeout:        4000,
			},
		},

		CacheBuster: CacheBusterConfig{
			Enabled:         false,
			UserAgentPattern: "",
		},

		AllowKeysExposure:      false,
		SkipContentCheck:       false,
		WhitelistImportDomains: []string{"localhost", "cdn.discordapp.com", "files.catbox.moe", "raw.githubusercontent.com"},
		RequestOverrides:       []RequestOverride{},

		Extensions: ExtensionsConfig{
			Enabled:    true,
			AutoUpdate: true,
			Models: ExtensionsModels{
				AutoDownload:   true,
				Classification: "Cohee/distilbert-base-uncased-go-emotions-onnx",
				Captioning:     "Xenova/vit-gpt2-image-captioning",
				Embedding:      "Cohee/jina-embeddings-v2-base-en",
				SpeechToText:   "Xenova/whisper-small",
				TextToSpeech:   "Xenova/speecht5_tts",
			},
		},

		Git: GitConfig{Backend: "auto"},

		EnableDownloadableTokenizers: true,
		PromptPlaceholder:           "[Start a new chat]",

		OpenAI:  OpenAIConfig{RandomizeUserID: false, CaptionSystemPrompt: ""},
		DeepL:   DeepLConfig{Formality: "default"},
		Mistral: MistralConfig{EnablePrefix: false},
		Ollama:  OllamaConfig{KeepAlive: -1, BatchSize: -1},
		Claude: ClaudeConfig{
			EnableSystemPromptCache: false,
			CachingAtDepth:          -1,
			ExtendedTTL:             false,
			EnableAdaptiveThinking:  false,
		},
		Gemini: GeminiConfig{
			APIVersion:              "v1beta",
			ThoughtSignatures:       true,
			EnableSystemPromptCache: false,
			Image:                   GeminiImage{PersonGeneration: "allow_adult"},
		},

		EnableServerPlugins:           false,
		EnableServerPluginsAutoUpdate: true,
	}
}

// Load reads and parses the YAML config file at the given path.
// If the file does not exist, DefaultConfig is returned.
func Load(path string) (*Config, error) {
	cfg := DefaultConfig()

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}

	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}

	applyEnvOverrides(cfg)
	return cfg, nil
}

// applyEnvOverrides applies SILLYTAVERN_ prefixed environment variables.
func applyEnvOverrides(cfg *Config) {
	if v := os.Getenv("SILLYTAVERN_DATA_ROOT"); v != "" {
		cfg.DataRoot = v
	}
	if v := os.Getenv("SILLYTAVERN_PORT"); v != "" {
		if p, err := strconv.Atoi(v); err == nil {
			cfg.Port = p
		}
	}
	if v := os.Getenv("SILLYTAVERN_LISTEN"); v != "" {
		cfg.Listen = v == "true" || v == "1"
	}
}

// DataDir returns the resolved absolute path for dataRoot.
func (c *Config) DataDir() string {
	if filepath.IsAbs(c.DataRoot) {
		return c.DataRoot
	}
	return util.ResolveAppPath(c.DataRoot)
}

// HostHash returns a SHA-256 prefix of the hostname, used for cookie names.
func (c *Config) HostHash() string {
	hostname, err := os.Hostname()
	if err != nil {
		hostname = "localhost"
	}
	h := sha256.Sum256([]byte(hostname))
	return fmt.Sprintf("%x", h)[:8]
}
