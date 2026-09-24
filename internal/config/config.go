// Package config loads Frenix Pay's runtime configuration from the
// environment. This service never holds a private key: it only ever
// needs the public receiving address for each chain and read-only RPC
// endpoints.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

type Config struct {
	HTTPAddr      string
	MigrationsDir string

	PostgresDSN string
	RedisAddr   string
	RedisDB     int

	// APIKey guards POST /v1/orders. This is a single-tenant service —
	// one shared secret, not per-caller credentials.
	APIKey string

	OrdersRateLimitPerMinute int
	PublicRateLimitPerMinute int

	OrderExpiry time.Duration

	// AmountTolerance is how far an on-chain amount may differ from an
	// order's expected_amount and still count as an exact match
	// (rounding/precision slack), in USD.
	AmountTolerance decimal.Decimal

	// UnderpaymentMaxGapPercent bounds how far short of expected_amount a
	// transfer may fall and still be attributed to that order as
	// "underpaid" rather than logged as a fully unmatched payment. E.g.
	// 50 means a transfer for as little as 50% of the expected amount is
	// still treated as an underpayment attempt on that order; anything
	// short of that is too ambiguous to attribute automatically.
	UnderpaymentMaxGapPercent decimal.Decimal

	// Every new order's expected_amount is base_amount_usd plus a random
	// offset of OffsetMin..OffsetMax ten-thousandths of a dollar (i.e.
	// $0.0001-$0.0099 by default), retried up to MaxCollisionRetries
	// times on a collision with another pending order on the same chain.
	OffsetMinTenThousandths int
	OffsetMaxTenThousandths int
	MaxCollisionRetries     int

	Chains ChainsConfig
}

type ChainsConfig struct {
	Tron    TronConfig
	TON     TONConfig
	Polygon EVMConfig
	BSC     EVMConfig
}

type TronConfig struct {
	Enabled          bool
	Address          string
	APIURL           string
	FallbackAPIURLs  []string
	APIKey           string
	USDTContract     string
	PollInterval     time.Duration
	RequiredConfirms int64
}

type TONConfig struct {
	Enabled bool
	Address string
	// APIURL is the toncenter v3 API base, e.g. https://toncenter.com/api/v3
	APIURL          string
	FallbackAPIURLs []string
	// APIKey is an optional toncenter API key (raises rate limits); the
	// public API works without one.
	APIKey string
	// USDTJettonMaster is the USDT Jetton master contract address on TON.
	USDTJettonMaster string
	PollInterval     time.Duration
	// RequiredConfirms is measured in masterchain blocks elapsed since
	// the transfer's own block, analogous to "confirmations" elsewhere.
	// TON's finality is fast (~5s/block); a small number is normal.
	RequiredConfirms int64
}

type EVMConfig struct {
	Enabled          bool
	ChainName        string
	Address          string
	RPCURL           string
	FallbackRPCURLs  []string
	USDTContract     string
	PollInterval     time.Duration
	RequiredConfirms int64
	// LogsBatchBlocks caps the block range per eth_getLogs call so a
	// large backlog after downtime doesn't get filtered in one giant
	// request.
	LogsBatchBlocks int64
}

func Load() (*Config, error) {
	cfg := &Config{
		HTTPAddr:      getEnvDefault("HTTP_ADDR", ":8080"),
		MigrationsDir: getEnvDefault("MIGRATIONS_DIR", "migrations"),
		PostgresDSN:   os.Getenv("POSTGRES_DSN"),
		RedisAddr:     getEnvDefault("REDIS_ADDR", "127.0.0.1:6379"),
		RedisDB:       getEnvIntDefault("REDIS_DB", 0),

		APIKey: os.Getenv("API_KEY"),

		OrdersRateLimitPerMinute: getEnvIntDefault("ORDERS_RATE_LIMIT_PER_MINUTE", 60),
		PublicRateLimitPerMinute: getEnvIntDefault("PUBLIC_RATE_LIMIT_PER_MINUTE", 60),

		OrderExpiry: getEnvDurationDefault("ORDER_EXPIRY", 20*time.Minute),

		OffsetMinTenThousandths: getEnvIntDefault("EXPECTED_AMOUNT_OFFSET_MIN", 1),
		OffsetMaxTenThousandths: getEnvIntDefault("EXPECTED_AMOUNT_OFFSET_MAX", 99),
		MaxCollisionRetries:     getEnvIntDefault("MAX_AMOUNT_COLLISION_RETRIES", 20),
	}

	if cfg.PostgresDSN == "" {
		return nil, fmt.Errorf("POSTGRES_DSN is required")
	}
	if cfg.APIKey == "" {
		return nil, fmt.Errorf("API_KEY is required")
	}

	tolerance, err := getEnvDecimalDefault("AMOUNT_TOLERANCE_USD", decimal.NewFromFloat(0.02))
	if err != nil {
		return nil, fmt.Errorf("AMOUNT_TOLERANCE_USD: %w", err)
	}
	cfg.AmountTolerance = tolerance

	underpaymentGap, err := getEnvDecimalDefault("UNDERPAYMENT_MAX_GAP_PERCENT", decimal.NewFromInt(50))
	if err != nil {
		return nil, fmt.Errorf("UNDERPAYMENT_MAX_GAP_PERCENT: %w", err)
	}
	cfg.UnderpaymentMaxGapPercent = underpaymentGap

	cfg.Chains.Tron = TronConfig{
		Enabled:          getEnvBoolDefault("TRON_ENABLED", true),
		Address:          os.Getenv("TRON_ADDRESS"),
		APIURL:           getEnvDefault("TRON_API_URL", "https://api.trongrid.io"),
		FallbackAPIURLs:  splitCSV(os.Getenv("TRON_FALLBACK_API_URLS")),
		APIKey:           os.Getenv("TRON_API_KEY"),
		USDTContract:     getEnvDefault("TRON_USDT_CONTRACT", "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t"),
		PollInterval:     getEnvDurationDefault("TRON_POLL_INTERVAL", 5*time.Second),
		RequiredConfirms: int64(getEnvIntDefault("TRON_REQUIRED_CONFIRMATIONS", 20)),
	}
	if cfg.Chains.Tron.Enabled && cfg.Chains.Tron.Address == "" {
		return nil, fmt.Errorf("TRON_ADDRESS is required when TRON_ENABLED=true")
	}

	cfg.Chains.TON = TONConfig{
		Enabled:          getEnvBoolDefault("TON_ENABLED", false),
		Address:          os.Getenv("TON_ADDRESS"),
		APIURL:           getEnvDefault("TON_API_URL", "https://toncenter.com/api/v3"),
		FallbackAPIURLs:  splitCSV(os.Getenv("TON_FALLBACK_API_URLS")),
		APIKey:           os.Getenv("TON_API_KEY"),
		USDTJettonMaster: getEnvDefault("TON_USDT_JETTON_MASTER", "EQCxE6mUtQJKFnGfaROTKOt1lZbDiiX1kCixRv7Nw2Id_sDs"),
		PollInterval:     getEnvDurationDefault("TON_POLL_INTERVAL", 8*time.Second),
		RequiredConfirms: int64(getEnvIntDefault("TON_REQUIRED_CONFIRMATIONS", 5)),
	}
	if cfg.Chains.TON.Enabled && cfg.Chains.TON.Address == "" {
		return nil, fmt.Errorf("TON_ADDRESS is required when TON_ENABLED=true")
	}

	cfg.Chains.Polygon = loadEVMConfig("POLYGON", "polygon",
		"https://polygon-rpc.com",
		"0xc2132D05D31c914a87C6611C10748AEb04B58e8F", 128)
	cfg.Chains.BSC = loadEVMConfig("BSC", "bsc",
		"https://bsc-dataseed.binance.org",
		"0x55d398326f99059fF775485246999027B3197955", 15)

	if cfg.Chains.Polygon.Enabled && cfg.Chains.Polygon.Address == "" {
		return nil, fmt.Errorf("POLYGON_ADDRESS is required when POLYGON_ENABLED=true")
	}
	if cfg.Chains.BSC.Enabled && cfg.Chains.BSC.Address == "" {
		return nil, fmt.Errorf("BSC_ADDRESS is required when BSC_ENABLED=true")
	}

	return cfg, nil
}

func loadEVMConfig(envPrefix, chainName, defaultRPC, defaultUSDT string, defaultConfirms int64) EVMConfig {
	return EVMConfig{
		Enabled:          getEnvBoolDefault(envPrefix+"_ENABLED", false),
		ChainName:        chainName,
		Address:          os.Getenv(envPrefix + "_ADDRESS"),
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

func getEnvDecimalDefault(key string, def decimal.Decimal) (decimal.Decimal, error) {
	v := os.Getenv(key)
	if v == "" {
		return def, nil
	}
	return decimal.NewFromString(v)
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
