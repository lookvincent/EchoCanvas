"use client";

import Link from "next/link";
import { useState } from "react";
import {
  AlertTriangle,
  ArrowLeft,
  CheckCircle2,
  Coins,
  Download,
  FileText,
  ImageDown,
  Loader2,
  Radio,
} from "lucide-react";
import type { Project, StoryJob } from "@/lib/types";

interface StudioHeaderProps {
  project: Project | null;
  job: StoryJob | null;
  sseConnected: boolean;
  busy: boolean;
  onApproveBudget: (newLimitUsd: number) => Promise<void>;
  onExportPng: () => Promise<void>;
  onExportBundle: () => Promise<void>;
}

export function StudioHeader({
  project,
  job,
  sseConnected,
  busy,
  onApproveBudget,
  onExportPng,
  onExportBundle,
}: StudioHeaderProps) {
  const [showBudgetModal, setShowBudgetModal] = useState(false);
  const [newBudgetInput, setNewBudgetInput] = useState("1.50");

  const budgetLimit = job?.budget_limit_usd ?? 0.5;
  const totalCost = job?.total_cost_usd ?? 0;
  const pct = budgetLimit > 0 ? Math.min(100, (totalCost / budgetLimit) * 100) : 0;
  const nearLimit = pct >= 80;
  const pausedBudget = job?.status === "PAUSED_BUDGET";

  const statusTone =
    job?.status === "SUCCEEDED"
      ? "border-emerald-500/40 bg-emerald-500/15 text-emerald-300"
      : job?.status === "WAITING_GATE"
        ? "border-amber-500/40 bg-amber-500/15 text-amber-300"
        : job?.status === "PAUSED_BUDGET" || job?.status === "FAILED"
          ? "border-rose-500/40 bg-rose-500/15 text-rose-300"
          : "border-cyan-500/40 bg-cyan-500/15 text-cyan-300";

  async function submitBudget(e: React.FormEvent) {
    e.preventDefault();
    const parsed = Number(newBudgetInput);
    if (Number.isNaN(parsed) || parsed <= totalCost) return;
    await onApproveBudget(parsed);
    setShowBudgetModal(false);
  }

  return (
    <div className="space-y-3">
      <div className="flex flex-wrap items-center justify-between gap-4 rounded-2xl border border-[var(--border-subtle)] bg-[var(--bg-panel)] px-4 py-3">
        <div className="flex items-center gap-3">
          <Link
            href="/projects"
            className="flex h-8 w-8 items-center justify-center rounded-lg border border-[var(--border-subtle)] text-slate-400 transition hover:text-white"
            title="Back to Projects"
          >
            <ArrowLeft className="h-4 w-4" />
          </Link>
          <div>
            <div className="flex items-center gap-2.5">
              <h1 className="text-base font-semibold text-white">
                {project?.title || "Loading Studio..."}
              </h1>
              {project && (
                <span className="rounded bg-slate-800 px-2 py-0.5 font-mono text-[11px] text-slate-300">
                  {project.aspect_ratio} · {project.target_frames}F · {project.style_preset}
                </span>
              )}
            </div>
            {job && (
              <div className="mt-0.5 flex flex-wrap items-center gap-2 text-[11px] text-slate-400">
                <span className="font-mono">Trace: {job.trace_id}</span>
                <span>·</span>
                <span className="inline-flex items-center gap-1">
                  <Radio
                    className={`h-3 w-3 ${sseConnected ? "text-emerald-400" : "text-slate-500"}`}
                  />
                  {sseConnected ? "SSE Connected" : "SSE Standby"}
                </span>
              </div>
            )}
          </div>
        </div>

        <div className="flex flex-wrap items-center gap-3">
          {job && (
            <div className={`flex items-center gap-1.5 rounded-full border px-3 py-1 text-xs font-medium ${statusTone}`}>
              {job.status === "RUNNING" ? (
                <Loader2 className="h-3.5 w-3.5 animate-spin" />
              ) : job.status === "SUCCEEDED" ? (
                <CheckCircle2 className="h-3.5 w-3.5" />
              ) : (
                <AlertTriangle className="h-3.5 w-3.5" />
              )}
              <span>
                {job.status} · {job.current_stage}
              </span>
            </div>
          )}

          {job && (
            <button
              type="button"
              onClick={() => {
                setNewBudgetInput((budgetLimit + 1.0).toFixed(2));
                setShowBudgetModal((v) => !v);
              }}
              className="flex items-center gap-2 rounded-xl border border-[var(--border-subtle)] bg-[var(--bg-elevated)] px-3 py-1.5 text-xs"
            >
              <Coins className={`h-3.5 w-3.5 ${nearLimit ? "text-amber-400" : "text-emerald-400"}`} />
              <span className="font-mono text-white">
                ${totalCost.toFixed(4)} / ${budgetLimit.toFixed(2)}
              </span>
              <span className="h-1.5 w-14 overflow-hidden rounded-full bg-slate-800">
                <span
                  className={`block h-full ${pausedBudget ? "bg-rose-500" : nearLimit ? "bg-amber-400" : "bg-emerald-400"}`}
                  style={{ width: `${pct}%` }}
                />
              </span>
            </button>
          )}

          {job && (
            <div className="flex items-center gap-1.5">
              <button
                type="button"
                disabled={busy}
                onClick={() => void onExportPng()}
                className="flex items-center gap-1.5 rounded-lg border border-indigo-500/40 bg-indigo-500/15 px-3 py-1.5 text-xs font-medium text-indigo-200 transition hover:bg-indigo-500/25 disabled:opacity-50"
              >
                <ImageDown className="h-3.5 w-3.5" />
                Export Long PNG
              </button>
              <button
                type="button"
                disabled={busy}
                onClick={() => void onExportBundle()}
                className="flex items-center gap-1.5 rounded-lg border border-[var(--border-subtle)] bg-[var(--bg-elevated)] px-3 py-1.5 text-xs font-medium text-slate-200 transition hover:bg-slate-800 disabled:opacity-50"
              >
                <FileText className="h-3.5 w-3.5" />
                <Download className="h-3 w-3" />
                MD + JSON
              </button>
            </div>
          )}
        </div>
      </div>

      {(pausedBudget || showBudgetModal) && job && (
        <form
          onSubmit={submitBudget}
          className="flex flex-wrap items-center justify-between gap-3 rounded-xl border border-amber-500/40 bg-amber-500/10 px-4 py-2.5 text-xs text-amber-200"
        >
          <span>
            {pausedBudget
              ? "Budget limit reached (PAUSED_BUDGET). Increase budget_limit_usd to resume queued frame rendering."
              : "Adjust task budget guardrail limit (USD):"}
          </span>
          <div className="flex items-center gap-2">
            <input
              type="number"
              step="0.1"
              min={(totalCost + 0.1).toFixed(2)}
              value={newBudgetInput}
              onChange={(e) => setNewBudgetInput(e.target.value)}
              className="w-24 rounded-lg border border-amber-500/40 bg-[#0b0d12] px-2.5 py-1 font-mono text-white"
            />
            <button
              type="submit"
              disabled={busy}
              className="rounded-lg bg-amber-500 px-3 py-1 font-medium text-slate-950 hover:bg-amber-400 disabled:opacity-50"
            >
              Approve Budget
            </button>
          </div>
        </form>
      )}
    </div>
  );
}
