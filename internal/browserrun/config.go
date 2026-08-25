package browserrun

import (
	"os"
	"strconv"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Server   ServerConfig  `yaml:"server"`
	Browser  BrowserConfig `yaml:"browser"`
	Crawl    CrawlConfig   `yaml:"crawl"`
	AI       AIConfig      `yaml:"ai"`
	Storage  StorageConfig `yaml:"storage"`
	LogLevel string        `yaml:"log_level"`
}

type ServerConfig struct {
	Host      string `yaml:"host"`
	Port      int    `yaml:"port"`
	AuthToken string `yaml:"auth_token"`
}

type BrowserConfig struct {
	ChromiumPath       string        `yaml:"chromium_path"`
	PoolSize           int           `yaml:"pool_size"`
	PoolWaitTimeout    time.Duration `yaml:"pool_wait_timeout"`
	IdleTimeout        time.Duration `yaml:"idle_timeout"`
	MaxSessionLifetime time.Duration `yaml:"max_session_lifetime"`
}

type CrawlConfig struct {
	MaxConcurrentJobs int           `yaml:"max_concurrent_jobs"`
	ResultTTL         time.Duration `yaml:"result_ttl"`
	DefaultDelayMs    int           `yaml:"default_delay_ms"`
}

type AIConfig struct {
	DefaultProvider string `yaml:"default_provider"`
	OllamaBaseURL   string `yaml:"ollama_base_url"`
	OllamaModel     string `yaml:"ollama_model"`
	OpenAIAPIKey    string `yaml:"openai_api_key"`
	AnthropicAPIKey string `yaml:"anthropic_api_key"`
}

type StorageConfig struct {
	DBPath string `yaml:"db_path"`
}

func DefaultConfig() Config {
	return Config{
		Server: ServerConfig{
			Host: "127.0.0.1",
			Port: 7600,
		},
		Browser: BrowserConfig{
			PoolSize:           5,
			PoolWaitTimeout:    30 * time.Second,
			IdleTimeout:        60 * time.Second,
			MaxSessionLifetime: 600 * time.Second,
		},
		Crawl: CrawlConfig{
			MaxConcurrentJobs: 3,
			ResultTTL:         14 * 24 * time.Hour,
			DefaultDelayMs:    500,
		},
		AI: AIConfig{
			DefaultProvider: "ollama",
			OllamaBaseURL:   "http://localhost:11434",
			OllamaModel:     "llama3.2",
		},
		Storage: StorageConfig{
			DBPath: "./data/browser-run.db",
		},
		LogLevel: "info",
	}
}

func LoadConfig(path string) (Config, error) {
	cfg := DefaultConfig()
	if path != "" {
		data, err := os.ReadFile(path)
		if err != nil && !os.IsNotExist(err) {
			return cfg, err
		}
		if err == nil {
			if err := yaml.Unmarshal(data, &cfg); err != nil {
				return cfg, err
			}
		}
	}
	applyEnv(&cfg)
	return cfg, nil
}

func applyEnv(cfg *Config) {
	if v := os.Getenv("BR_HOST"); v != "" {
		cfg.Server.Host = v
	}
	if v := os.Getenv("BR_PORT"); v != "" {
		if p, err := strconv.Atoi(v); err == nil {
			cfg.Server.Port = p
		}
	}
	if v := os.Getenv("BR_AUTH_TOKEN"); v != "" {
		cfg.Server.AuthToken = v
	}
	if v := os.Getenv("BR_CHROMIUM_PATH"); v != "" {
		cfg.Browser.ChromiumPath = v
	}
	if v := os.Getenv("BR_POOL_SIZE"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.Browser.PoolSize = n
		}
	}
	if v := os.Getenv("BR_DB_PATH"); v != "" {
		cfg.Storage.DBPath = v
	}
	if v := os.Getenv("BR_OLLAMA_MODEL"); v != "" {
		cfg.AI.OllamaModel = v
	}
	if v := os.Getenv("BR_OPENAI_API_KEY"); v != "" {
		cfg.AI.OpenAIAPIKey = v
	}
	if v := os.Getenv("BR_ANTHROPIC_API_KEY"); v != "" {
		cfg.AI.AnthropicAPIKey = v
	}
}
