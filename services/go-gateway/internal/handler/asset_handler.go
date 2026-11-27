package handler

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/lookvincent/EchoCanvas/services/go-gateway/internal/model"
	"github.com/lookvincent/EchoCanvas/services/go-gateway/internal/repository"
	"github.com/lookvincent/EchoCanvas/services/go-gateway/internal/response"
)

// AssetHandler handles asset_style_profiles endpoints.
type AssetHandler struct {
	projectRepo repository.ProjectRepository
	assetRepo   repository.AssetRepository
}

// NewAssetHandler constructs an AssetHandler.
func NewAssetHandler(projectRepo repository.ProjectRepository, assetRepo repository.AssetRepository) *AssetHandler {
	return &AssetHandler{
		projectRepo: projectRepo,
		assetRepo:   assetRepo,
	}
}

// ListByProject handles GET /api/v1/projects/:id/assets.
func (h *AssetHandler) ListByProject(c *gin.Context) {
	projectID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		response.Fail(c, http.StatusBadRequest, response.CodeInvalidParam, "invalid project id")
		return
	}

	if _, err := h.projectRepo.GetByID(c.Request.Context(), projectID); errors.Is(err, repository.ErrNotFound) {
		response.Fail(c, http.StatusNotFound, response.CodeNotFound, "project not found")
		return
	} else if err != nil {
		response.Fail(c, http.StatusInternalServerError, response.CodeInternal, "failed to verify project")
		return
	}

	items, err := h.assetRepo.ListByProject(c.Request.Context(), projectID)
	if err != nil {
		response.Fail(c, http.StatusInternalServerError, response.CodeInternal, "failed to list assets")
		return
	}
	response.OK(c, gin.H{"items": items})
}

// Create handles POST /api/v1/projects/:id/assets.
func (h *AssetHandler) Create(c *gin.Context) {
	projectID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		response.Fail(c, http.StatusBadRequest, response.CodeInvalidParam, "invalid project id")
		return
	}

	var req model.CreateAssetRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, http.StatusBadRequest, response.CodeInvalidParam, "invalid JSON payload")
		return
	}
	if err := req.NormalizeAndValidate(); err != nil {
		response.Fail(c, http.StatusBadRequest, response.CodeInvalidParam, err.Error())
		return
	}

	if _, err := h.projectRepo.GetByID(c.Request.Context(), projectID); errors.Is(err, repository.ErrNotFound) {
		response.Fail(c, http.StatusNotFound, response.CodeNotFound, "project not found")
		return
	} else if err != nil {
		response.Fail(c, http.StatusInternalServerError, response.CodeInternal, "failed to verify project")
		return
	}

	asset, err := h.assetRepo.Create(c.Request.Context(), projectID, req)
	if errors.Is(err, repository.ErrConflict) {
		response.Fail(c, http.StatusConflict, response.CodeConflict, "asset entity_key already exists in project")
		return
	}
	if errors.Is(err, repository.ErrNotFound) {
		response.Fail(c, http.StatusNotFound, response.CodeNotFound, "project not found")
		return
	}
	if err != nil {
		response.Fail(c, http.StatusInternalServerError, response.CodeInternal, "failed to create asset")
		return
	}
	response.Created(c, asset)
}

// Update handles PUT /api/v1/assets/:assetId.
func (h *AssetHandler) Update(c *gin.Context) {
	assetID, err := uuid.Parse(c.Param("assetId"))
	if err != nil {
		response.Fail(c, http.StatusBadRequest, response.CodeInvalidParam, "invalid asset id")
		return
	}

	var req model.UpdateAssetRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, http.StatusBadRequest, response.CodeInvalidParam, "invalid JSON payload")
		return
	}
	if err := req.Validate(); err != nil {
		response.Fail(c, http.StatusBadRequest, response.CodeInvalidParam, err.Error())
		return
	}

	asset, err := h.assetRepo.Update(c.Request.Context(), assetID, req)
	if errors.Is(err, repository.ErrNotFound) {
		response.Fail(c, http.StatusNotFound, response.CodeNotFound, "asset not found")
		return
	}
	if err != nil {
		response.Fail(c, http.StatusInternalServerError, response.CodeInternal, "failed to update asset")
		return
	}
	response.OK(c, asset)
}
