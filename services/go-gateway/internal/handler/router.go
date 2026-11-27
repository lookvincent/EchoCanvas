package handler

import (
	"log/slog"

	"github.com/gin-gonic/gin"
	"github.com/lookvincent/EchoCanvas/services/go-gateway/internal/middleware"
	"github.com/lookvincent/EchoCanvas/services/go-gateway/internal/repository"
)

// RouterDeps bundles handlers and dependencies for constructing the HTTP router.
type RouterDeps struct {
	Logger            *slog.Logger
	PGPinger          Pinger
	ProjectRepo       repository.ProjectRepository
	AssetRepo         repository.AssetRepository
	JobHandler        *JobHandler
	SSEHandler        *SSEHandler
	GatewayHandler    *GatewayHandler
	InternalAuthToken string
}

// NewRouter builds the Gin engine for legacy Project/Asset routes.
func NewRouter(
	logger *slog.Logger,
	pgPinger Pinger,
	projectRepo repository.ProjectRepository,
	assetRepo repository.AssetRepository,
) *gin.Engine {
	return NewRouterWithDeps(RouterDeps{
		Logger:      logger,
		PGPinger:    pgPinger,
		ProjectRepo: projectRepo,
		AssetRepo:   assetRepo,
	})
}

// NewRouterWithDeps builds the Gin engine with middleware and all configured route groups.
func NewRouterWithDeps(deps RouterDeps) *gin.Engine {
	r := gin.New()
	r.Use(gin.Recovery(), middleware.Trace(deps.Logger))

	health := NewHealthHandler(deps.PGPinger)
	projects := NewProjectHandler(deps.ProjectRepo)
	assets := NewAssetHandler(deps.ProjectRepo, deps.AssetRepo)

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

		if deps.JobHandler != nil {
			v1.POST("/projects/:id/jobs", deps.JobHandler.CreateJob)
			v1.GET("/jobs/:jobId", deps.JobHandler.GetJob)
			v1.POST("/jobs/:jobId/gates/:gateNo/confirm", deps.JobHandler.ConfirmGate)
			v1.POST("/frames/:frameId/regenerate", deps.JobHandler.RegenerateFrame)
			v1.PUT("/frames/:frameId/active-version", deps.JobHandler.SetActiveVersion)
			v1.POST("/frame-tasks/:taskId/retry", deps.JobHandler.RetryFrameTask)
		}
		if deps.SSEHandler != nil {
			v1.GET("/jobs/:jobId/events", deps.SSEHandler.StreamJobEvents)
		}
		if deps.GatewayHandler != nil {
			v1.GET("/jobs/:jobId/usage", deps.GatewayHandler.GetJobUsage)
			v1.POST("/jobs/:jobId/budget/approve", deps.GatewayHandler.ApproveBudget)
		}
	}

	if deps.GatewayHandler != nil {
		proxy := r.Group("/proxy/v1")
		proxy.Use(middleware.InternalAuth(deps.InternalAuthToken))
		{
			proxy.POST("/chat/completions", deps.GatewayHandler.ProxyChatCompletions)
			proxy.POST("/images/generations", deps.GatewayHandler.ProxyImageGenerations)
		}
	}

	return r
}
