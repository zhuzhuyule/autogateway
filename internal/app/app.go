// Package app provides the main application logic and lifecycle management.
package app

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"autogateway/internal/config"
	db "autogateway/internal/db/migrations"
	"autogateway/internal/handler"
	"autogateway/internal/i18n"
	"autogateway/internal/keypool"
	"autogateway/internal/models"
	"autogateway/internal/proxy"
	"autogateway/internal/services"
	"autogateway/internal/store"
	"autogateway/internal/types"
	"autogateway/internal/version"

	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
	"go.uber.org/dig"
	"gorm.io/gorm"
)

// App holds all services and manages the application lifecycle.
type App struct {
	engine                *gin.Engine
	configManager         types.ConfigManager
	settingsManager       *config.SystemSettingsManager
	groupManager          *services.GroupManager
	aggregateGroupService *services.AggregateGroupService
	aliasService          *services.AliasService
	freeModelsRegistry    *services.FreeModelsRegistry
	logCleanupService     *services.LogCleanupService
	modelRefreshService   *services.ModelRefreshService
	requestLogService     *services.RequestLogService
	cronChecker           *keypool.CronChecker
	keyPoolProvider       *keypool.KeyProvider
	proxyServer           *proxy.ProxyServer
	syncPeerManager       *services.SyncPeerManager
	syncHandler           *handler.SyncHandler
	videoTaskWorker       *services.VideoTaskWorker
	storage               store.Store
	db                    *gorm.DB
	httpServer            *http.Server
}

// AppParams defines the dependencies for the App.
type AppParams struct {
	dig.In
	Engine                *gin.Engine
	ConfigManager         types.ConfigManager
	SettingsManager       *config.SystemSettingsManager
	GroupManager          *services.GroupManager
	AggregateGroupService *services.AggregateGroupService
	AliasService          *services.AliasService
	FreeModelsRegistry    *services.FreeModelsRegistry
	LogCleanupService     *services.LogCleanupService
	ModelRefreshService   *services.ModelRefreshService
	RequestLogService     *services.RequestLogService
	CronChecker           *keypool.CronChecker
	KeyPoolProvider       *keypool.KeyProvider
	ProxyServer           *proxy.ProxyServer
	SyncPeerManager       *services.SyncPeerManager
	SyncHandler           *handler.SyncHandler
	VideoTaskWorker       *services.VideoTaskWorker
	Storage               store.Store
	DB                    *gorm.DB
}

// NewApp is the constructor for App, with dependencies injected by dig.
func NewApp(params AppParams) *App {
	return &App{
		engine:                params.Engine,
		configManager:         params.ConfigManager,
		settingsManager:       params.SettingsManager,
		groupManager:          params.GroupManager,
		aggregateGroupService: params.AggregateGroupService,
		aliasService:          params.AliasService,
		freeModelsRegistry:    params.FreeModelsRegistry,
		logCleanupService:     params.LogCleanupService,
		modelRefreshService:   params.ModelRefreshService,
		requestLogService:     params.RequestLogService,
		cronChecker:           params.CronChecker,
		keyPoolProvider:       params.KeyPoolProvider,
		proxyServer:           params.ProxyServer,
		syncPeerManager:       params.SyncPeerManager,
		syncHandler:           params.SyncHandler,
		videoTaskWorker:       params.VideoTaskWorker,
		storage:               params.Storage,
		db:                    params.DB,
	}
}

// Start runs the application, it is a non-blocking call.
func (a *App) Start() error {
	// 初始化 i18n
	if err := i18n.Init(); err != nil {
		return fmt.Errorf("failed to initialize i18n: %w", err)
	}
	logrus.Info("i18n initialized successfully.")

	// 把 env AUTH_KEY 缓存为 bootstrap (master / slave 都需要,
	// DB 里 auth_key 为空时由它兜底)
	config.SetBootstrapAuthKey(a.configManager.GetAuthConfig().Key)

	// Master 节点执行初始化
	if a.configManager.IsMaster() {
		logrus.Info("Starting as Master Node.")

		if err := a.storage.Clear(); err != nil {
			return fmt.Errorf("cache cleanup failed: %w", err)
		}

		// 数据库迁移
		db.HandleLegacyIndexes(a.db)
		// Pre-migrate dedup: must run BEFORE AutoMigrate so the new
		// idx_alias_group_model unique index can be created without conflicts.
		if err := db.V1_2_0_DedupModelAliases(a.db); err != nil {
			return fmt.Errorf("pre-migrate dedup model_aliases failed: %w", err)
		}
		if err := a.db.AutoMigrate(
			&models.SystemSetting{},
			&models.Group{},
			&models.GroupSubGroup{},
			&models.APIKey{},
			&models.RequestLog{},
			&models.GroupHourlyStat{},
			&models.ModelAlias{},
			&models.SyncPeer{},
			&models.SyncLog{},
			&models.VideoTask{},
		); err != nil {
			return fmt.Errorf("database auto-migration failed: %w", err)
		}
		// Drop the legacy auto_routing config (Model Routing rewrite §13: hard cut).
		if a.db.Migrator().HasColumn(&models.SystemSetting{}, "auto_routing_config") {
			if err := a.db.Migrator().DropColumn(&models.SystemSetting{}, "auto_routing_config"); err != nil {
				logrus.WithError(err).Warn("dropping auto_routing_config column failed (non-fatal)")
			}
		}
		// 数据修复
		if err := db.MigrateDatabase(a.db); err != nil {
			return fmt.Errorf("database data migration failed: %w", err)
		}
		// P11.19: replace legacy UNIQUE(name) with partial unique (active rows only).
		// Lets soft-deleted groups co-exist with a same-named new group.
		// Must run AFTER AutoMigrate (gorm creates the legacy index there).
		if err := db.V2_5_17_PartialUniqueGroupName(a.db); err != nil {
			return fmt.Errorf("V2_5_17 partial unique groups.name failed: %w", err)
		}
		logrus.Info("Database auto-migration completed.")

		// 初始化系统设置
		if err := a.settingsManager.EnsureSettingsInitialized(a.configManager.GetAuthConfig()); err != nil {
			return fmt.Errorf("failed to initialize system settings: %w", err)
		}
		logrus.Info("System settings initialized in DB.")

		// 确保 3 个系统默认聚合分组存在(default-openai/gemini/anthropic)
		// 共享 AUTH_KEY 作为 proxy_keys,实现"一个 key 调所有 default"
		if err := a.aggregateGroupService.EnsureSystemAggregates(
			context.Background(),
			a.configManager.GetAuthConfig().Key,
		); err != nil {
			logrus.WithError(err).Warn("ensure system aggregates failed (non-fatal)")
		}
		// 把启用本特性之前就存在的 standard 分组挂入对应系统默认聚合
		a.aggregateGroupService.BackfillSystemAggregates(context.Background())

		// Seed reserved aliases (auto-simple/medium/complex placeholders).
		if err := a.aliasService.EnsureReservedSeeded(context.Background()); err != nil {
			logrus.WithError(err).Warn("seed reserved aliases failed (non-fatal)")
		}

		// Pull the free-models registry from the public CDN; cached on disk
		// for offline restart, refreshed every 6h. Non-fatal if upstream is
		// unreachable — frontend has a static fallback list.
		a.freeModelsRegistry.Start(context.Background())

		// 注册 GORM 回调，以便在配置变更时触发同步推送。
		// 通过 services.IsSyncMerge 判断当前事务是否由合并触发,如是则短路防止 A→B→A 回环。
		notifyFunc := func(db *gorm.DB) {
			if db.Error != nil || db.Statement == nil || db.Statement.Schema == nil {
				return
			}
			if services.IsSyncMerge(db.Statement.Context) {
				return
			}
			table := db.Statement.Schema.Table
			if table == "system_settings" || table == "groups" || table == "group_sub_groups" || table == "model_aliases" || table == "api_keys" {
				a.syncPeerManager.NotifyChange()
			}
		}
		a.db.Callback().Create().After("gorm:create").Register("sync_notify", notifyFunc)
		a.db.Callback().Update().After("gorm:update").Register("sync_notify", notifyFunc)
		a.db.Callback().Delete().After("gorm:delete").Register("sync_notify", notifyFunc)

		a.settingsManager.Initialize(a.storage, a.groupManager, a.configManager.IsMaster())

		// 从数据库加载密钥到 Redis
		if err := a.keyPoolProvider.LoadKeysFromDB(); err != nil {
			return fmt.Errorf("failed to load keys into key pool: %w", err)
		}
		logrus.Debug("API keys loaded into Redis cache by master.")

		// 仅 Master 节点启动的服务
		a.requestLogService.Start()
		a.logCleanupService.Start()
		a.modelRefreshService.Start()
		a.cronChecker.Start()
		// 启动同步管理器前清理 30 天以上的 sync_logs, 避免无限增长
		a.syncPeerManager.PurgeOldLogs(30)
		// Gap 3: 双向 mesh 注入 broadcaster, push 时既推 client 持有的 conn,
		// 也推 ws server 端持有的 conn.
		a.syncPeerManager.SetBroadcaster(a.syncHandler)
		a.syncPeerManager.Start(context.Background())
	} else {
		logrus.Info("Starting as Slave Node.")
		a.settingsManager.Initialize(a.storage, a.groupManager, a.configManager.IsMaster())
		a.syncPeerManager.SetBroadcaster(a.syncHandler)
		a.syncPeerManager.Start(context.Background())
	}

	// 视频任务后台 worker 在所有节点启动 —— 与 cronChecker 那种 master-only
	// 服务不同: video_tasks 的任务级租约(DB 条件 UPDATE)保证同一任务只被一个
	// 实例执行, 且某实例崩溃后其过期租约任务可被其它节点重新 claim 接管。这正是
	// spec 选择"任务级租约而非全局 leader"的目的; master-only 会让接管落空。
	// (多实例接管依赖共享 DB/Redis 部署 —— P9 mesh 的标准形态。)
	a.videoTaskWorker.Start()

	// 显示配置并启动所有后台服务
	a.configManager.DisplayServerConfig()

	a.groupManager.Initialize()

	// Create HTTP server
	serverConfig := a.configManager.GetEffectiveServerConfig()
	a.httpServer = &http.Server{
		Addr:           fmt.Sprintf("%s:%d", serverConfig.Host, serverConfig.Port),
		Handler:        a.engine,
		ReadTimeout:    time.Duration(serverConfig.ReadTimeout) * time.Second,
		WriteTimeout:   time.Duration(serverConfig.WriteTimeout) * time.Second,
		IdleTimeout:    time.Duration(serverConfig.IdleTimeout) * time.Second,
		MaxHeaderBytes: 1 << 20,
	}

	// Start HTTP server in a new goroutine
	go func() {
		logrus.Infof("AutoGateway proxy server started successfully on Version: %s", version.Version)
		logrus.Infof("Server address: http://%s:%d", serverConfig.Host, serverConfig.Port)
		logrus.Info("")
		if err := a.httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logrus.Fatalf("Server startup failed: %v", err)
		}
	}()

	return nil
}

// Stop gracefully shuts down the application.
func (a *App) Stop(ctx context.Context) {
	logrus.Info("Shutting down server...")

	serverConfig := a.configManager.GetEffectiveServerConfig()
	totalTimeout := time.Duration(serverConfig.GracefulShutdownTimeout) * time.Second

	// 动态计算 HTTP 关机超时时间，为后台服务固定预留 5 秒
	httpShutdownTimeout := totalTimeout - 5*time.Second
	httpShutdownCtx, cancelHttpShutdown := context.WithTimeout(context.Background(), httpShutdownTimeout)
	defer cancelHttpShutdown()

	logrus.Debugf("Attempting to gracefully shut down HTTP server (max %v)...", httpShutdownTimeout)
	if err := a.httpServer.Shutdown(httpShutdownCtx); err != nil {
		logrus.Debugf("HTTP server graceful shutdown timed out as expected, forcing remaining connections to close.")
		if closeErr := a.httpServer.Close(); closeErr != nil {
			logrus.Errorf("Error forcing HTTP server to close: %v", closeErr)
		}
	}
	logrus.Info("HTTP server has been shut down.")

	// 使用原始的总超时 context 继续关闭其他后台服务
	stoppableServices := []func(context.Context){
		a.groupManager.Stop,
		a.settingsManager.Stop,
		// 视频 worker 在所有节点启动, 故所有节点都要 Stop(与 Start 对称)。
		a.videoTaskWorker.Stop,
	}

	if serverConfig.IsMaster {
		stoppableServices = append(stoppableServices,
			a.cronChecker.Stop,
			a.logCleanupService.Stop,
			a.modelRefreshService.Stop,
			a.requestLogService.Stop,
		)
	}

	var wg sync.WaitGroup
	wg.Add(len(stoppableServices))

	for _, stopFunc := range stoppableServices {
		go func(stop func(context.Context)) {
			defer wg.Done()
			stop(ctx)
		}(stopFunc)
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		logrus.Info("All background services stopped.")
	case <-ctx.Done():
		logrus.Warn("Shutdown timed out, some services may not have stopped gracefully.")
	}

	if a.storage != nil {
		a.storage.Close()
	}

	logrus.Info("Server exited gracefully")
}
