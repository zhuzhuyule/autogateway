// Package container provides a dependency injection container for the application.
package container

import (
	"autogateway/internal/app"
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

	// Model Routing rewrite (§13): selector + alias service + handlers.
	if err := container.Provide(services.NewAliasService); err != nil {
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
