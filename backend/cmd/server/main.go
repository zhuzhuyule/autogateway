package main

import (
	"github.com/gin-gonic/gin"
	"github.com/zhuzhuyule/b4a/internal/autoroute"
	"github.com/zhuzhuyule/b4a/internal/router"
)

func main() {
	r := gin.Default()

	cfg := &router.AutoRouteConfig{
		Enabled:          true,
		SimpleThreshold:  2000,
		ComplexThreshold: 8000,
		GroupMapping: map[string]autoroute.GroupMapping{
			"gpt-4o": {
				SimpleGroup:  "gpt-4o-lite",
				MediumGroup:  "gpt-4o-pro",
				ComplexGroup: "gpt-4.1-max",
			},
			"claude": {
				SimpleGroup:  "claude-haiku",
				MediumGroup:  "claude-sonnet",
				ComplexGroup: "claude-opus",
			},
		},
	}

	router.RegisterAutoRoute(r, cfg)

	r.Run(":8080")
}
