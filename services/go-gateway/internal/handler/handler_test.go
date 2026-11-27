package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/lookvincent/EchoCanvas/services/go-gateway/internal/model"
	"github.com/lookvincent/EchoCanvas/services/go-gateway/internal/repository"
	"github.com/lookvincent/EchoCanvas/services/go-gateway/internal/response"
)

type fakePinger struct {
	err error
}

func (f fakePinger) Ping(context.Context) error { return f.err }

type fakeProjectRepo struct {
	mu       sync.Mutex
	projects map[uuid.UUID]model.Project
}

func newFakeProjectRepo() *fakeProjectRepo {
	return &fakeProjectRepo{projects: make(map[uuid.UUID]model.Project)}
}

func (f *fakeProjectRepo) Create(_ context.Context, req model.CreateProjectRequest) (model.Project, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	now := time.Now().UTC()
	p := model.Project{
		ID:           uuid.New(),
		Title:        req.Title,
		RawStory:     req.RawStory,
		AspectRatio:  req.AspectRatio,
		StylePreset:  req.StylePreset,
		TargetFrames: *req.TargetFrames,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	f.projects[p.ID] = p
	return p, nil
}

func (f *fakeProjectRepo) GetByID(_ context.Context, id uuid.UUID) (model.Project, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.projects[id]
	if !ok {
		return model.Project{}, repository.ErrNotFound
	}
	return p, nil
}

func (f *fakeProjectRepo) List(_ context.Context, limit, offset int) ([]model.Project, int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	all := make([]model.Project, 0, len(f.projects))
	for _, p := range f.projects {
		all = append(all, p)
	}
	total := int64(len(all))
	if offset >= len(all) {
		return []model.Project{}, total, nil
	}
	end := offset + limit
	if end > len(all) {
		end = len(all)
	}
	return all[offset:end], total, nil
}

func (f *fakeProjectRepo) Update(_ context.Context, id uuid.UUID, req model.UpdateProjectRequest) (model.Project, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.projects[id]
	if !ok {
		return model.Project{}, repository.ErrNotFound
	}
	if req.Title != nil {
		p.Title = *req.Title
	}
	if req.RawStory != nil {
		p.RawStory = *req.RawStory
	}
	if req.AspectRatio != nil {
		p.AspectRatio = *req.AspectRatio
	}
	if req.StylePreset != nil {
		p.StylePreset = *req.StylePreset
	}
	if req.TargetFrames != nil {
		p.TargetFrames = *req.TargetFrames
	}
	p.UpdatedAt = time.Now().UTC()
	f.projects[id] = p
	return p, nil
}

func (f *fakeProjectRepo) Delete(_ context.Context, id uuid.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.projects[id]; !ok {
		return repository.ErrNotFound
	}
	delete(f.projects, id)
	return nil
}

type fakeAssetRepo struct {
	mu     sync.Mutex
	assets map[uuid.UUID]model.AssetStyleProfile
}

func newFakeAssetRepo() *fakeAssetRepo {
	return &fakeAssetRepo{assets: make(map[uuid.UUID]model.AssetStyleProfile)}
}

func (f *fakeAssetRepo) ListByProject(_ context.Context, projectID uuid.UUID) ([]model.AssetStyleProfile, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]model.AssetStyleProfile, 0)
	for _, a := range f.assets {
		if a.ProjectID == projectID {
			out = append(out, a)
		}
	}
	return out, nil
}

func (f *fakeAssetRepo) Create(_ context.Context, projectID uuid.UUID, req model.CreateAssetRequest) (model.AssetStyleProfile, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, existing := range f.assets {
		if existing.ProjectID == projectID && existing.EntityKey == req.EntityKey {
			return model.AssetStyleProfile{}, repository.ErrConflict
		}
	}
	now := time.Now().UTC()
	a := model.AssetStyleProfile{
		ID:                uuid.New(),
		ProjectID:         projectID,
		AssetType:         req.AssetType,
		EntityKey:         req.EntityKey,
		DisplayName:       req.DisplayName,
		ProfileJSON:       req.ProfileJSON,
		ReferenceImageURL: req.ReferenceImageURL,
		Source:            req.Source,
		IsLocked:          *req.IsLocked,
		CreatedAt:         now,
		UpdatedAt:         now,
	}
	f.assets[a.ID] = a
	return a, nil
}

func (f *fakeAssetRepo) GetByID(_ context.Context, id uuid.UUID) (model.AssetStyleProfile, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	a, ok := f.assets[id]
	if !ok {
		return model.AssetStyleProfile{}, repository.ErrNotFound
	}
	return a, nil
}

func (f *fakeAssetRepo) Update(_ context.Context, id uuid.UUID, req model.UpdateAssetRequest) (model.AssetStyleProfile, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	a, ok := f.assets[id]
	if !ok {
		return model.AssetStyleProfile{}, repository.ErrNotFound
	}
	if req.DisplayName != nil {
		a.DisplayName = *req.DisplayName
	}
	if req.ProfileJSON != nil {
		a.ProfileJSON = *req.ProfileJSON
	}
	if req.ReferenceImageURL != nil {
		a.ReferenceImageURL = req.ReferenceImageURL
	}
	if req.IsLocked != nil {
		a.IsLocked = *req.IsLocked
	}
	a.Source = "user_ground_truth"
	a.UpdatedAt = time.Now().UTC()
	f.assets[id] = a
	return a, nil
}

func setupTestRouter(pingErr error) (*gin.Engine, *fakeProjectRepo, *fakeAssetRepo) {
	gin.SetMode(gin.TestMode)
	pRepo := newFakeProjectRepo()
	aRepo := newFakeAssetRepo()
	r := NewRouter(nil, fakePinger{err: pingErr}, pRepo, aRepo)
	return r, pRepo, aRepo
}

func TestHealthAndTraceMiddleware(t *testing.T) {
	r, _, _ := setupTestRouter(nil)

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if got := w.Header().Get("X-Trace-ID"); !strings.HasPrefix(got, "tr_") {
		t.Fatalf("expected generated X-Trace-ID prefix tr_, got %q", got)
	}

	reqCustom := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	reqCustom.Header.Set("X-Trace-ID", "tr_custom_123456")
	wCustom := httptest.NewRecorder()
	r.ServeHTTP(wCustom, reqCustom)
	if got := wCustom.Header().Get("X-Trace-ID"); got != "tr_custom_123456" {
		t.Fatalf("expected propagated trace id, got %q", got)
	}

	rErr, _, _ := setupTestRouter(errors.New("db down"))
	wErr := httptest.NewRecorder()
	rErr.ServeHTTP(wErr, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if wErr.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 when db down, got %d", wErr.Code)
	}
}

func TestProjectCRUDAndValidation(t *testing.T) {
	r, _, _ := setupTestRouter(nil)

	// Invalid payload -> 40001
	wBad := httptest.NewRecorder()
	reqBad := httptest.NewRequest(http.MethodPost, "/api/v1/projects", bytes.NewBufferString(`{"title":"","raw_story":"hello"}`))
	reqBad.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(wBad, reqBad)
	if wBad.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", wBad.Code)
	}
	var badEnv response.Envelope
	_ = json.Unmarshal(wBad.Body.Bytes(), &badEnv)
	if badEnv.Code != response.CodeInvalidParam {
		t.Fatalf("expected code %d, got %d", response.CodeInvalidParam, badEnv.Code)
	}

	// Create valid project -> 201
	wCreate := httptest.NewRecorder()
	reqCreate := httptest.NewRequest(http.MethodPost, "/api/v1/projects", bytes.NewBufferString(`{"title":"Beijing Overpass","raw_story":"Snowy winter night in 2016."}`))
	reqCreate.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(wCreate, reqCreate)
	if wCreate.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d body=%s", wCreate.Code, wCreate.Body.String())
	}

	var createResp struct {
		Code int           `json:"code"`
		Data model.Project `json:"data"`
	}
	if err := json.Unmarshal(wCreate.Body.Bytes(), &createResp); err != nil {
		t.Fatal(err)
	}
	if createResp.Data.AspectRatio != "16:9" || createResp.Data.TargetFrames != 4 {
		t.Fatalf("unexpected defaults: %+v", createResp.Data)
	}
	id := createResp.Data.ID.String()

	// Get non-existent -> 404
	w404 := httptest.NewRecorder()
	r.ServeHTTP(w404, httptest.NewRequest(http.MethodGet, "/api/v1/projects/"+uuid.NewString(), nil))
	if w404.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w404.Code)
	}

	// Update -> 200
	wUpd := httptest.NewRecorder()
	reqUpd := httptest.NewRequest(http.MethodPut, "/api/v1/projects/"+id, bytes.NewBufferString(`{"target_frames":6}`))
	reqUpd.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(wUpd, reqUpd)
	if wUpd.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", wUpd.Code)
	}

	// Delete -> 200
	wDel := httptest.NewRecorder()
	r.ServeHTTP(wDel, httptest.NewRequest(http.MethodDelete, "/api/v1/projects/"+id, nil))
	if wDel.Code != http.StatusOK {
		t.Fatalf("expected 200 on delete, got %d", wDel.Code)
	}
}

func TestAssetEndpointsEnforceGroundTruthOnUpdate(t *testing.T) {
	r, _, _ := setupTestRouter(nil)

	wCreate := httptest.NewRecorder()
	reqCreate := httptest.NewRequest(http.MethodPost, "/api/v1/projects", bytes.NewBufferString(`{"title":"Dream","raw_story":"Night bridge"}`))
	reqCreate.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(wCreate, reqCreate)
	var projResp struct {
		Data model.Project `json:"data"`
	}
	_ = json.Unmarshal(wCreate.Body.Bytes(), &projResp)
	projectID := projResp.Data.ID.String()

	// Create asset -> source defaults to agent_inferred
	wAsset := httptest.NewRecorder()
	reqAsset := httptest.NewRequest(http.MethodPost, "/api/v1/projects/"+projectID+"/assets", bytes.NewBufferString(`{
		"asset_type":"CHARACTER",
		"entity_key":"hero_lin",
		"display_name":"Lin Shen",
		"profile_json":{"coat":"black trench coat"}
	}`))
	reqAsset.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(wAsset, reqAsset)
	if wAsset.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d body=%s", wAsset.Code, wAsset.Body.String())
	}
	var assetResp struct {
		Data model.AssetStyleProfile `json:"data"`
	}
	_ = json.Unmarshal(wAsset.Body.Bytes(), &assetResp)
	if assetResp.Data.Source != "agent_inferred" {
		t.Fatalf("expected agent_inferred, got %s", assetResp.Data.Source)
	}

	// Update & lock asset -> source must flip to user_ground_truth
	wPut := httptest.NewRecorder()
	reqPut := httptest.NewRequest(http.MethodPut, "/api/v1/assets/"+assetResp.Data.ID.String(), bytes.NewBufferString(`{"is_locked":true,"display_name":"Lin Shen (Locked)"}`))
	reqPut.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(wPut, reqPut)
	if wPut.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", wPut.Code, wPut.Body.String())
	}
	var updatedResp struct {
		Data model.AssetStyleProfile `json:"data"`
	}
	_ = json.Unmarshal(wPut.Body.Bytes(), &updatedResp)
	if updatedResp.Data.Source != "user_ground_truth" || !updatedResp.Data.IsLocked {
		t.Fatalf("expected user_ground_truth and locked=true, got %+v", updatedResp.Data)
	}
}
