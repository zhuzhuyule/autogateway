package router

import (
	"github.com/gin-gonic/gin"
	"github.com/zhuzhuyule/b4a/internal/autoroute"
)

type AutoRouteConfig struct {
	Enabled          bool
	SimpleThreshold  int
	ComplexThreshold int
	GroupMapping     map[string]autoroute.GroupMapping
}

func RegisterAutoRoute(r *gin.Engine, cfg *AutoRouteConfig) error {
	store := autoroute.NewMemoryConfigStore()
	configManager := autoroute.NewConfigManager(store)

	if cfg != nil {
		rc := &autoroute.RouteConfig{
			Enabled:          cfg.Enabled,
			SimpleThreshold:  cfg.SimpleThreshold,
			ComplexThreshold: cfg.ComplexThreshold,
			GroupMapping:     cfg.GroupMapping,
		}
		if err := configManager.Save(rc); err != nil {
			return err
		}
	} else {
		if err := configManager.Load(); err != nil {
			return err
		}
	}

	classifier := autoroute.NewClassifier(&autoroute.ClassifierConfig{
		SimpleTokenThreshold:   2000,
		ComplexTokenThreshold:  8000,
		ToolComplexityWeight:   500,
		VisionComplexityWeight: 1000,
	})

	configProvider := func() *autoroute.RouteConfig {
		return configManager.GetConfig()
	}

	r.Use(autoroute.Middleware(classifier, configProvider, nil))

	configAPI := autoroute.NewConfigAPI(configManager)

	api := r.Group("/api/auto-routing")
	{
		api.GET("/config", func(c *gin.Context) {
			resp := configAPI.GetConfig()
			c.JSON(200, resp)
		})

		api.POST("/config", func(c *gin.Context) {
			var req autoroute.SaveConfigRequest
			if err := c.ShouldBindJSON(&req); err != nil {
				c.JSON(400, gin.H{"error": err.Error()})
				return
			}
			resp := configAPI.SaveConfig(&req)
			c.JSON(200, resp)
		})

		api.POST("/test", func(c *gin.Context) {
			var req autoroute.TestRouteRequest
			if err := c.ShouldBindJSON(&req); err != nil {
				c.JSON(400, gin.H{"error": err.Error()})
				return
			}
			resp := configAPI.TestRoute(classifier, &req)
			c.JSON(200, resp)
		})
	}

	return nil
}

func GetConfigManager() *autoroute.ConfigManager {
	store := autoroute.NewMemoryConfigStore()
	return autoroute.NewConfigManager(store)
}
