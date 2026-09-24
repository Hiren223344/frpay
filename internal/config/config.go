// Package config loads Frenix Pay's runtime configuration from the
// environment. No secret (master xpub, merchant secrets encryption key,
// RPC API keys) ever has a hardcoded default in code.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/hiren223344/frpay/internal/security"
)

type Config struct {
	HTTPAddr      string
	MigrationsDir string

	PostgresDSN string
	RedisAddr   string
	RedisDB     int

	MerchantRateLimitPerMinute int
	PublicRateLimitPerMinute   int

	// AppEncryptionKey encrypts merchant secrets (api_secret, webhook_secret)
	// at rest. 32 raw bytes, given as base64/hex via env.
	AppEncryptionKey []byte

	// MasterXPubs holds one extended public key per chain family, used for
	// watch-only BIP44 address derivation. This service never sees a
	// private key.
	MasterXPubTron string // coin type 195
	MasterXPubEVM  string // coin type 60, shared by all EVM chains

	OrderExpiry time.Duration

	Chains ChainsConfig
}

type ChainsConfig struct {
	Tron     TronConfig
	Ethereum EVMConfig
	BSC      EVMConfig
	Polygon  EVMConfig
}

type TronConfig struct {
	Enabled          bool
	APIURL           string
	FallbackAPIURLs  []string
	APIKey           string
	USDTContract     string
	PollInterval     time.Duration
	RequiredConfirms int64
}

type EVMConfig struct {
	Enabled          bool
	ChainName        string
	RPCURL           string
	FallbackRPCURLs  []string
	USDTContract     string
	PollInterval     time.Duration
	RequiredConfirms int64
	// LogsBatchBlocks caps the block range per eth_getLogs call so a large
	// backlog after downtime doesn't get filtered in one giant request.
	LogsBatchBlocks int64
}

func Load() (*Config, error) {
	cfg := &Config{
		HTTPAddr:      getEnvDefault("HTTP_ADDR", ":8080"),
		MigrationsDir: getEnvDefault("MIGRATIONS_DIR", "migrations"),
		PostgresDSN:   os.Getenv("POSTGRES_DSN"),
		RedisAddr:     getEnvDefault("REDIS_ADDR", "127.0.0.1:6379"),
		RedisDB:       getEnvIntDefault("REDIS_DB", 0),

		MerchantRateLimitPerMinute: getEnvIntDefault("MERCHANT_RATE_LIMIT_PER_MINUTE", 120),
		PublicRateLimitPerMinute:   getEnvIntDefault("PUBLIC_RATE_LIMIT_PER_MINUTE", 60),

		MasterXPubTron: os.Getenv("MASTER_XPUB_TRON"),
		MasterXPubEVM:  os.Getenv("MASTER_XPUB_EVM"),

		OrderExpiry: getEnvDurationDefault("ORDER_EXPIRY", 20*time.Minute),
	}

	if cfg.PostgresDSN == "" {
		return nil, fmt.Errorf("POSTGRES_DSN is required")
	}

	keyRaw := os.Getenv("APP_ENCRYPTION_KEY")
	if keyRaw == "" {
		return nil, fmt.Errorf("APP_ENCRYPTION_KEY is required (32 bytes, base64)")
	}
	key, err := security.DecodeKey(keyRaw)
	if err != nil {
		return nil, fmt.Errorf("APP_ENCRYPTION_KEY: %w", err)
	}
	cfg.AppEncryptionKey = key

	cfg.Chains.Tron = TronConfig{
		Enabled:          getEnvBoolDefault("TRON_ENABLED", true),
		APIURL:           getEnvDefault("TRON_API_URL", "https://api.trongrid.io"),
		FallbackAPIURLs:  splitCSV(os.Getenv("TRON_FALLBACK_API_URLS")),
		APIKey:           os.Getenv("TRON_API_KEY"),
		USDTContract:     getEnvDefault("TRON_USDT_CONTRACT", "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t"),
		PollInterval:     getEnvDurationDefault("TRON_POLL_INTERVAL", 5*time.Second),
		RequiredConfirms: int64(getEnvIntDefault("TRON_REQUIRED_CONFIRMATIONS", 20)),
	}

	if cfg.MasterXPubTron == "" && cfg.Chains.Tron.Enabled {
		return nil, fmt.Errorf("MASTER_XPUB_TRON is required when TRON_ENABLED=true")
	}

	cfg.Chains.Ethereum = loadEVMConfig("ETHEREUM", "ethereum",
		"https://eth.llamarpc.com",
		"0xdAC17F958D2ee523a2206206994597C13D831ec", 12)
	cfg.Chains.BSC = loadEVMConfig("BSC", "bsc",
		"https://bsc-dataseed.binance.org",
		"0x55d398326f99059fF775485246999027B3197955", 15)
	cfg.Chains.Polygon = loadEVMConfig("POLYGON", "polygon",
		"https://polygon-rpc.com",
		"0xc2132D05D31c914a87C6611C10748AEb04B58e8F", 128)

	if (cfg.Chains.Ethereum.Enabled || cfg.Chains.BSC.Enabled || cfg.Chains.Polygon.Enabled) && cfg.MasterXPubEVM == "" {
		return nil, fmt.Errorf("MASTER_XPUB_EVM is required when any EVM chain is enabled")
	}

	return cfg, nil
}

func loadEVMConfig(envPrefix, chainName, defaultRPC, defaultUSDT string, defaultConfirms int64) EVMConfig {
	return EVMConfig{
		Enabled:          getEnvBoolDefault(envPrefix+"_ENABLED", false),
		ChainName:        chainName,
		RPCURL:           getEnvDefault(envPrefix+"_RPC_URL", defaultRPC),
		FallbackRPCURLs:  splitCSV(os.Getenv(envPrefix + "_FALLBACK_RPC_URLS")),
		USDTContract:     getEnvDefault(envPrefix+"_USDT_CONTRACT", defaultUSDT),
		PollInterval:     getEnvDurationDefault(envPrefix+"_POLL_INTERVAL", 15*time.Second),
		RequiredConfirms: int64(getEnvIntDefault(envPrefix+"_REQUIRED_CONFIRMATIONS", int(defaultConfirms))),
		LogsBatchBlocks:  int64(getEnvIntDefault(envPrefix+"_LOGS_BATCH_BLOCKS", 2000)),
	}
}

func getEnvDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func getEnvIntDefault(key string, def int) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

func getEnvBoolDefault(key string, def bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def
	}
	return b
}

func getEnvDurationDefault(key string, def time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return def
	}
	return d
}

func splitCSV(v string) []string {
	if v == "" {
		return nil
	}
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
