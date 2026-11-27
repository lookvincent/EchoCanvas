package handler

import (
	"log/slog"

	"github.com/gin-gonic/gin"
	"github.com/lookvincent/EchoCanvas/services/go-gateway/internal/middleware"
	"github.com/lookvincent/EchoCanvas/services/go-gateway/internal/repository"
)

// NewRouter builds the Gin engine with middleware and v1 routes.
func NewRouter(
	logger *slog.Logger,
	pgPinger Pinger,
	projectRepo repository.ProjectRepository,
	assetRepo repository.AssetRepository,
) *gin.Engine {
	r := gin.New()
	r.Use(gin.Recovery(), middleware.Trace(logger))

	health := NewHealthHandler(pgPinger)
	projects := NewProjectHandler(projectRepo)
	assets := NewAssetHandler(projectRepo, assetRepo)

	r.GET("/healthz", health.Check)

	v1 := r.Group("/api/v1")
	{
		v1.POST("/projects", projects.Create)
		v1.GET("/projects", projects.List)
		v1.GET("/projects/:id", projects.Get)
		v1.PUT("/projects/:id", projects.Update)
		v1.DELETE("/projects/:id", projects.Delete)

		v1.GET("/projects/:id/assets", assets.ListByProject)
		v1.POST("/projects/:id/assets", assets.Create)
		v1.PUT("/assets/:assetId", assets.Update)
	}

	return r
}
