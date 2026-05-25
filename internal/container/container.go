// Package container provides a dependency injection container for the application.
package container

import (
	"context"

	"autogateway/internal/app"
	"autogateway/internal/backup"
	"autogateway/internal/channel"
	"autogateway/internal/config"
	"autogateway/internal/db"
	"autogateway/internal/encryption"
	"autogateway/internal/handler"
	"autogateway/internal/httpclient"
	"autogateway/internal/keypool"
	"autogateway/internal/proxy"
	"autogateway/internal/router"
	"autogateway/internal/router_engine"
	"autogateway/internal/services"
	"autogateway/internal/store"
	"autogateway/internal/types"
	"autogateway/internal/version"

	"go.uber.org/dig"
	"gorm.io/gorm"
)

// BuildContainer creates a new dependency injection container and provides all the application's services.
func BuildContainer() (*dig.Container, error) {
	container := dig.New()

	// Infrastructure Services
	if err := container.Provide(config.NewManager); err != nil {
		return nil, err
	}
	if err := container.Provide(func(configManager types.ConfigManager) (encryption.Service, error) {
		return encryption.NewService(configManager.GetEncryptionKey())
	}); err != nil {
		return nil, err
	}
	if err := container.Provide(db.NewDB); err != nil {
		return nil, err
	}
	if err := container.Provide(config.NewSystemSettingsManager); err != nil {
		return nil, err
	}
	if err := container.Provide(store.NewStore); err != nil {
		return nil, err
	}
	if err := container.Provide(httpclient.NewHTTPClientManager); err != nil {
		return nil, err
	}
	if err := container.Provide(channel.NewFactory); err != nil {
		return nil, err
	}

	// Business Services
	if err := container.Provide(services.NewTaskService); err != nil {
		return nil, err
	}
	if err := container.Provide(services.NewKeyManualValidationService); err != nil {
		return nil, err
	}
	if err := container.Provide(services.NewKeyService); err != nil {
		return nil, err
	}
	if err := container.Provide(services.NewKeyImportService); err != nil {
		return nil, err
	}
	if err := container.Provide(services.NewKeyDeleteService); err != nil {
		return nil, err
	}
	if err := container.Provide(services.NewLogService); err != nil {
		return nil, err
	}
	if err := container.Provide(services.NewLogCleanupService); err != nil {
		return nil, err
	}
	if err := container.Provide(services.NewRequestLogService); err != nil {
		return nil, err
	}
	// 把 *services.RequestLogService 暴露为 keypool.RequestLogRecorder,
	// 这样 keypool 不必反向 import services, 避免 import cycle.
	if err := container.Provide(func(s *services.RequestLogService) keypool.RequestLogRecorder {
		return s
	}); err != nil {
		return nil, err
	}
	if err := container.Provide(services.NewSubGroupManager); err != nil {
		return nil, err
	}
	if err := container.Provide(services.NewGroupManager); err != nil {
		return nil, err
	}
	if err := container.Provide(services.NewGroupService); err != nil {
		return nil, err
	}
	if err := container.Provide(services.NewAggregateGroupService); err != nil {
		return nil, err
	}
	if err := container.Provide(keypool.NewProvider); err != nil {
		return nil, err
	}
	if err := container.Provide(keypool.NewKeyValidator); err != nil {
		return nil, err
	}
	if err := container.Provide(keypool.NewCronChecker); err != nil {
		return nil, err
	}

	// Handlers
	if err := container.Provide(handler.NewServer); err != nil {
		return nil, err
	}
	if err := container.Provide(handler.NewCommonHandler); err != nil {
		return nil, err
	}
	if err := container.Provide(handler.NewUpstreamProbeHandler); err != nil {
		return nil, err
	}
	if err := container.Provide(services.NewFreeModelsRegistry); err != nil {
		return nil, err
	}
	if err := container.Provide(handler.NewFreeModelsHandler); err != nil {
		return nil, err
	}

	// Model Routing rewrite (§13): selector + alias service + handlers.
	if err := container.Provide(services.NewAliasService); err != nil {
		return nil, err
	}
	// P4 智能路由解析层: 三层链 alias > family > raw, 把 user model name
	// 展开成 candidate pool 供 proxy 选 sub-group + 改写 body.model.
	if err := container.Provide(services.NewModelResolver); err != nil {
		return nil, err
	}
	if err := container.Provide(services.NewAliasSuggestionService); err != nil {
		return nil, err
	}
	if err := container.Provide(handler.NewAliasSuggestionHandler); err != nil {
		return nil, err
	}
	if err := container.Provide(func(db *gorm.DB) *router_engine.Selector {
		return router_engine.NewSelector(db)
	}); err != nil {
		return nil, err
	}
	if err := container.Provide(handler.NewAliasHandler); err != nil {
		return nil, err
	}
	if err := container.Provide(handler.NewRoutingSettingsHandler); err != nil {
		return nil, err
	}
	if err := container.Provide(handler.NewModelCatalogHandler); err != nil {
		return nil, err
	}

	// Model Dedup Service
	if err := container.Provide(services.NewModelDedupService); err != nil {
		return nil, err
	}
	if err := container.Provide(handler.NewDedupHandler); err != nil {
		return nil, err
	}

	// Backup/Restore (config 一键备份恢复)
	if err := container.Provide(func(
		db *gorm.DB,
		enc encryption.Service,
		groupManager *services.GroupManager,
		aggSvc *services.AggregateGroupService,
		settingsManager *config.SystemSettingsManager,
		keyProvider *keypool.KeyProvider,
	) *backup.Service {
		svc := backup.NewService(db, enc, version.Version)
		// 1) Backfill: 让新导入的 standard 分组自动挂回 default-openai/gemini/anthropic 等系统聚合。
		svc = svc.WithInvalidator(func() error {
			aggSvc.BackfillSystemAggregates(context.Background())
			return nil
		})
		// 2) Invalidate GroupManager 缓存：下次请求重新从 DB 加载分组配置。
		svc = svc.WithInvalidator(groupManager.Invalidate)
		// 3) Invalidate SystemSettingsManager：让 app_url/auth_key 等 SystemSetting
		//    在所有节点立刻生效，不需要重启。
		svc = svc.WithInvalidator(settingsManager.Invalidate)
		// 4) Reload in-memory key pool from DB: import 写入的新 key 必须立刻
		//    出现在 keypool 里，否则 SelectKey 还命中 boot 时的快照 → NO_KEYS_AVAILABLE.
		svc = svc.WithInvalidator(keyProvider.LoadKeysFromDB)
		return svc
	}); err != nil {
		return nil, err
	}
	if err := container.Provide(func(svc *backup.Service, sm *config.SystemSettingsManager) *handler.BackupHandler {
		return handler.NewBackupHandler(svc).WithSettingsManager(sm)
	}); err != nil {
		return nil, err
	}

	// Proxy & Router
	if err := container.Provide(proxy.NewProxyServer); err != nil {
		return nil, err
	}
	if err := container.Provide(router.NewRouter); err != nil {
		return nil, err
	}

	// Application Layer
	if err := container.Provide(app.NewApp); err != nil {
		return nil, err
	}

	return container, nil
}
