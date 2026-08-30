// Package main 是 TracePulse 服务的入口，负责组装各层组件并启动 HTTP 服务器。
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/example/tracepulse/config"
	"github.com/example/tracepulse/controller"
	"github.com/example/tracepulse/logger"
	"github.com/example/tracepulse/repository"
	"github.com/example/tracepulse/router"
	"github.com/example/tracepulse/service"

	"go.uber.org/zap"
)

func main() {
	config.LoadConfig()

	if err := config.InitDirectories(); err != nil {
		log.Fatalf("failed to init directories: %v", err)
	}

	logCfg := config.GetLogConfig()
	if err := logger.Init(logger.Options{
		Dir:            logCfg.Path,
		Level:          logCfg.Level,
		Mode:           logCfg.Mode,
		Levels:         logCfg.Levels,
		DisableConsole: logCfg.DisableConsole,
	}); err != nil {
		log.Fatalf("failed to init logger: %v", err)
	}
	defer logger.Sync()

	db := config.NewDatabase()
	logger.Debug("database connected")

	userRepo := repository.NewUserRepository(db)
	userService := service.NewUserService(userRepo)
	userController := controller.NewUserController(userService)
	logger.Debug("user components initialized")

	statusService := service.NewStatusService()
	statusController := controller.NewStatusController(statusService)

	traceCfg := config.GetTraceConfig()
	alertSvc := service.NewAlertService(config.GetAlertConfig())
	traceRepo := repository.NewTraceRepository(db)
	traceSvc := service.NewTraceService(traceRepo, alertSvc, traceCfg)
	traceController := controller.NewTraceController(traceSvc, int64(traceCfg.ReportMaxBodyBytes))
	logger.Debug("trace components initialized",
		zap.Int("report_max_body_bytes", traceCfg.ReportMaxBodyBytes),
	)

	// 首次启动（库里还没有任何链路）时灌入演示数据，打开页面就能看到效果。
	// 正式部署在 config/config.yaml 里把 demo.disable 设为 true 即可关闭。
	demoCfg := config.GetDemoConfig()
	logger.Debug("demo data config",
		zap.Bool("disable", demoCfg.Disable),
		zap.Bool("force", demoCfg.Force),
	)
	if !demoCfg.Disable {
		seeded, err := service.SeedDemoData(traceRepo, demoCfg.Force)
		if err != nil {
			logger.Error("failed to seed demo traces", zap.Error(err))
		}
		if seeded > 0 {
			logger.Info("demo traces seeded", zap.Int("count", seeded))
		}
	}

	r := router.NewRouter(userController, statusController, traceController)

	serverCfg := config.GetServerConfig()
	srv := &http.Server{
		Addr:              fmt.Sprintf(":%d", serverCfg.Port),
		Handler:           r,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       time.Duration(serverCfg.ReadTimeoutSeconds) * time.Second,
		WriteTimeout:      time.Duration(serverCfg.WriteTimeoutSeconds) * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	// 优雅关闭：先停 HTTP 接收新请求，再把内存里的链路全部落盘，最后停告警协程。
	logger.Debug("http server config",
		zap.Int("port", serverCfg.Port),
		zap.Int("read_timeout_seconds", serverCfg.ReadTimeoutSeconds),
		zap.Int("write_timeout_seconds", serverCfg.WriteTimeoutSeconds),
		zap.Int("shutdown_timeout_seconds", serverCfg.ShutdownTimeoutSeconds),
	)
	serverErr := make(chan error, 1)
	go func() {
		logger.Info("server starting", zap.Int("port", serverCfg.Port))
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
			return
		}
		serverErr <- nil
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	select {
	case err := <-serverErr:
		if err != nil {
			logger.Error("server exited", zap.Error(err))
		}
		shutdown(traceSvc, alertSvc, srv)
	case sig := <-quit:
		logger.Info("shutdown signal received", zap.String("signal", sig.String()))
		shutdown(traceSvc, alertSvc, srv)
	}
}

// shutdown 按依赖顺序收敛：HTTP → 链路落盘 → 告警。
func shutdown(traceSvc service.TraceService, alertSvc service.AlertService, srv *http.Server) {
	ctx, cancel := context.WithTimeout(
		context.Background(),
		time.Duration(config.GetServerConfig().ShutdownTimeoutSeconds)*time.Second,
	)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		logger.Warn("http server shutdown unfinished", zap.Error(err))
	}

	// HTTP 已停止接收，此时把队列抽干并落盘，保证不丢数据。
	traceSvc.Shutdown()
	// 告警最后停，让退出前触发的告警仍有机会发出去。
	alertSvc.Shutdown()

	logger.Sync()
}
