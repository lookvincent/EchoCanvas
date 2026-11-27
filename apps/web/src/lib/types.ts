export interface ApiEnvelope<T> {
  code: number;
  message: string;
  trace_id: string;
  data: T;
}

export interface Project {
  id: string;
  title: string;
  raw_story: string;
  aspect_ratio: "16:9" | "9:16" | "1:1";
  style_preset: string;
  target_frames: number;
  created_at: string;
  updated_at: string;
}

export interface ProjectListResponse {
  items: Project[];
  total: number;
  limit: number;
  offset: number;
}

export interface AssetStyleProfile {
  id: string;
  project_id: string;
  asset_type: "CHARACTER" | "SCENE" | "STYLE";
  entity_key: string;
  display_name: string;
  profile_json: Record<string, unknown>;
  reference_image_url?: string | null;
  source: "user_ground_truth" | "agent_inferred";
  is_locked: boolean;
  created_at: string;
  updated_at: string;
}

export interface FrameScript {
  frame_index?: number;
  shot_type: string;
  camera_angle: string;
  composition: string;
  lighting: string;
  action: string;
  dialogue: string;
  entities?: string[];
}

export interface StoryJob {
  id: string;
  project_id: string;
  idempotency_key: string;
  langgraph_thread_id: string;
  trace_id: string;
  status:
    | "PENDING"
    | "RUNNING"
    | "WAITING_GATE"
    | "SUCCEEDED"
    | "PARTIAL_FAILED"
    | "FAILED"
    | "PAUSED_BUDGET";
  current_stage:
    | "INIT"
    | "EXTRACT_ASSETS"
    | "GATE_1"
    | "PLAN_SCRIPT"
    | "GATE_2"
    | "RENDER_FRAMES"
    | "DONE";
  budget_limit_usd: number;
  total_cost_usd: number;
  error_message?: string | null;
  created_at: string;
  updated_at: string;
}

export interface StoryFrame {
  id: string;
  job_id: string;
  frame_index: number;
  script_json: FrameScript;
  active_version_id?: string | null;
  created_at: string;
  updated_at: string;
}

export interface VLMFeedback {
  score?: number;
  character_consistent?: boolean;
  missing_props?: string[];
  fix_hint?: string;
  needs_manual_review?: boolean;
  attempts?: number;
}

export interface FrameVersion {
  id: string;
  frame_id: string;
  version_no: number;
  user_edit_note?: string | null;
  prompt_snapshot: string;
  image_url: string;
  seed?: number | null;
  vlm_score?: number | null;
  vlm_feedback?: VLMFeedback | null;
  created_at: string;
}

export interface FrameTask {
  id: string;
  job_id: string;
  frame_id: string;
  task_type: "INITIAL_RENDER" | "REGENERATE";
  status: "PENDING" | "QUEUED" | "RUNNING" | "VERIFYING" | "SUCCEEDED" | "FAILED";
  retry_count: number;
  max_retries: number;
  error_code?: string | null;
  error_detail?: string | null;
  started_at?: string | null;
  finished_at?: string | null;
  created_at: string;
}

export interface JobDetailResponse {
  job: StoryJob;
  frames: StoryFrame[];
  versions: FrameVersion[];
  tasks: FrameTask[];
}

export interface EpisodicEntityState {
  id: string;
  job_id: string;
  frame_index: number;
  entity_key: string;
  state_json: Record<string, unknown>;
  diff_from_prev?: Record<string, { from: unknown; to: unknown }> | null;
  created_at: string;
}

export interface UsageLedgerEntry {
  id: string;
  job_id: string;
  frame_id?: string | null;
  trace_id: string;
  provider: string;
  model: string;
  call_type: "LLM_TEXT" | "IMAGE_GEN" | "VLM_JUDGE" | "EMBEDDING";
  input_tokens: number;
  output_tokens: number;
  image_count: number;
  latency_ms: number;
  cost_usd: number;
  is_fallback: boolean;
  created_at: string;
}

export interface ModelUsageBreakdown {
  provider: string;
  model: string;
  call_type: string;
  call_count: number;
  input_tokens: number;
  output_tokens: number;
  image_count: number;
  cost_usd: number;
}

export interface JobUsageSummary {
  job_id: string;
  status: string;
  budget_limit_usd: number;
  total_cost_usd: number;
  breakdown: ModelUsageBreakdown[];
  entries: UsageLedgerEntry[];
}

export interface RegenerateFrameResponse {
  task: FrameTask;
  version: FrameVersion;
  cascaded_frame_indices: number[];
}

export interface ExportJobResponse {
  job_id: string;
  project_title: string;
  format: string;
  filename: string;
  markdown: string;
  manifest: Record<string, unknown>;
  total_cost_usd: number;
}
