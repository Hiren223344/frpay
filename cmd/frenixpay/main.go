// Command frenixpay runs the Frenix Pay service: the public HTTP API,
// every enabled chain watcher, the order-expiry job and the webhook
// delivery loop, all in one process.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/hiren223344/frpay/internal/api"
	"github.com/hiren223344/frpay/internal/audit"
	"github.com/hiren223344/frpay/internal/chains"
	"github.com/hiren223344/frpay/internal/chains/evm"
	"github.com/hiren223344/frpay/internal/chains/tron"
	"github.com/hiren223344/frpay/internal/config"
	"github.com/hiren223344/frpay/internal/db"
	"github.com/hiren223344/frpay/internal/merchant"
	"github.com/hiren223344/frpay/internal/orders"
	"github.com/hiren223344/frpay/internal/ratelimit"
	"github.com/hiren223344/frpay/internal/security"
	"github.com/hiren223344/frpay/internal/wallet"
	"github.com/hiren223344/frpay/internal/webhook"
)

// depositSinkProxy breaks the construction-order cycle between
// chains.Manager (which the watchers need to exist before) and
// orders.Service (which needs the Manager to exist before it, but is
// itself the DepositSink every watcher reports into). No watcher polls
// until Manager.Run is started explicitly, well after svc is set, so
// this is safe without further synchronization.
type depositSinkProxy struct {
	svc *orders.Service
}

func (p *depositSinkProxy) OnDeposit(ctx context.Context, event chains.DepositEvent) error {
	return p.svc.OnDeposit(ctx, event)
}

func main() {
	createMerchant := flag.String("create-merchant", "", "provision a new merchant with this name, print its credentials, and exit")
	flag.Parse()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	cfg, err := config.Load()
	if err != nil {
		logger.Error("config error", "error", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := db.Migrate(cfg.PostgresDSN, cfg.MigrationsDir); err != nil {
		logger.Error("migration failed", "error", err)
		os.Exit(1)
	}

	sqlDB, err := db.Connect(ctx, cfg.PostgresDSN)
	if err != nil {
		logger.Error("postgres connection failed", "error", err)
		os.Exit(1)
	}
	defer sqlDB.Close()

	box, err := security.NewBox(cfg.AppEncryptionKey)
	if err != nil {
		logger.Error("encryption box init failed", "error", err)
		os.Exit(1)
	}

	merchantRepo := merchant.NewRepository(sqlDB)
	merchantSvc := merchant.NewService(merchantRepo, box)

	if *createMerchant != "" {
		apiKey, apiSecret, err := merchantSvc.CreateMerchant(ctx, *createMerchant)
		if err != nil {
			logger.Error("create merchant failed", "error", err)
			os.Exit(1)
		}
		fmt.Printf("Merchant %q created.\n", *createMerchant)
		fmt.Printf("API Key:    %s\n", apiKey)
		fmt.Printf("API Secret: %s\n", apiSecret)
		fmt.Println("Store the secret now — it is never shown again and is only kept encrypted from here on.")
		return
	}

	redisClient, err := db.ConnectRedis(ctx, cfg.RedisAddr, cfg.RedisDB)
	if err != nil {
		logger.Error("redis connection failed", "error", err)
		os.Exit(1)
	}
	defer redisClient.Close()

	hdWallet, err := wallet.New(map[wallet.ChainFamily]string{
		wallet.FamilyTron: cfg.MasterXPubTron,
		wallet.FamilyEVM:  cfg.MasterXPubEVM,
	})
	if err != nil {
		logger.Error("wallet init failed", "error", err)
		os.Exit(1)
	}

	cursorStore := db.NewCursorStore(sqlDB)
	sinkProxy := &depositSinkProxy{}

	var watchers []chains.ChainWatcher
	if cfg.Chains.Tron.Enabled {
		watchers = append(watchers, tron.NewWatcher(cfg.Chains.Tron, sinkProxy, cursorStore, logger))
	}
	if cfg.Chains.Ethereum.Enabled {
		watchers = append(watchers, evm.NewWatcher(chains.Ethereum, cfg.Chains.Ethereum, sinkProxy, cursorStore, logger))
	}
	if cfg.Chains.BSC.Enabled {
		watchers = append(watchers, evm.NewWatcher(chains.BSC, cfg.Chains.BSC, sinkProxy, cursorStore, logger))
	}
	if cfg.Chains.Polygon.Enabled {
		watchers = append(watchers, evm.NewWatcher(chains.Polygon, cfg.Chains.Polygon, sinkProxy, cursorStore, logger))
	}
	if len(watchers) == 0 {
		logger.Warn("no chains are enabled; orders can still be created but will never confirm")
	}
	chainMgr := chains.NewManager(logger, watchers...)

	auditLogger := audit.NewLogger(sqlDB)
	webhookRepo := webhook.NewRepository(sqlDB)
	webhookDispatcher := webhook.NewDispatcher(webhookRepo, merchantRepo, merchantSvc, logger)

	ordersRepo := orders.NewRepository(sqlDB)
	ordersSvc := orders.NewService(ordersRepo, hdWallet, chainMgr, auditLogger, webhookDispatcher, cfg.OrderExpiry, logger)
	sinkProxy.svc = ordersSvc

	if err := ordersSvc.LoadActiveWatches(ctx); err != nil {
		logger.Error("failed to reload active watches on startup", "error", err)
		os.Exit(1)
	}

	merchantLimiter := ratelimit.NewLimiter(redisClient, cfg.MerchantRateLimitPerMinute, time.Minute, logger)
	publicLimiter := ratelimit.NewLimiter(redisClient, cfg.PublicRateLimitPerMinute, time.Minute, logger)
	router := api.NewRouter(ordersSvc, merchantSvc, merchantLimiter, publicLimiter, logger)

	httpServer := &http.Server{
		Addr:         cfg.HTTPAddr,
		Handler:      router,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := chainMgr.Run(ctx); err != nil {
			logger.Error("chain manager stopped with error", "error", err)
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		ordersSvc.RunExpiryLoop(ctx, 30*time.Second)
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		webhookDispatcher.RunDeliveryLoop(ctx, 15*time.Second)
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		logger.Info("http server starting", "addr", cfg.HTTPAddr)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("http server error", "error", err)
		}
	}()

	<-ctx.Done()
	logger.Info("shutdown signal received")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		logger.Error("http server shutdown error", "error", err)
	}

	wg.Wait()
	logger.Info("shutdown complete")
}
