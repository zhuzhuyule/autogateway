package router

import (
	"embed"
	"autogateway/internal/handler"
	"autogateway/internal/i18n"
	"autogateway/internal/middleware"
	"autogateway/internal/proxy"
	"autogateway/internal/router_engine"
	"autogateway/internal/services"
	"autogateway/internal/types"
	"io/fs"
	"net/http"
	"strings"
	"time"

	"github.com/gin-contrib/gzip"
	"github.com/gin-contrib/static"

	"github.com/gin-gonic/gin"
)

type embedFileSystem struct {
	http.FileSystem
}

func (e embedFileSystem) Exists(prefix string, path string) bool {
	_, err := e.Open(path)
	return err == nil
}

func EmbedFolder(fsEmbed embed.FS, targetPath string) static.ServeFileSystem {
	efs, err := fs.Sub(fsEmbed, targetPath)
	if err != nil {
		panic(err)
	}
	return embedFileSystem{
		FileSystem: http.FS(efs),
	}
}

func NewRouter(
	serverHandler *handler.Server,
	proxyServer *proxy.ProxyServer,
	configManager types.ConfigManager,
	groupManager *services.GroupManager,
	selector *router_engine.Selector,
	aliasHandler *handler.AliasHandler,
	aliasSuggestionHandler *handler.AliasSuggestionHandler,
	routingSettingsHandler *handler.RoutingSettingsHandler,
	modelCatalogHandler *handler.ModelCatalogHandler,
	dedupHandler *handler.DedupHandler,
	upstreamProbeHandler *handler.UpstreamProbeHandler,
	freeModelsHandler *handler.FreeModelsHandler,
	buildFS embed.FS,
	indexPage []byte,
) *gin.Engine {
	gin.SetMode(gin.ReleaseMode)

	router := gin.New()

	// 注册全局中间件
	router.Use(middleware.Recovery())
	router.Use(middleware.ErrorHandler())
	router.Use(middleware.Logger(configManager.GetLogConfig()))
	router.Use(middleware.CORS(configManager.GetCORSConfig()))
	router.Use(middleware.RateLimiter(configManager.GetPerformanceConfig()))
	router.Use(middleware.SecurityHeaders())
	startTime := time.Now()
	router.Use(func(c *gin.Context) {
		c.Set("serverStartTime", startTime)
		c.Next()
	})

	// 注册路由
	registerSystemRoutes(router, serverHandler)
	registerAPIRoutes(router, serverHandler, configManager, aliasHandler, aliasSuggestionHandler, routingSettingsHandler, modelCatalogHandler, dedupHandler, upstreamProbeHandler, freeModelsHandler)
	registerProxyRoutes(router, proxyServer, groupManager, serverHandler, selector)
	registerFrontendRoutes(router, buildFS, indexPage)

	return router
}

// registerSystemRoutes 注册系统级路由
func registerSystemRoutes(router *gin.Engine, serverHandler *handler.Server) {
	router.GET("/health", serverHandler.Health)
}

// registerAPIRoutes 注册API路由
func registerAPIRoutes(
	router *gin.Engine,
	serverHandler *handler.Server,
	configManager types.ConfigManager,
	aliasHandler *handler.AliasHandler,
	aliasSuggestionHandler *handler.AliasSuggestionHandler,
	routingSettingsHandler *handler.RoutingSettingsHandler,
	modelCatalogHandler *handler.ModelCatalogHandler,
	dedupHandler *handler.DedupHandler,
	upstreamProbeHandler *handler.UpstreamProbeHandler,
	freeModelsHandler *handler.FreeModelsHandler,
) {
	api := router.Group("/api")
	api.Use(i18n.Middleware())

	// 公开
	registerPublicAPIRoutes(api, serverHandler)

	// 认证 — middleware 通过 SettingsManager 每次请求动态读 auth_key,
	// 用户在 UI 改完即时生效,无需重启
	protectedAPI := api.Group("")
	protectedAPI.Use(middleware.Auth(serverHandler.SettingsManager))
	registerProtectedAPIRoutes(protectedAPI, serverHandler, aliasHandler, aliasSuggestionHandler, routingSettingsHandler, modelCatalogHandler, dedupHandler, upstreamProbeHandler, freeModelsHandler)
}

// registerPublicAPIRoutes 公开API路由
func registerPublicAPIRoutes(api *gin.RouterGroup, serverHandler *handler.Server) {
	api.POST("/auth/login", serverHandler.Login)
	api.GET("/integration/info", serverHandler.GetIntegrationInfo)
}

// registerProtectedAPIRoutes 认证API路由
func registerProtectedAPIRoutes(
	api *gin.RouterGroup,
	serverHandler *handler.Server,
	aliasHandler *handler.AliasHandler,
	aliasSuggestionHandler *handler.AliasSuggestionHandler,
	routingSettingsHandler *handler.RoutingSettingsHandler,
	modelCatalogHandler *handler.ModelCatalogHandler,
	dedupHandler *handler.DedupHandler,
	upstreamProbeHandler *handler.UpstreamProbeHandler,
	freeModelsHandler *handler.FreeModelsHandler,
) {
	api.GET("/channel-types", serverHandler.CommonHandler.GetChannelTypes)

	// Model Routing rewrite (§13): aliases CRUD + smart-routing settings.
	aliases := api.Group("/aliases")
	{
		aliases.GET("", aliasHandler.List)
		aliases.GET("/suggestions", aliasSuggestionHandler.Suggest)
		aliases.GET("/:alias", aliasHandler.GetByAlias)
		aliases.POST("", aliasHandler.Create)
		aliases.PUT("/:id", aliasHandler.Update)
		aliases.DELETE("/:id", aliasHandler.Delete)
	}
	routing := api.Group("/routing")
	{
		routing.GET("/settings", routingSettingsHandler.Get)
		routing.PUT("/settings", routingSettingsHandler.Save)
	}

	// Model Catalog API
	api.GET("/models", modelCatalogHandler.ListModels)

	// Model Dedup API
	dedup := api.Group("/dedup")
	{
		dedup.GET("/suggestions", dedupHandler.GetSuggestions)
		dedup.POST("/create", dedupHandler.CreateAliasUnification)
	}

	api.GET("/upstream/probe", upstreamProbeHandler.Probe)
	api.GET("/freemodels/registry", freeModelsHandler.Registry)

	groups := api.Group("/groups")
	{
		groups.POST("", serverHandler.CreateGroup)
		groups.GET("", serverHandler.ListGroups)
		groups.GET("/list", serverHandler.List)
		groups.GET("/config-options", serverHandler.GetGroupConfigOptions)
		groups.PUT("/reorder", serverHandler.ReorderGroups)
		groups.PUT("/:id", serverHandler.UpdateGroup)
		groups.DELETE("/:id", serverHandler.DeleteGroup)
		groups.GET("/:id/stats", serverHandler.GetGroupStats)
		groups.POST("/:id/copy", serverHandler.CopyGroup)
		groups.POST("/:id/refresh-models", serverHandler.RefreshGroupModels)
		groups.POST("/:id/test-model", serverHandler.TestGroupModel)

		groups.GET("/:id/sub-groups", serverHandler.GetSubGroups)
		groups.POST("/:id/sub-groups", serverHandler.AddSubGroups)
		groups.PUT("/:id/sub-groups/:subGroupId/weight", serverHandler.UpdateSubGroupWeight)
		groups.DELETE("/:id/sub-groups/:subGroupId", serverHandler.DeleteSubGroup)
		groups.GET("/:id/parent-aggregate-groups", serverHandler.GetParentAggregateGroups)
	}

	// Key Management Routes
	keys := api.Group("/keys")
	{
		keys.GET("", serverHandler.ListKeysInGroup)
		keys.GET("/export", serverHandler.ExportKeys)
		keys.POST("/add-multiple", serverHandler.AddMultipleKeys)
		keys.POST("/add-async", serverHandler.AddMultipleKeysAsync)
		keys.POST("/delete-multiple", serverHandler.DeleteMultipleKeys)
		keys.POST("/delete-async", serverHandler.DeleteMultipleKeysAsync)
		keys.POST("/restore-multiple", serverHandler.RestoreMultipleKeys)
		keys.POST("/restore-all-invalid", serverHandler.RestoreAllInvalidKeys)
		keys.POST("/clear-all-invalid", serverHandler.ClearAllInvalidKeys)
		keys.POST("/clear-all", serverHandler.ClearAllKeys)
		keys.POST("/validate-group", serverHandler.ValidateGroupKeys)
		keys.POST("/test-multiple", serverHandler.TestMultipleKeys)
		keys.PUT("/:id/notes", serverHandler.UpdateKeyNotes)
	}

	// Tasks
	api.GET("/tasks/status", serverHandler.GetTaskStatus)

	// 仪表板和日志
	dashboard := api.Group("/dashboard")
	{
		dashboard.GET("/stats", serverHandler.Stats)
		dashboard.GET("/chart", serverHandler.Chart)
		dashboard.GET("/encryption-status", serverHandler.EncryptionStatus)
		dashboard.GET("/top-models", serverHandler.TopModels)
		dashboard.GET("/model-timings", serverHandler.ModelTimings)
	}

	// 日志
	logs := api.Group("/logs")
	{
		logs.GET("", serverHandler.GetLogs)
		logs.GET("/export", serverHandler.ExportLogs)
	}

	// 设置
	settings := api.Group("/settings")
	{
		settings.GET("", serverHandler.GetSettings)
		settings.PUT("", serverHandler.UpdateSettings)
	}
}

// registerProxyRoutes 注册代理路由
func registerProxyRoutes(
	router *gin.Engine,
	proxyServer *proxy.ProxyServer,
	groupManager *services.GroupManager,
	serverHandler *handler.Server,
	selector *router_engine.Selector,
) {
	mw := router_engine.Middleware(selector, groupManager)

	// 1) 标准命名路由: /proxy/{group_name}/*
	proxyGroup := router.Group("/proxy/:group_name")
	proxyGroup.Use(middleware.ProxyRouteDispatcher(serverHandler))
	proxyGroup.Use(middleware.ProxyAuth(groupManager))
	proxyGroup.Use(mw)
	proxyGroup.Any("/*path", proxyServer.HandleProxy)

	// 2) 系统默认聚合分组的快捷路由(无 /proxy 前缀): /openai/*, /gemini/*, /anthropic/*
	systemShortcuts := []struct {
		Prefix string
		Role   string
	}{
		{"/openai", "default-openai"},
		{"/gemini", "default-gemini"},
		{"/anthropic", "default-anthropic"},
	}
	for _, sc := range systemShortcuts {
		role := sc.Role
		grp := router.Group(sc.Prefix)
		grp.Use(injectSystemGroupName(groupManager, role))
		grp.Use(middleware.ProxyAuth(groupManager))
		grp.Use(mw)
		grp.Any("/*path", proxyServer.HandleProxy)
	}
}

// injectSystemGroupName 把 c.Param("group_name") 注入为对应 system_role 分组的名字.
// 失败(找不到/未初始化)返回 404,避免让请求穿透到 SPA 兜底.
func injectSystemGroupName(gm *services.GroupManager, role string) gin.HandlerFunc {
	return func(c *gin.Context) {
		group, err := gm.GetGroupBySystemRole(role)
		if err != nil || group == nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "system aggregate group not initialized: " + role})
			c.Abort()
			return
		}
		c.Params = append(c.Params, gin.Param{Key: "group_name", Value: group.Name})
		c.Next()
	}
}

// registerFrontendRoutes 注册前端路由
func registerFrontendRoutes(router *gin.Engine, buildFS embed.FS, indexPage []byte) {
	router.Use(gzip.Gzip(gzip.DefaultCompression))
	router.NoMethod(func(c *gin.Context) {
		c.JSON(http.StatusMethodNotAllowed, gin.H{"error": "Method not allowed"})
	})

	// 使用静态资源缓存中间件
	router.Use(middleware.StaticCache())

	router.Use(static.Serve("/", EmbedFolder(buildFS, "web/dist")))
	router.NoRoute(func(c *gin.Context) {
		uri := c.Request.RequestURI
		if strings.HasPrefix(uri, "/api") ||
			strings.HasPrefix(uri, "/proxy") ||
			strings.HasPrefix(uri, "/openai/") ||
			strings.HasPrefix(uri, "/gemini/") ||
			strings.HasPrefix(uri, "/anthropic/") {
			c.JSON(http.StatusNotFound, gin.H{"error": "Not Found"})
			return
		}
		// HTML页面不缓存，确保更新能及时生效
		c.Header("Cache-Control", "no-cache, no-store, must-revalidate")
		c.Header("Pragma", "no-cache")
		c.Header("Expires", "0")
		c.Data(http.StatusOK, "text/html; charset=utf-8", indexPage)
	})
}
