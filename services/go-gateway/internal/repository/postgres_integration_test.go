package repository

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lookvincent/EchoCanvas/services/go-gateway/internal/config"
	"github.com/lookvincent/EchoCanvas/services/go-gateway/internal/model"
)

func openIntegrationPool(t *testing.T) (*PostgresProjectRepository, *PostgresAssetRepository, func()) {
	t.Helper()
	strict := os.Getenv("RUN_PG_INTEGRATION") == "1"

	cfg, err := config.Load()
	if err != nil {
		if strict {
			t.Fatalf("RUN_PG_INTEGRATION=1 requires database config: %v", err)
		}
		t.Skipf("skipping postgres integration test: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	pool, err := NewPool(ctx, cfg.DatabaseURL)
	if err != nil {
		if strict {
			t.Fatalf("failed to connect postgres under RUN_PG_INTEGRATION=1: %v", err)
		}
		t.Skipf("skipping postgres integration test (unreachable): %v", err)
	}

	return NewPostgresProjectRepository(pool), NewPostgresAssetRepository(pool), pool.Close
}

func TestPostgresIntegration_ProjectAndAssetLifecycle(t *testing.T) {
	projRepo, assetRepo, cleanupPool := openIntegrationPool(t)
	defer cleanupPool()

	ctx := context.Background()

	// 1. Create Project
	createReq := model.CreateProjectRequest{
		Title:    "Integration Test Story",
		RawStory: "2016 snowy winter night in Beijing overpass.",
	}
	if err := createReq.NormalizeAndValidate(); err != nil {
		t.Fatalf("unexpected validation err: %v", err)
	}
	created, err := projRepo.Create(ctx, createReq)
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	// Ensure cleanup even on test failure so DB stays clean.
	t.Cleanup(func() {
		_ = projRepo.Delete(context.Background(), created.ID)
	})

	// 2. GetByID & List
	fetched, err := projRepo.GetByID(ctx, created.ID)
	if err != nil {
		t.Fatalf("get project: %v", err)
	}
	if fetched.Title != createReq.Title || fetched.TargetFrames != 4 {
		t.Fatalf("unexpected fetched project: %+v", fetched)
	}

	items, total, err := projRepo.List(ctx, 10, 0)
	if err != nil {
		t.Fatalf("list projects: %v", err)
	}
	if total < 1 || len(items) == 0 {
		t.Fatalf("expected non-empty project list, total=%d", total)
	}

	// 3. Update Project & verify updated_at trigger advances
	time.Sleep(15 * time.Millisecond)
	newFrames := 6
	updated, err := projRepo.Update(ctx, created.ID, model.UpdateProjectRequest{TargetFrames: &newFrames})
	if err != nil {
		t.Fatalf("update project: %v", err)
	}
	if updated.TargetFrames != 6 || !updated.UpdatedAt.After(created.UpdatedAt) {
		t.Fatalf("expected target_frames=6 and updated_at advanced: before=%v after=%v", created.UpdatedAt, updated.UpdatedAt)
	}

	// 4. Create Asset
	assetReq := model.CreateAssetRequest{
		AssetType:   "CHARACTER",
		EntityKey:   "hero_linshen",
		DisplayName: "Lin Shen",
		ProfileJSON: json.RawMessage(`{"coat":"black wool trench coat","mood":"melancholy"}`),
	}
	if err := assetReq.NormalizeAndValidate(); err != nil {
		t.Fatalf("asset validate: %v", err)
	}
	asset, err := assetRepo.Create(ctx, created.ID, assetReq)
	if err != nil {
		t.Fatalf("create asset: %v", err)
	}
	if asset.Source != "agent_inferred" {
		t.Fatalf("expected default source agent_inferred, got %s", asset.Source)
	}

	// 5. Duplicate entity_key in same project must return ErrConflict
	_, dupErr := assetRepo.Create(ctx, created.ID, assetReq)
	if !errors.Is(dupErr, ErrConflict) {
		t.Fatalf("expected ErrConflict on duplicate entity_key, got %v", dupErr)
	}

	// 6. Update Asset -> must flip source to user_ground_truth
	locked := true
	newName := "Lin Shen (Locked)"
	updatedAsset, err := assetRepo.Update(ctx, asset.ID, model.UpdateAssetRequest{
		DisplayName: &newName,
		IsLocked:    &locked,
	})
	if err != nil {
		t.Fatalf("update asset: %v", err)
	}
	if updatedAsset.Source != "user_ground_truth" || !updatedAsset.IsLocked {
		t.Fatalf("expected user_ground_truth and is_locked=true, got %+v", updatedAsset)
	}

	// 7. Delete Project must cascade delete its assets
	if err := projRepo.Delete(ctx, created.ID); err != nil {
		t.Fatalf("delete project: %v", err)
	}
	_, getDeletedAssetErr := assetRepo.GetByID(ctx, asset.ID)
	if !errors.Is(getDeletedAssetErr, ErrNotFound) {
		t.Fatalf("expected ErrNotFound for cascade-deleted asset, got %v", getDeletedAssetErr)
	}
}

func TestPostgresIntegration_JobIdempotencyAndOptimisticLock(t *testing.T) {
	projRepo, _, cleanupPool := openIntegrationPool(t)
	defer cleanupPool()

	cfg, _ := config.Load()
	ctx := context.Background()
	pool, err := NewPool(ctx, cfg.DatabaseURL)
	if err != nil {
		t.Fatalf("connect pool: %v", err)
	}
	defer pool.Close()
	jobRepo := NewPostgresJobRepository(pool)

	p1Req := model.CreateProjectRequest{Title: "Job Repo Test 1", RawStory: "Story 1"}
	_ = p1Req.NormalizeAndValidate()
	p1, err := projRepo.Create(ctx, p1Req)
	if err != nil {
		t.Fatalf("create p1: %v", err)
	}
	t.Cleanup(func() { _ = projRepo.Delete(context.Background(), p1.ID) })

	p2Req := model.CreateProjectRequest{Title: "Job Repo Test 2", RawStory: "Story 2"}
	_ = p2Req.NormalizeAndValidate()
	p2, err := projRepo.Create(ctx, p2Req)
	if err != nil {
		t.Fatalf("create p2: %v", err)
	}
	t.Cleanup(func() { _ = projRepo.Delete(context.Background(), p2.ID) })

	idemKey := "pg-idem-" + uuid.NewString()
	job1, created1, err := jobRepo.CreateOrGetByIdempotencyKey(ctx, p1.ID, idemKey, "th_"+uuid.NewString(), "tr_pg1", 1.0)
	if err != nil || !created1 {
		t.Fatalf("expected created1=true, err=nil; got created=%v err=%v", created1, err)
	}

	jobDup, createdDup, err := jobRepo.CreateOrGetByIdempotencyKey(ctx, p1.ID, idemKey, "th_"+uuid.NewString(), "tr_pg2", 1.0)
	if err != nil || createdDup || jobDup.ID != job1.ID {
		t.Fatalf("expected existing job %s with created=false, got id=%s created=%v err=%v", job1.ID, jobDup.ID, createdDup, err)
	}

	_, _, crossErr := jobRepo.CreateOrGetByIdempotencyKey(ctx, p2.ID, idemKey, "th_"+uuid.NewString(), "tr_pg3", 1.0)
	if !errors.Is(crossErr, ErrConflict) {
		t.Fatalf("expected ErrConflict when reusing idempotency_key on another project, got %v", crossErr)
	}

	frame, err := jobRepo.CreateFrame(ctx, job1.ID, 1, json.RawMessage(`{"shot":"wide","camera":"eye_level"}`))
	if err != nil {
		t.Fatalf("create frame: %v", err)
	}
	task, err := jobRepo.CreateFrameTask(ctx, job1.ID, frame.ID, model.TaskTypeInitialRender, 2)
	if err != nil {
		t.Fatalf("create frame task: %v", err)
	}

	queued, err := jobRepo.TransitionTaskStatus(ctx, task.ID, model.TaskStatusPending, model.TaskStatusQueued, nil, nil)
	if err != nil || queued.Status != model.TaskStatusQueued {
		t.Fatalf("PENDING->QUEUED failed: %+v err=%v", queued, err)
	}
	running, err := jobRepo.TransitionTaskStatus(ctx, task.ID, model.TaskStatusQueued, model.TaskStatusRunning, nil, nil)
	if err != nil || running.Status != model.TaskStatusRunning || running.StartedAt == nil {
		t.Fatalf("QUEUED->RUNNING failed: %+v err=%v", running, err)
	}

	// Stale transition QUEUED -> RUNNING must fail with ErrInvalidTransition
	_, staleErr := jobRepo.TransitionTaskStatus(ctx, task.ID, model.TaskStatusQueued, model.TaskStatusRunning, nil, nil)
	if !errors.Is(staleErr, ErrInvalidTransition) {
		t.Fatalf("expected ErrInvalidTransition on stale transition, got %v", staleErr)
	}

	errCode := "TRANSIENT"
	errDetail := "timeout"
	retried, err := jobRepo.IncrementTaskRetry(ctx, task.ID, model.TaskStatusQueued, &errCode, &errDetail)
	if err != nil || retried.RetryCount != 1 {
		t.Fatalf("IncrementTaskRetry failed: %+v err=%v", retried, err)
	}
}
