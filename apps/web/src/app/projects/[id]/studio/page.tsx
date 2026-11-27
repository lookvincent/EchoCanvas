"use client";

import { useCallback, useEffect, useState } from "react";
import { useParams } from "next/navigation";
import { AssetSidebar } from "@/components/studio/AssetSidebar";
import { FrameInspector } from "@/components/studio/FrameInspector";
import { StoryboardGallery } from "@/components/studio/StoryboardGallery";
import { StudioHeader } from "@/components/studio/StudioHeader";
import {
  approveBudget,
  confirmGate,
  createJob,
  exportJob,
  getJob,
  getJobEventsUrl,
  getJobStates,
  getJobUsage,
  getProject,
  listProjectAssets,
  listProjectJobs,
  regenerateFrame,
  retryFrameTask,
  setActiveVersion,
  updateAsset,
} from "@/lib/api";
import { downloadTextFile, exportLongStripPng } from "@/lib/exportCanvas";
import type {
  AssetStyleProfile,
  EpisodicEntityState,
  FrameScript,
  JobDetailResponse,
  JobUsageSummary,
  Project,
} from "@/lib/types";

export default function StudioPage() {
  const params = useParams<{ id: string }>();
  const projectId = params.id;

  const [project, setProject] = useState<Project | null>(null);
  const [assets, setAssets] = useState<AssetStyleProfile[]>([]);
  const [jobDetail, setJobDetail] = useState<JobDetailResponse | null>(null);
  const [states, setStates] = useState<EpisodicEntityState[]>([]);
  const [usage, setUsage] = useState<JobUsageSummary | null>(null);
  const [selectedFrameId, setSelectedFrameId] = useState<string | null>(null);
  const [sseConnected, setSseConnected] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const loadJobBundle = useCallback(async (jobId: string) => {
    const [detail, usageSummary, stateList] = await Promise.all([
      getJob(jobId),
      getJobUsage(jobId).catch(() => null),
      getJobStates(jobId).catch(() => ({ items: [] })),
    ]);
    setJobDetail(detail);
    if (usageSummary) setUsage(usageSummary);
    setStates(stateList.items || []);
    if (detail.frames.length > 0) {
      setSelectedFrameId((prev) =>
        prev && detail.frames.some((f) => f.id === prev) ? prev : detail.frames[0].id,
      );
    }
  }, []);

  const refreshAll = useCallback(async () => {
    if (!projectId) return;
    try {
      const [proj, assetRes, jobsRes] = await Promise.all([
        getProject(projectId),
        listProjectAssets(projectId),
        listProjectJobs(projectId),
      ]);
      setProject(proj);
      setAssets(assetRes.items || []);
      const latestJob = jobsRes.items?.[0];
      if (latestJob) {
        await loadJobBundle(latestJob.id);
      }
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to load studio state");
    }
  }, [projectId, loadJobBundle]);

  useEffect(() => {
    void refreshAll();
  }, [refreshAll]);

  const activeJobId = jobDetail?.job.id;

  useEffect(() => {
    if (!activeJobId) return;
    const es = new EventSource(getJobEventsUrl(activeJobId));
    es.onopen = () => setSseConnected(true);
    es.onerror = () => setSseConnected(false);

    const handleRefresh = () => {
      void Promise.all([
        loadJobBundle(activeJobId),
        listProjectAssets(projectId).then((res) => setAssets(res.items || [])),
      ]);
    };

    const events = [
      "stage_changed",
      "gate_waiting",
      "frame_progress",
      "frame_completed",
      "frame_failed",
      "cost_updated",
      "job_completed",
    ];
    events.forEach((evt) => es.addEventListener(evt, handleRefresh));

    return () => {
      events.forEach((evt) => es.removeEventListener(evt, handleRefresh));
      es.close();
      setSseConnected(false);
    };
  }, [activeJobId, projectId, loadJobBundle]);

  async function handleStartJob(budgetUsd: number) {
    setBusy(true);
    setError(null);
    try {
      const job = await createJob(projectId, budgetUsd);
      await loadJobBundle(job.id);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to start job");
    } finally {
      setBusy(false);
    }
  }

  async function handleUpdateAsset(
    assetId: string,
    payload: {
      display_name?: string;
      profile_json?: Record<string, unknown>;
      reference_image_url?: string | null;
      is_locked?: boolean;
    },
  ) {
    setBusy(true);
    setError(null);
    try {
      const updated = await updateAsset(assetId, payload);
      setAssets((prev) => prev.map((a) => (a.id === assetId ? updated : a)));
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to update asset");
    } finally {
      setBusy(false);
    }
  }

  async function handleConfirmGate1() {
    if (!activeJobId) return;
    setBusy(true);
    setError(null);
    try {
      const edits = assets.map((a) => ({
        id: a.id,
        entity_key: a.entity_key,
        display_name: a.display_name,
        asset_type: a.asset_type,
        profile_json: a.profile_json,
        reference_image_url: a.reference_image_url,
        is_locked: true,
      }));
      await confirmGate(activeJobId, 1, { assets: edits });
      await loadJobBundle(activeJobId);
      const assetRes = await listProjectAssets(projectId);
      setAssets(assetRes.items || []);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to confirm Gate 1");
    } finally {
      setBusy(false);
    }
  }

  async function handleConfirmGate2(
    editedFrames: Array<Partial<FrameScript> & { id: string; frame_index: number }>,
  ) {
    if (!activeJobId) return;
    setBusy(true);
    setError(null);
    try {
      await confirmGate(activeJobId, 2, { frames: editedFrames });
      await loadJobBundle(activeJobId);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to confirm Gate 2");
    } finally {
      setBusy(false);
    }
  }

  async function handleRegenerate(
    frameId: string,
    note: string,
    cascadeDownstream: boolean,
  ) {
    if (!activeJobId) return;
    setBusy(true);
    setError(null);
    try {
      await regenerateFrame(frameId, {
        user_edit_note: note,
        cascade_downstream: cascadeDownstream,
      });
      await loadJobBundle(activeJobId);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to regenerate frame");
    } finally {
      setBusy(false);
    }
  }

  async function handleSetActiveVersion(frameId: string, versionId: string) {
    if (!activeJobId) return;
    setBusy(true);
    setError(null);
    try {
      await setActiveVersion(frameId, versionId);
      await loadJobBundle(activeJobId);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to switch version");
    } finally {
      setBusy(false);
    }
  }

  async function handleRetryTask(taskId: string) {
    if (!activeJobId) return;
    setBusy(true);
    setError(null);
    try {
      await retryFrameTask(taskId);
      await loadJobBundle(activeJobId);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to retry task");
    } finally {
      setBusy(false);
    }
  }

  async function handleApproveBudget(newLimitUsd: number) {
    if (!activeJobId) return;
    setBusy(true);
    setError(null);
    try {
      await approveBudget(activeJobId, newLimitUsd);
      await loadJobBundle(activeJobId);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to approve budget");
    } finally {
      setBusy(false);
    }
  }

  async function handleExportPng() {
    if (!jobDetail || !project) return;
    setBusy(true);
    setError(null);
    try {
      await exportLongStripPng({
        projectTitle: project.title,
        frames: jobDetail.frames,
        versions: jobDetail.versions,
        totalCostUsd: jobDetail.job.total_cost_usd,
        traceId: jobDetail.job.trace_id,
      });
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to export PNG strip");
    } finally {
      setBusy(false);
    }
  }

  async function handleExportBundle() {
    if (!activeJobId) return;
    setBusy(true);
    setError(null);
    try {
      const exp = await exportJob(activeJobId, "markdown_json");
      downloadTextFile(
        exp.filename.replace(".json", ".md"),
        exp.markdown,
        "text/markdown;charset=utf-8",
      );
      downloadTextFile(
        exp.filename,
        JSON.stringify(exp.manifest, null, 2),
        "application/json;charset=utf-8",
      );
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to export Markdown + JSON");
    } finally {
      setBusy(false);
    }
  }

  const selectedFrame =
    jobDetail?.frames.find((f) => f.id === selectedFrameId) || jobDetail?.frames[0] || null;

  return (
    <div className="space-y-4">
      <StudioHeader
        project={project}
        job={jobDetail?.job || null}
        sseConnected={sseConnected}
        busy={busy}
        onApproveBudget={handleApproveBudget}
        onExportPng={handleExportPng}
        onExportBundle={handleExportBundle}
      />

      {error && (
        <div className="rounded-xl border border-rose-500/40 bg-rose-500/10 px-4 py-2.5 text-xs text-rose-300">
          {error}
        </div>
      )}

      <div className="grid gap-4 lg:grid-cols-12">
        <div className="lg:col-span-3">
          <AssetSidebar
            assets={assets}
            job={jobDetail?.job || null}
            busy={busy}
            onUpdateAsset={handleUpdateAsset}
          />
        </div>

        <div className="lg:col-span-6">
          <StoryboardGallery
            job={jobDetail?.job || null}
            frames={jobDetail?.frames || []}
            versions={jobDetail?.versions || []}
            tasks={jobDetail?.tasks || []}
            selectedFrameId={selectedFrame?.id || null}
            busy={busy}
            onSelectFrame={setSelectedFrameId}
            onStartJob={handleStartJob}
            onConfirmGate1={handleConfirmGate1}
            onConfirmGate2={handleConfirmGate2}
            onSetActiveVersion={handleSetActiveVersion}
            onRetryTask={handleRetryTask}
          />
        </div>

        <div className="lg:col-span-3">
          <FrameInspector
            frame={selectedFrame}
            versions={jobDetail?.versions || []}
            states={states}
            usage={usage}
            busy={busy}
            onRegenerate={handleRegenerate}
          />
        </div>
      </div>
    </div>
  );
}
