import type {
  ApiEnvelope,
  AssetStyleProfile,
  EpisodicEntityState,
  ExportJobResponse,
  FrameScript,
  JobDetailResponse,
  JobUsageSummary,
  Project,
  ProjectListResponse,
  RegenerateFrameResponse,
  StoryFrame,
  StoryJob,
} from "./types";

export const API_BASE_URL =
  process.env.NEXT_PUBLIC_API_BASE_URL?.replace(/\/$/, "") || "http://localhost:8080";

export class ApiError extends Error {
  status: number;
  code: number;
  traceId: string;

  constructor(message: string, status: number, code: number, traceId = "") {
    super(message);
    this.status = status;
    this.code = code;
    this.traceId = traceId;
  }
}

async function apiFetch<T>(path: string, init?: RequestInit): Promise<T> {
  const headers = new Headers(init?.headers);
  if (init?.body && !headers.has("Content-Type")) {
    headers.set("Content-Type", "application/json");
  }
  const resp = await fetch(`${API_BASE_URL}${path}`, {
    ...init,
    headers,
    cache: "no-store",
  });
  let parsed: ApiEnvelope<T> | null = null;
  try {
    parsed = (await resp.json()) as ApiEnvelope<T>;
  } catch {
    throw new ApiError(`HTTP ${resp.status} from gateway`, resp.status, resp.status * 100);
  }
  if (!resp.ok || parsed.code !== 0) {
    throw new ApiError(
      parsed.message || `Request failed (${resp.status})`,
      resp.status,
      parsed.code,
      parsed.trace_id || "",
    );
  }
  return parsed.data;
}

export async function checkHealth(): Promise<{ status: string }> {
  const resp = await fetch(`${API_BASE_URL}/healthz`, { cache: "no-store" });
  if (!resp.ok) {
    throw new Error(`healthz returned ${resp.status}`);
  }
  return (await resp.json()) as { status: string };
}

export function listProjects(limit = 30, offset = 0): Promise<ProjectListResponse> {
  return apiFetch<ProjectListResponse>(`/api/v1/projects?limit=${limit}&offset=${offset}`);
}

export function createProject(payload: {
  title: string;
  raw_story: string;
  aspect_ratio: "16:9" | "9:16" | "1:1";
  style_preset: string;
  target_frames: number;
}): Promise<Project> {
  return apiFetch<Project>("/api/v1/projects", {
    method: "POST",
    body: JSON.stringify(payload),
  });
}

export function getProject(projectId: string): Promise<Project> {
  return apiFetch<Project>(`/api/v1/projects/${projectId}`);
}

export function deleteProject(projectId: string): Promise<{ deleted: boolean }> {
  return apiFetch<{ deleted: boolean }>(`/api/v1/projects/${projectId}`, {
    method: "DELETE",
  });
}

export function listProjectAssets(projectId: string): Promise<{ items: AssetStyleProfile[] }> {
  return apiFetch<{ items: AssetStyleProfile[] }>(`/api/v1/projects/${projectId}/assets`);
}

export function updateAsset(
  assetId: string,
  payload: {
    display_name?: string;
    profile_json?: Record<string, unknown>;
    reference_image_url?: string | null;
    is_locked?: boolean;
  },
): Promise<AssetStyleProfile> {
  return apiFetch<AssetStyleProfile>(`/api/v1/assets/${assetId}`, {
    method: "PUT",
    body: JSON.stringify(payload),
  });
}

export function listProjectJobs(projectId: string): Promise<{ items: StoryJob[] }> {
  return apiFetch<{ items: StoryJob[] }>(`/api/v1/projects/${projectId}/jobs`);
}

export function createJob(
  projectId: string,
  budgetLimitUsd: number,
  idempotencyKey?: string,
): Promise<StoryJob> {
  const key =
    idempotencyKey ||
    (typeof crypto !== "undefined" && "randomUUID" in crypto
      ? `web_${crypto.randomUUID()}`
      : `web_${Date.now()}`);
  return apiFetch<StoryJob>(`/api/v1/projects/${projectId}/jobs`, {
    method: "POST",
    headers: { "X-Idempotency-Key": key },
    body: JSON.stringify({ budget_limit_usd: budgetLimitUsd }),
  });
}

export function getJob(jobId: string): Promise<JobDetailResponse> {
  return apiFetch<JobDetailResponse>(`/api/v1/jobs/${jobId}`);
}

export function confirmGate(
  jobId: string,
  gateNo: 1 | 2,
  userEdits?: {
    assets?: Array<Record<string, unknown>>;
    frames?: Array<Partial<FrameScript> & { id?: string; frame_index?: number }>;
  },
): Promise<JobDetailResponse> {
  return apiFetch<JobDetailResponse>(`/api/v1/jobs/${jobId}/gates/${gateNo}/confirm`, {
    method: "POST",
    body: JSON.stringify(userEdits ? { user_edits: userEdits } : {}),
  });
}

export function regenerateFrame(
  frameId: string,
  payload: { user_edit_note: string; cascade_downstream?: boolean; seed?: number },
): Promise<RegenerateFrameResponse> {
  return apiFetch<RegenerateFrameResponse>(`/api/v1/frames/${frameId}/regenerate`, {
    method: "POST",
    body: JSON.stringify(payload),
  });
}

export function setActiveVersion(frameId: string, versionId: string): Promise<StoryFrame> {
  return apiFetch<StoryFrame>(`/api/v1/frames/${frameId}/active-version`, {
    method: "PUT",
    body: JSON.stringify({ version_id: versionId }),
  });
}

export function retryFrameTask(taskId: string): Promise<unknown> {
  return apiFetch<unknown>(`/api/v1/frame-tasks/${taskId}/retry`, {
    method: "POST",
  });
}

export function getJobUsage(jobId: string): Promise<JobUsageSummary> {
  return apiFetch<JobUsageSummary>(`/api/v1/jobs/${jobId}/usage`);
}

export function approveBudget(jobId: string, budgetLimitUsd: number): Promise<StoryJob> {
  return apiFetch<StoryJob>(`/api/v1/jobs/${jobId}/budget/approve`, {
    method: "POST",
    body: JSON.stringify({ budget_limit_usd: budgetLimitUsd }),
  });
}

export function getJobStates(jobId: string): Promise<{ items: EpisodicEntityState[] }> {
  return apiFetch<{ items: EpisodicEntityState[] }>(`/api/v1/jobs/${jobId}/states`);
}

export function exportJob(
  jobId: string,
  format: "markdown_json" | "long_strip_manifest" = "markdown_json",
): Promise<ExportJobResponse> {
  return apiFetch<ExportJobResponse>(`/api/v1/jobs/${jobId}/export`, {
    method: "POST",
    body: JSON.stringify({ format }),
  });
}

export function getJobEventsUrl(jobId: string): string {
  return `${API_BASE_URL}/api/v1/jobs/${jobId}/events`;
}
