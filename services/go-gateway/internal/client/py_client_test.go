package client

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/lookvincent/EchoCanvas/services/go-gateway/internal/model"
	"github.com/lookvincent/EchoCanvas/services/go-gateway/internal/scheduler"
)

func TestPySidecarClient_AuthTraceAndErrorMapping(t *testing.T) {
	var seenToken, seenTrace, seenPath string
	statusCode := http.StatusOK
	respBody := `{"job_id":"j1","thread_id":"th1","status":"WAITING_GATE","current_stage":"GATE_1","assets":[],"trace_id":"tr_client_1"}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenToken = r.Header.Get("X-Internal-Token")
		seenTrace = r.Header.Get("X-Trace-ID")
		seenPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(statusCode)
		_, _ = w.Write([]byte(respBody))
	}))
	defer srv.Close()

	c := NewPySidecarClient(srv.URL, "test_internal_token", nil)
	resp, err := c.ParseStory(context.Background(), ParseStoryReq{
		JobID:    "j1",
		ThreadID: "th1",
		RawStory: "story",
	}, "tr_client_1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if seenToken != "test_internal_token" || seenTrace != "tr_client_1" || seenPath != "/internal/v1/workflows/parse-story" {
		t.Fatalf("unexpected headers/path: token=%q trace=%q path=%q", seenToken, seenTrace, seenPath)
	}
	if resp.CurrentStage != "GATE_1" {
		t.Fatalf("expected GATE_1, got %s", resp.CurrentStage)
	}

	// Verify HTTP 422 maps to scheduler.ErrSafetyBlocked
	statusCode = http.StatusUnprocessableEntity
	respBody = `{"detail":"SAFETY_BLOCKED"}`
	_, safeErr := c.RegenerateFrame(context.Background(), uuid.New(), RegenerateFrameReq{UserEditNote: "test"}, "tr_2")
	if !errors.Is(safeErr, scheduler.ErrSafetyBlocked) {
		t.Fatalf("expected ErrSafetyBlocked on 422, got %v", safeErr)
	}

	// Verify HTTP 402 maps to ErrBudgetExceeded
	statusCode = http.StatusPaymentRequired
	respBody = `{"detail":"budget exceeded"}`
	_, budgetErr := c.RenderFrame(context.Background(), uuid.New(), RenderFrameReq{}, "tr_3")
	if !errors.Is(budgetErr, ErrBudgetExceeded) {
		t.Fatalf("expected ErrBudgetExceeded on 402, got %v", budgetErr)
	}

	// Verify normal RenderFrame decoding
	statusCode = http.StatusOK
	rawVer, _ := json.Marshal(FrameVersionResp{
		ID:             "ver-1",
		FrameID:        "frame-1",
		VersionNo:      1,
		PromptSnapshot: "snowy bridge",
		ImageURL:       "https://cdn.local/1.png",
	})
	respBody = string(rawVer)
	ver, err := c.RenderFrame(context.Background(), uuid.New(), RenderFrameReq{JobID: model.TaskTypeInitialRender}, "tr_4")
	if err != nil || ver.VersionNo != 1 || ver.ImageURL != "https://cdn.local/1.png" {
		t.Fatalf("unexpected render result: %+v err=%v", ver, err)
	}
}
