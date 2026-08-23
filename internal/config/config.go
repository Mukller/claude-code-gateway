package config

import (
	"fmt"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Server    Server      `yaml:"server"`
	Auth      Auth        `yaml:"auth"`
	Providers []Provider  `yaml:"providers"`
	Routing   Routing     `yaml:"routing"`
	Retry     Retry       `yaml:"retry"`
	Pricing   []PriceRule `yaml:"pricing"`
	Logging   Logging     `yaml:"logging"`
	RateLimit int         `yaml:"rate_limit_rpm"`
	Cache     Cache       `yaml:"cache"`
}

type Cache struct {
	Enabled    bool          `yaml:"enabled"`
	TTL        time.Duration `yaml:"ttl"`
	MaxEntries int           `yaml:"max_entries"`
}

type Server struct {
	Listen       string        `yaml:"listen"`
	ReadTimeout  time.Duration `yaml:"read_timeout"`
	WriteTimeout time.Duration `yaml:"write_timeout"`
	UpstreamTO   time.Duration `yaml:"upstream_timeout"`
}

type Auth struct {
	Tokens     []string `yaml:"tokens"`
	AdminToken string   `yaml:"admin_token"`
	AllowAnon  bool     `yaml:"allow_anon"`
}

type Provider struct {
	Name               string            `yaml:"name"`
	Type               string            `yaml:"type"`
	BaseURL            string            `yaml:"base_url"`
	Keys               []string          `yaml:"keys"`
	AuthStyle          string            `yaml:"auth_style"`
	AnthropicVersion   string            `yaml:"anthropic_version"`
	ExtraHeaders       map[string]string `yaml:"extra_headers"`
	Timeout            time.Duration     `yaml:"timeout"`
	DiscoverModels     bool              `yaml:"discover_models"`
	RefreshInterval    time.Duration     `yaml:"refresh_interval"`
	Models             []string          `yaml:"models"`
	SendStreamOptions  bool              `yaml:"send_stream_options"`
	Region             string            `yaml:"region"`
	ServiceAccountJSON string            `yaml:"service_account_json"`
}

type Routing struct {
	DefaultChain      []string `yaml:"default_chain"`
	AliasClaudePrefix bool     `yaml:"alias_claude_prefix"`
	Rules             []Rule   `yaml:"rules"`
}

type Rule struct {
	Prefix      string            `yaml:"prefix"`
	StripPrefix bool              `yaml:"strip_prefix"`
	Chain       []string          `yaml:"chain"`
	ModelMap    map[string]string `yaml:"model_map"`
}

type Retry struct {
	MaxAttempts   int   `yaml:"max_attempts"`
	RetryStatuses []int `yaml:"retry_statuses"`
	KeyFailStatus []int `yaml:"key_fail_statuses"`
}

type PriceRule struct {
	Pattern           string  `yaml:"pattern"`
	InputPerMTok      float64 `yaml:"input_per_mtok"`
	OutputPerMTok     float64 `yaml:"output_per_mtok"`
	CacheReadPerMTok  float64 `yaml:"cache_read_per_mtok"`
	CacheWritePerMTok float64 `yaml:"cache_write_per_mtok"`
}

type Logging struct {
	File     string `yaml:"file"`
	RingSize int    `yaml:"ring_size"`
}

func Load(path string) (*Config, error) {
	c, err := parseFile(path)
	if err != nil {
		return nil, err
	}
	if err := c.validate(); err != nil {
		return nil, err
	}
	return c, nil
}

func LoadLenient(path string) (*Config, error) {
	return parseFile(path)
}

func parseFile(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	expanded := os.Expand(string(raw), expandVar)
	var c Config
	if err := yaml.Unmarshal([]byte(expanded), &c); err != nil {
		return nil, fmt.Errorf("parse yaml: %w", err)
	}
	c.applyDefaults()
	return &c, nil
}

func expandVar(k string) string {
	def := ""
	name := k
	if i := strings.Index(k, ":-"); i >= 0 {
		name = k[:i]
		def = k[i+2:]
	}
	v := os.Getenv(name)
	if v == "" {
		return def
	}
	return v
}

func (c *Config) applyDefaults() {
	if c.Server.Listen == "" {
		c.Server.Listen = ":8080"
	}
	if c.Server.ReadTimeout == 0 {
		c.Server.ReadTimeout = 120 * time.Second
	}
	if c.Server.WriteTimeout == 0 {
		c.Server.WriteTimeout = 15 * time.Minute
	}
	if c.Server.UpstreamTO == 0 {
		c.Server.UpstreamTO = 10 * time.Minute
	}
	if c.Retry.MaxAttempts <= 0 {
		c.Retry.MaxAttempts = 8
	}
	if len(c.Retry.RetryStatuses) == 0 {
		c.Retry.RetryStatuses = []int{408, 409, 429, 500, 502, 503, 504, 529}
	}
	if len(c.Retry.KeyFailStatus) == 0 {
		c.Retry.KeyFailStatus = []int{401, 403}
	}
	if c.Logging.File == "" {
		c.Logging.File = "data/usage.jsonl"
	}
	if c.Logging.RingSize <= 0 {
		c.Logging.RingSize = 500
	}
	if c.Cache.TTL <= 0 {
		c.Cache.TTL = 10 * time.Minute
	}
	if c.Cache.MaxEntries <= 0 {
		c.Cache.MaxEntries = 128
	}
	if len(c.Providers) > 0 && len(c.Routing.DefaultChain) == 0 {
		c.Routing.DefaultChain = []string{c.Providers[0].Name}
	}
	for i := range c.Providers {
		p := &c.Providers[i]
		if p.Timeout == 0 {
			p.Timeout = c.Server.UpstreamTO
		}
		if p.RefreshInterval == 0 {
			p.RefreshInterval = 30 * time.Minute
		}
		if p.AnthropicVersion == "" {
			p.AnthropicVersion = "2023-06-01"
		}
		if p.AuthStyle == "" {
			p.AuthStyle = "x-api-key"
		}
	}
}

func (c *Config) validate() error {
	names := map[string]bool{}
	hasValid := false
	for _, p := range c.Providers {
		if p.Name == "" {
			return fmt.Errorf("provider with empty name")
		}
		if names[p.Name] {
			return fmt.Errorf("duplicate provider name %q", p.Name)
		}
		names[p.Name] = true
		switch p.Type {
		case "anthropic", "anthropic-compat", "openai", "bedrock", "vertex":
		default:
			return fmt.Errorf("provider %q: unknown type %q", p.Name, p.Type)
		}
		if p.BaseURL == "" {
			return fmt.Errorf("provider %q: base_url required", p.Name)
		}
		n := 0
		for _, k := range p.Keys {
			if k != "" {
				n++
			}
		}
		if n > 0 || (p.Type == "vertex" && p.AuthStyle == "sa") {
			hasValid = true
		}
	}
	if !hasValid {
		return fmt.Errorf("no provider has at least one API key")
	}
	checkChains := func(chain []string, what string) error {
		for _, cn := range chain {
			if !names[cn] {
				return fmt.Errorf("%s references unknown provider %q", what, cn)
			}
		}
		return nil
	}
	if err := checkChains(c.Routing.DefaultChain, "default_chain"); err != nil {
		return err
	}
	for i, r := range c.Routing.Rules {
		if err := checkChains(r.Chain, fmt.Sprintf("rules[%d]", i)); err != nil {
			return err
		}
	}
	return nil
}
