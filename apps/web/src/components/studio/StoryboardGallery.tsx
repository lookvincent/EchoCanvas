"use client";

import { useEffect, useState } from "react";
import {
  AlertCircle,
  CheckCircle2,
  Loader2,
  Play,
  RefreshCw,
  ShieldAlert,
  Sparkles,
} from "lucide-react";
import type {
  FrameScript,
  FrameTask,
  FrameVersion,
  StoryFrame,
  StoryJob,
} from "@/lib/types";

interface StoryboardGalleryProps {
  job: StoryJob | null;
  frames: StoryFrame[];
  versions: FrameVersion[];
  tasks: FrameTask[];
  selectedFrameId: string | null;
  busy: boolean;
  onSelectFrame: (frameId: string) => void;
  onStartJob: (budgetUsd: number) => Promise<void>;
  onConfirmGate1: () => Promise<void>;
  onConfirmGate2: (editedFrames: Array<Partial<FrameScript> & { id: string; frame_index: number }>) => Promise<void>;
  onSetActiveVersion: (frameId: string, versionId: string) => Promise<void>;
  onRetryTask: (taskId: string) => Promise<void>;
}

export function StoryboardGallery({
  job,
  frames,
  versions,
  tasks,
  selectedFrameId,
  busy,
  onSelectFrame,
  onStartJob,
  onConfirmGate1,
  onConfirmGate2,
  onSetActiveVersion,
  onRetryTask,
}: StoryboardGalleryProps) {
  const [budgetInput, setBudgetInput] = useState("0.50");
  const [scriptDrafts, setScriptDrafts] = useState<Record<string, FrameScript>>({});

  useEffect(() => {
    const next: Record<string, FrameScript> = {};
    for (const f of frames) {
      next[f.id] = { ...f.script_json };
    }
    setScriptDrafts(next);
  }, [frames]);

  const sortedFrames = [...frames].sort((a, b) => a.frame_index - b.frame_index);
  const isGate1Waiting = job?.status === "WAITING_GATE" && job.current_stage === "GATE_1";
  const isGate2Waiting = job?.status === "WAITING_GATE" && job.current_stage === "GATE_2";

  function updateDraft(frameId: string, field: keyof FrameScript, value: string) {
    setScriptDrafts((prev) => {
      const curr = prev[frameId];
      if (!curr) return prev;
      return {
        ...prev,
        [frameId]: { ...curr, [field]: value },
      };
    });
  }

  async function handleGate2Confirm() {
    const payload = sortedFrames.map((f) => {
      const draft = scriptDrafts[f.id] || f.script_json;
      return {
        id: f.id,
        frame_index: f.frame_index,
        shot_type: draft.shot_type,
        camera_angle: draft.camera_angle,
        composition: draft.composition,
        lighting: draft.lighting,
        action: draft.action,
        dialogue: draft.dialogue,
      };
    });
    await onConfirmGate2(payload);
  }

  if (!job) {
    return (
      <section className="flex flex-col items-center justify-center rounded-2xl border border-dashed border-[var(--border-subtle)] bg-[var(--bg-panel)] p-10 text-center">
        <div className="flex h-12 w-12 items-center justify-center rounded-2xl bg-indigo-500/20 text-indigo-300">
          <Sparkles className="h-6 w-6" />
        </div>
        <h2 className="mt-4 text-lg font-semibold text-white">Launch Asynchronous Storyboard Job</h2>
        <p className="mt-1.5 max-w-md text-xs leading-relaxed text-slate-400">
          EchoCanvas extracts characters & scenes first, pauses at Gate 1 for ground-truth locking, and pauses at Gate 2 for script review before spending Vision compute.
        </p>

        <div className="mt-6 flex items-center gap-3">
          <label className="text-xs text-slate-300">Budget Cap (USD):</label>
          <input
            type="number"
            step="0.1"
            min="0.1"
            max="50"
            value={budgetInput}
            onChange={(e) => setBudgetInput(e.target.value)}
            className="w-24 rounded-lg border border-[var(--border-subtle)] bg-[#0b0d12] px-3 py-1.5 font-mono text-sm text-white"
          />
          <button
            type="button"
            disabled={busy}
            onClick={() => void onStartJob(Number(budgetInput) || 0.5)}
            className="flex items-center gap-2 rounded-xl bg-indigo-600 px-5 py-2 text-sm font-medium text-white transition hover:bg-indigo-500 disabled:opacity-50"
          >
            <Play className="h-4 w-4" />
            {busy ? "Starting..." : "Extract Assets (Start Job)"}
          </button>
        </div>
      </section>
    );
  }

  return (
    <section className="space-y-4">
      {isGate1Waiting && (
        <div className="flex flex-wrap items-center justify-between gap-4 rounded-2xl border border-amber-500/40 bg-amber-500/10 p-4">
          <div>
            <h2 className="text-sm font-semibold text-amber-200">
              Gate 1: Verify Character & Scene Assets
            </h2>
            <p className="mt-0.5 text-xs text-amber-100/80">
              Review extracted assets in the left sidebar and lock any edits as{" "}
              <code className="rounded bg-black/30 px-1">user_ground_truth</code> before planning scripts.
            </p>
          </div>
          <button
            type="button"
            disabled={busy}
            onClick={() => void onConfirmGate1()}
            className="flex items-center gap-2 rounded-xl bg-amber-500 px-4 py-2 text-xs font-semibold text-slate-950 transition hover:bg-amber-400 disabled:opacity-50"
          >
            {busy ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : <CheckCircle2 className="h-3.5 w-3.5" />}
            Lock Assets & Plan Storyboard (Gate 1)
          </button>
        </div>
      )}

      {isGate2Waiting && (
        <div className="flex flex-wrap items-center justify-between gap-4 rounded-2xl border border-amber-500/40 bg-amber-500/10 p-4">
          <div>
            <h2 className="text-sm font-semibold text-amber-200">
              Gate 2: Review & Edit Frame Scripts Before Image Rendering
            </h2>
            <p className="mt-0.5 text-xs text-amber-100/80">
              Tweak camera angles, actions, or dialogue below. Frame 1 renders first to establish character visual anchoring.
            </p>
          </div>
          <button
            type="button"
            disabled={busy}
            onClick={() => void handleGate2Confirm()}
            className="flex items-center gap-2 rounded-xl bg-amber-500 px-4 py-2 text-xs font-semibold text-slate-950 transition hover:bg-amber-400 disabled:opacity-50"
          >
            {busy ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : <Play className="h-3.5 w-3.5" />}
            Confirm Scripts & Render Frames (Gate 2)
          </button>
        </div>
      )}

      {sortedFrames.length === 0 ? (
        <div className="flex flex-col items-center justify-center rounded-2xl border border-[var(--border-subtle)] bg-[var(--bg-panel)] p-12 text-center">
          <Loader2 className="h-6 w-6 animate-spin text-indigo-400" />
          <p className="mt-3 text-xs text-slate-400">
            Workflow stage: <span className="font-mono text-slate-200">{job.current_stage}</span>
          </p>
        </div>
      ) : (
        <div className="grid gap-4 sm:grid-cols-2">
          {sortedFrames.map((frame) => {
            const frameVersions = versions
              .filter((v) => v.frame_id === frame.id)
              .sort((a, b) => a.version_no - b.version_no);
            const activeVer =
              frameVersions.find((v) => v.id === frame.active_version_id) ||
              frameVersions[frameVersions.length - 1];
            const latestTask = [...tasks]
              .reverse()
              .find((t) => t.frame_id === frame.id);
            const selected = selectedFrameId === frame.id;
            const draft = scriptDrafts[frame.id] || frame.script_json;
            const needsReview = Boolean(activeVer?.vlm_feedback?.needs_manual_review);

            return (
              <div
                key={frame.id}
                onClick={() => onSelectFrame(frame.id)}
                className={`flex cursor-pointer flex-col justify-between rounded-2xl border bg-[var(--bg-panel)] p-3.5 transition ${
                  selected
                    ? "border-indigo-500 ring-1 ring-indigo-500/40"
                    : "border-[var(--border-subtle)] hover:border-slate-600"
                }`}
              >
                <div>
                  <div className="mb-2.5 flex items-center justify-between gap-2">
                    <div className="flex items-center gap-2">
                      <span className="rounded-md bg-indigo-500/20 px-2 py-0.5 font-mono text-xs font-semibold text-indigo-300">
                        Frame {String(frame.frame_index).padStart(2, "0")}
                      </span>
                      {frame.frame_index === 1 && (
                        <span className="rounded bg-emerald-500/15 px-1.5 py-0.5 text-[10px] text-emerald-300">
                          Anchor
                        </span>
                      )}
                    </div>

                    <div className="flex items-center gap-1.5">
                      {activeVer?.vlm_score != null && (
                        <span
                          className={`rounded-full px-2 py-0.5 font-mono text-[10px] font-medium ${
                            needsReview
                              ? "bg-amber-500/20 text-amber-300"
                              : "bg-emerald-500/15 text-emerald-300"
                          }`}
                        >
                          VLM {activeVer.vlm_score.toFixed(1)}
                          {needsReview ? " · Review" : ""}
                        </span>
                      )}
                      {latestTask && (
                        <span className="rounded bg-slate-800 px-1.5 py-0.5 font-mono text-[10px] text-slate-300">
                          {latestTask.status}
                        </span>
                      )}
                    </div>
                  </div>

                  {isGate2Waiting ? (
                    <div
                      className="space-y-2 rounded-xl border border-[var(--border-subtle)] bg-[var(--bg-canvas)] p-3"
                      onClick={(e) => e.stopPropagation()}
                    >
                      <div className="grid grid-cols-2 gap-2">
                        <input
                          type="text"
                          value={draft.shot_type}
                          onChange={(e) => updateDraft(frame.id, "shot_type", e.target.value)}
                          placeholder="Shot type"
                          className="rounded border border-[var(--border-subtle)] bg-[var(--bg-panel)] px-2 py-1 text-xs text-white"
                        />
                        <input
                          type="text"
                          value={draft.camera_angle}
                          onChange={(e) => updateDraft(frame.id, "camera_angle", e.target.value)}
                          placeholder="Camera angle"
                          className="rounded border border-[var(--border-subtle)] bg-[var(--bg-panel)] px-2 py-1 text-xs text-white"
                        />
                      </div>
                      <textarea
                        rows={3}
                        value={draft.action}
                        onChange={(e) => updateDraft(frame.id, "action", e.target.value)}
                        placeholder="Frame action description"
                        className="w-full rounded border border-[var(--border-subtle)] bg-[var(--bg-panel)] p-2 text-xs text-white"
                      />
                      <input
                        type="text"
                        value={draft.dialogue}
                        onChange={(e) => updateDraft(frame.id, "dialogue", e.target.value)}
                        placeholder="Optional dialogue / narration"
                        className="w-full rounded border border-[var(--border-subtle)] bg-[var(--bg-panel)] px-2 py-1 text-xs text-slate-200"
                      />
                    </div>
                  ) : (
                    <>
                      <div className="relative aspect-video overflow-hidden rounded-xl border border-[var(--border-subtle)] bg-[#0b0d12]">
                        {activeVer ? (
                          <img
                            src={activeVer.image_url}
                            alt={`Frame ${frame.frame_index}`}
                            className="h-full w-full object-cover"
                          />
                        ) : latestTask?.status === "FAILED" ? (
                          <div className="flex h-full flex-col items-center justify-center gap-2 p-4 text-center">
                            <ShieldAlert className="h-6 w-6 text-rose-400" />
                            <p className="text-xs text-rose-300">
                              {latestTask.error_code || "Render Failed"}
                            </p>
                            <button
                              type="button"
                              onClick={(e) => {
                                e.stopPropagation();
                                void onRetryTask(latestTask.id);
                              }}
                              className="flex items-center gap-1 rounded-lg bg-rose-500/20 px-3 py-1 text-xs text-rose-200 hover:bg-rose-500/30"
                            >
                              <RefreshCw className="h-3 w-3" /> Retry Frame
                            </button>
                          </div>
                        ) : (
                          <div className="flex h-full flex-col items-center justify-center gap-2 text-slate-500">
                            <Loader2 className="h-5 w-5 animate-spin text-indigo-400" />
                            <span className="font-mono text-[11px]">
                              {latestTask?.status || "Awaiting Render"}
                            </span>
                          </div>
                        )}
                      </div>

                      <div className="mt-2.5 space-y-1">
                        <div className="text-[11px] font-medium text-indigo-300">
                          {frame.script_json.shot_type} · {frame.script_json.camera_angle}
                        </div>
                        <p className="line-clamp-2 text-xs text-slate-300">
                          {frame.script_json.action}
                        </p>
                        {frame.script_json.dialogue && (
                          <p className="line-clamp-1 text-xs italic text-slate-400">
                            “{frame.script_json.dialogue}”
                          </p>
                        )}
                      </div>
                    </>
                  )}
                </div>

                {frameVersions.length > 0 && (
                  <div
                    className="mt-3 flex flex-wrap items-center gap-1.5 border-t border-[var(--border-subtle)] pt-2.5"
                    onClick={(e) => e.stopPropagation()}
                  >
                    <span className="text-[10px] text-slate-500">Versions:</span>
                    {frameVersions.map((ver) => {
                      const isActive = activeVer?.id === ver.id;
                      return (
                        <button
                          key={ver.id}
                          type="button"
                          disabled={busy}
                          onClick={() => void onSetActiveVersion(frame.id, ver.id)}
                          className={`rounded-md px-2 py-0.5 font-mono text-[10px] transition ${
                            isActive
                              ? "bg-indigo-600 text-white"
                              : "bg-slate-800 text-slate-300 hover:bg-slate-700"
                          }`}
                        >
                          v{ver.version_no}
                          {ver.vlm_score != null ? ` (${ver.vlm_score.toFixed(1)})` : ""}
                        </button>
                      );
                    })}
                  </div>
                )}

                {latestTask?.status === "FAILED" && activeVer && (
                  <div className="mt-2 flex items-center justify-between rounded bg-rose-500/10 px-2 py-1 text-[10px] text-rose-300">
                    <span className="flex items-center gap-1 truncate">
                      <AlertCircle className="h-3 w-3 shrink-0" />
                      {latestTask.error_detail || latestTask.error_code}
                    </span>
                  </div>
                )}
              </div>
            );
          })}
        </div>
      )}
    </section>
  );
}
