// Command frenixpay runs the Frenix Pay service: the public HTTP API,
// every enabled chain watcher, and the order-expiry job, all in one
// process.
package main

import (
	"context"
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
	"github.com/hiren223344/frpay/internal/chains/ton"
	"github.com/hiren223344/frpay/internal/chains/tron"
	"github.com/hiren223344/frpay/internal/config"
	"github.com/hiren223344/frpay/internal/db"
	"github.com/hiren223344/frpay/internal/orders"
	"github.com/hiren223344/frpay/internal/ratelimit"
)

// transferSinkProxy breaks the construction-order cycle between
// chains.Manager (which the watchers need to exist before) and
// orders.Service (which needs the Manager to exist before it, but is
// itself the TransferSink every watcher reports into). No watcher polls
// until Manager.Run is started explicitly, well after svc is set, so
// this is safe without further synchronization.
type transferSinkProxy struct {
	svc *orders.Service
}

func (p *transferSinkProxy) OnTransfer(ctx context.Context, event chains.TransferEvent) error {
	return p.svc.OnTransfer(ctx, event)
}

func main() {
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

	redisClient, err := db.ConnectRedis(ctx, cfg.RedisAddr, cfg.RedisDB)
	if err != nil {
		logger.Error("redis connection failed", "error", err)
		os.Exit(1)
	}
	defer redisClient.Close()

	cursorStore := db.NewCursorStore(sqlDB)
	sinkProxy := &transferSinkProxy{}
	addresses := make(map[chains.Chain]string)

	var watchers []chains.ChainWatcher
	if cfg.Chains.Tron.Enabled {
		w, err := tron.NewWatcher(cfg.Chains.Tron, sinkProxy, cursorStore, logger)
		if err != nil {
			logger.Error("tron watcher init failed", "error", err)
			os.Exit(1)
		}
		watchers = append(watchers, w)
		addresses[chains.Tron] = cfg.Chains.Tron.Address
	}
	if cfg.Chains.TON.Enabled {
		watchers = append(watchers, ton.NewWatcher(cfg.Chains.TON, sinkProxy, cursorStore, logger))
		addresses[chains.TON] = cfg.Chains.TON.Address
	}
	if cfg.Chains.Polygon.Enabled {
		watchers = append(watchers, evm.NewWatcher(chains.Polygon, cfg.Chains.Polygon, sinkProxy, cursorStore, logger))
		addresses[chains.Polygon] = cfg.Chains.Polygon.Address
	}
	if cfg.Chains.BSC.Enabled {
		watchers = append(watchers, evm.NewWatcher(chains.BSC, cfg.Chains.BSC, sinkProxy, cursorStore, logger))
		addresses[chains.BSC] = cfg.Chains.BSC.Address
	}
	if len(watchers) == 0 {
		logger.Warn("no chains are enabled; orders can still be created but will never confirm")
	}
	chainMgr := chains.NewManager(logger, watchers...)

	auditLogger := audit.NewLogger(sqlDB)
	ordersRepo := orders.NewRepository(sqlDB)
	ordersSvc := orders.NewService(
		ordersRepo, chainMgr, auditLogger, cfg.OrderExpiry,
		cfg.AmountTolerance, cfg.UnderpaymentMaxGapPercent,
		cfg.OffsetMinTenThousandths, cfg.OffsetMaxTenThousandths, cfg.MaxCollisionRetries,
		logger,
	)
	sinkProxy.svc = ordersSvc

	if err := ordersSvc.LoadPendingTransfers(ctx); err != nil {
		logger.Error("failed to reseed pending transfers on startup", "error", err)
		os.Exit(1)
	}

	ordersLimiter := ratelimit.NewLimiter(redisClient, cfg.OrdersRateLimitPerMinute, time.Minute, logger)
	publicLimiter := ratelimit.NewLimiter(redisClient, cfg.PublicRateLimitPerMinute, time.Minute, logger)
	router := api.NewRouter(ordersSvc, chainMgr, addresses, cfg.APIKey, ordersLimiter, publicLimiter, logger)

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
