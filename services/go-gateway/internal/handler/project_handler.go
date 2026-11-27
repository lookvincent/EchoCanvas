package handler

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/lookvincent/EchoCanvas/services/go-gateway/internal/model"
	"github.com/lookvincent/EchoCanvas/services/go-gateway/internal/repository"
	"github.com/lookvincent/EchoCanvas/services/go-gateway/internal/response"
)

// ProjectHandler handles project CRUD endpoints.
type ProjectHandler struct {
	repo repository.ProjectRepository
}

// NewProjectHandler constructs a ProjectHandler.
func NewProjectHandler(repo repository.ProjectRepository) *ProjectHandler {
	return &ProjectHandler{repo: repo}
}

// Create handles POST /api/v1/projects.
func (h *ProjectHandler) Create(c *gin.Context) {
	var req model.CreateProjectRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, http.StatusBadRequest, response.CodeInvalidParam, "invalid JSON payload")
		return
	}
	if err := req.NormalizeAndValidate(); err != nil {
		response.Fail(c, http.StatusBadRequest, response.CodeInvalidParam, err.Error())
		return
	}

	proj, err := h.repo.Create(c.Request.Context(), req)
	if err != nil {
		response.Fail(c, http.StatusInternalServerError, response.CodeInternal, "failed to create project")
		return
	}
	response.Created(c, proj)
}

// List handles GET /api/v1/projects.
func (h *ProjectHandler) List(c *gin.Context) {
	limit := 20
	offset := 0
	if raw := c.Query("limit"); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil || v < 1 || v > 100 {
			response.Fail(c, http.StatusBadRequest, response.CodeInvalidParam, "limit must be between 1 and 100")
			return
		}
		limit = v
	}
	if raw := c.Query("offset"); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil || v < 0 {
			response.Fail(c, http.StatusBadRequest, response.CodeInvalidParam, "offset must be non-negative")
			return
		}
		offset = v
	}

	items, total, err := h.repo.List(c.Request.Context(), limit, offset)
	if err != nil {
		response.Fail(c, http.StatusInternalServerError, response.CodeInternal, "failed to list projects")
		return
	}
	response.OK(c, model.ProjectListResponse{
		Items:  items,
		Total:  total,
		Limit:  limit,
		Offset: offset,
	})
}

// Get handles GET /api/v1/projects/:id.
func (h *ProjectHandler) Get(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		response.Fail(c, http.StatusBadRequest, response.CodeInvalidParam, "invalid project id")
		return
	}

	proj, err := h.repo.GetByID(c.Request.Context(), id)
	if errors.Is(err, repository.ErrNotFound) {
		response.Fail(c, http.StatusNotFound, response.CodeNotFound, "project not found")
		return
	}
	if err != nil {
		response.Fail(c, http.StatusInternalServerError, response.CodeInternal, "failed to get project")
		return
	}
	response.OK(c, proj)
}

// Update handles PUT /api/v1/projects/:id.
func (h *ProjectHandler) Update(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		response.Fail(c, http.StatusBadRequest, response.CodeInvalidParam, "invalid project id")
		return
	}

	var req model.UpdateProjectRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, http.StatusBadRequest, response.CodeInvalidParam, "invalid JSON payload")
		return
	}
	if err := req.Validate(); err != nil {
		response.Fail(c, http.StatusBadRequest, response.CodeInvalidParam, err.Error())
		return
	}

	proj, err := h.repo.Update(c.Request.Context(), id, req)
	if errors.Is(err, repository.ErrNotFound) {
		response.Fail(c, http.StatusNotFound, response.CodeNotFound, "project not found")
		return
	}
	if err != nil {
		response.Fail(c, http.StatusInternalServerError, response.CodeInternal, "failed to update project")
		return
	}
	response.OK(c, proj)
}

// Delete handles DELETE /api/v1/projects/:id.
func (h *ProjectHandler) Delete(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		response.Fail(c, http.StatusBadRequest, response.CodeInvalidParam, "invalid project id")
		return
	}

	err = h.repo.Delete(c.Request.Context(), id)
	if errors.Is(err, repository.ErrNotFound) {
		response.Fail(c, http.StatusNotFound, response.CodeNotFound, "project not found")
		return
	}
	if err != nil {
		response.Fail(c, http.StatusInternalServerError, response.CodeInternal, "failed to delete project")
		return
	}
	response.OK(c, gin.H{"deleted": true, "id": id.String()})
}
