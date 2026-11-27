package model

import (
	"fmt"
	"time"

	"github.com/google/uuid"
)

const (
	CallTypeLLMText   = "LLM_TEXT"
	CallTypeImageGen  = "IMAGE_GEN"
	CallTypeVLMJudge  = "VLM_JUDGE"
	CallTypeEmbedding = "EMBEDDING"
)

// UsageLedgerEntry maps to usage_ledger.
type UsageLedgerEntry struct {
	ID           uuid.UUID  `json:"id"`
	JobID        uuid.UUID  `json:"job_id"`
	FrameID      *uuid.UUID `json:"frame_id,omitempty"`
	TraceID      string     `json:"trace_id"`
	Provider     string     `json:"provider"`
	Model        string     `json:"model"`
	CallType     string     `json:"call_type"`
	InputTokens  int        `json:"input_tokens"`
	OutputTokens int        `json:"output_tokens"`
	ImageCount   int        `json:"image_count"`
	LatencyMS    int        `json:"latency_ms"`
	CostUSD      float64    `json:"cost_usd"`
	IsFallback   bool       `json:"is_fallback"`
	CreatedAt    time.Time  `json:"created_at"`
}

// ModelUsageBreakdown aggregates usage grouped by provider, model, and call_type.
type ModelUsageBreakdown struct {
	Provider     string  `json:"provider"`
	Model        string  `json:"model"`
	CallType     string  `json:"call_type"`
	CallCount    int     `json:"call_count"`
	InputTokens  int     `json:"input_tokens"`
	OutputTokens int     `json:"output_tokens"`
	ImageCount   int     `json:"image_count"`
	CostUSD      float64 `json:"cost_usd"`
}

// JobUsageSummary is returned by GET /api/v1/jobs/:jobId/usage.
type JobUsageSummary struct {
	JobID          uuid.UUID             `json:"job_id"`
	Status         string                `json:"status"`
	BudgetLimitUSD float64               `json:"budget_limit_usd"`
	TotalCostUSD   float64               `json:"total_cost_usd"`
	Breakdown      []ModelUsageBreakdown `json:"breakdown"`
	Entries        []UsageLedgerEntry    `json:"entries"`
}

// JobBudgetSnapshot carries pre-flight budget verification fields.
type JobBudgetSnapshot struct {
	JobID          uuid.UUID `json:"job_id"`
	Status         string    `json:"status"`
	CurrentStage   string    `json:"current_stage"`
	BudgetLimitUSD float64   `json:"budget_limit_usd"`
	TotalCostUSD   float64   `json:"total_cost_usd"`
}

// ApproveBudgetRequest defines payload for POST /api/v1/jobs/:jobId/budget/approve.
type ApproveBudgetRequest struct {
	BudgetLimitUSD float64 `json:"budget_limit_usd"`
}

// Validate checks bounds for approved budget ceiling.
func (r *ApproveBudgetRequest) Validate() error {
	if r.BudgetLimitUSD <= 0 || r.BudgetLimitUSD > 500.0 {
		return fmt.Errorf("budget_limit_usd must be > 0 and <= 500")
	}
	return nil
}
