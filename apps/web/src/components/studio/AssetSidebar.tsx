"use client";

import { useState } from "react";
import { Check, Edit3, Lock, ShieldCheck, Unlock } from "lucide-react";
import type { AssetStyleProfile, StoryJob } from "@/lib/types";

interface AssetSidebarProps {
  assets: AssetStyleProfile[];
  job: StoryJob | null;
  busy: boolean;
  onUpdateAsset: (
    assetId: string,
    payload: {
      display_name?: string;
      profile_json?: Record<string, unknown>;
      reference_image_url?: string | null;
      is_locked?: boolean;
    },
  ) => Promise<void>;
}

const PIPELINE_STEPS = [
  { key: "EXTRACT_ASSETS", label: "1. Extract Story Assets" },
  { key: "GATE_1", label: "2. Gate 1: Lock Ground Truth" },
  { key: "PLAN_SCRIPT", label: "3. AI Director Breakdown" },
  { key: "GATE_2", label: "4. Gate 2: Review Scripts" },
  { key: "RENDER_FRAMES", label: "5. Render + VLM Auto-QA" },
];

export function AssetSidebar({ assets, job, busy, onUpdateAsset }: AssetSidebarProps) {
  const [editingId, setEditingId] = useState<string | null>(null);
  const [draftName, setDraftName] = useState("");
  const [draftJson, setDraftJson] = useState("");
  const [draftRefUrl, setDraftRefUrl] = useState("");
  const [jsonErr, setJsonErr] = useState<string | null>(null);

  const stageOrder = [
    "INIT",
    "EXTRACT_ASSETS",
    "GATE_1",
    "PLAN_SCRIPT",
    "GATE_2",
    "RENDER_FRAMES",
    "DONE",
  ];
  const currentIdx = job ? stageOrder.indexOf(job.current_stage) : -1;

  function startEdit(asset: AssetStyleProfile) {
    setEditingId(asset.id);
    setDraftName(asset.display_name);
    setDraftJson(JSON.stringify(asset.profile_json, null, 2));
    setDraftRefUrl(asset.reference_image_url || "");
    setJsonErr(null);
  }

  async function saveEdit(assetId: string) {
    try {
      const parsed = JSON.parse(draftJson) as Record<string, unknown>;
      setJsonErr(null);
      await onUpdateAsset(assetId, {
        display_name: draftName.trim(),
        profile_json: parsed,
        reference_image_url: draftRefUrl.trim() || null,
        is_locked: true,
      });
      setEditingId(null);
    } catch {
      setJsonErr("Invalid JSON format in profile attributes.");
    }
  }

  return (
    <aside className="space-y-4">
      <div className="rounded-2xl border border-[var(--border-subtle)] bg-[var(--bg-panel)] p-4">
        <h2 className="text-xs font-semibold uppercase tracking-wider text-slate-400">
          Execution Pipeline
        </h2>
        <div className="mt-3 space-y-2">
          {PIPELINE_STEPS.map((step) => {
            const stepIdx = stageOrder.indexOf(step.key);
            const completed = currentIdx > stepIdx || job?.current_stage === "DONE";
            const active = currentIdx === stepIdx && job?.current_stage !== "DONE";
            return (
              <div
                key={step.key}
                className={`flex items-center justify-between rounded-lg px-2.5 py-1.5 text-xs ${
                  completed
                    ? "bg-emerald-500/10 text-emerald-300"
                    : active
                      ? "bg-amber-500/15 text-amber-200 ring-1 ring-amber-500/40"
                      : "text-slate-500"
                }`}
              >
                <span>{step.label}</span>
                {completed ? <Check className="h-3.5 w-3.5" /> : active ? <span>●</span> : null}
              </div>
            );
          })}
        </div>
      </div>

      <div className="rounded-2xl border border-[var(--border-subtle)] bg-[var(--bg-panel)] p-4">
        <div className="flex items-center justify-between">
          <h2 className="text-xs font-semibold uppercase tracking-wider text-slate-400">
            Assets & Style Truth ({assets.length})
          </h2>
          <span className="text-[11px] text-slate-500">Gate 1 Anchor</span>
        </div>

        {assets.length === 0 ? (
          <p className="mt-3 text-xs text-slate-500">
            Character, scene, and style profiles will appear here once asset extraction finishes.
          </p>
        ) : (
          <div className="mt-3 space-y-3">
            {assets.map((asset) => {
              const isGroundTruth = asset.source === "user_ground_truth";
              const isEditing = editingId === asset.id;
              return (
                <div
                  key={asset.id}
                  className={`rounded-xl border p-3 ${
                    isGroundTruth
                      ? "border-emerald-500/40 bg-emerald-500/5"
                      : "border-[var(--border-subtle)] bg-[var(--bg-elevated)]"
                  }`}
                >
                  <div className="flex items-start justify-between gap-2">
                    <div>
                      <div className="flex items-center gap-1.5">
                        <span className="rounded bg-indigo-500/20 px-1.5 py-0.5 text-[10px] font-semibold text-indigo-300">
                          {asset.asset_type}
                        </span>
                        <span className="text-xs font-semibold text-white">{asset.display_name}</span>
                      </div>
                      <div className="mt-0.5 font-mono text-[10px] text-slate-500">
                        {asset.entity_key}
                      </div>
                    </div>

                    <div className="flex items-center gap-1">
                      <button
                        type="button"
                        disabled={busy}
                        onClick={() => startEdit(asset)}
                        className="rounded p-1 text-slate-400 hover:bg-slate-800 hover:text-white"
                        title="Edit profile & lock truth"
                      >
                        <Edit3 className="h-3.5 w-3.5" />
                      </button>
                      <button
                        type="button"
                        disabled={busy}
                        onClick={() =>
                          void onUpdateAsset(asset.id, { is_locked: !asset.is_locked })
                        }
                        className={`rounded p-1 ${
                          asset.is_locked
                            ? "bg-emerald-500/20 text-emerald-300"
                            : "text-slate-400 hover:bg-slate-800"
                        }`}
                        title={asset.is_locked ? "Locked" : "Lock asset"}
                      >
                        {asset.is_locked ? (
                          <Lock className="h-3.5 w-3.5" />
                        ) : (
                          <Unlock className="h-3.5 w-3.5" />
                        )}
                      </button>
                    </div>
                  </div>

                  <div className="mt-2 flex items-center gap-1.5 text-[10px]">
                    <ShieldCheck
                      className={`h-3 w-3 ${isGroundTruth ? "text-emerald-400" : "text-slate-500"}`}
                    />
                    <span className={isGroundTruth ? "text-emerald-300" : "text-slate-400"}>
                      {isGroundTruth ? "user_ground_truth (Immutable)" : "agent_inferred"}
                    </span>
                  </div>

                  {asset.reference_image_url && !isEditing && (
                    <img
                      src={asset.reference_image_url}
                      alt={asset.display_name}
                      className="mt-2 h-20 w-full rounded-lg object-cover ring-1 ring-slate-700"
                    />
                  )}

                  {isEditing ? (
                    <div className="mt-2.5 space-y-2 border-t border-[var(--border-subtle)] pt-2.5">
                      <input
                        type="text"
                        value={draftName}
                        onChange={(e) => setDraftName(e.target.value)}
                        className="w-full rounded-lg border border-[var(--border-subtle)] bg-[#0b0d12] px-2.5 py-1 text-xs text-white"
                        placeholder="Display Name"
                      />
                      <textarea
                        rows={4}
                        value={draftJson}
                        onChange={(e) => setDraftJson(e.target.value)}
                        className="w-full rounded-lg border border-[var(--border-subtle)] bg-[#0b0d12] p-2 font-mono text-[11px] text-slate-200"
                      />
                      <input
                        type="text"
                        value={draftRefUrl}
                        onChange={(e) => setDraftRefUrl(e.target.value)}
                        placeholder="Optional reference_image_url"
                        className="w-full rounded-lg border border-[var(--border-subtle)] bg-[#0b0d12] px-2.5 py-1 text-[11px] text-slate-200"
                      />
                      {jsonErr && <p className="text-[11px] text-rose-400">{jsonErr}</p>}
                      <div className="flex justify-end gap-2">
                        <button
                          type="button"
                          onClick={() => setEditingId(null)}
                          className="rounded-lg px-2.5 py-1 text-xs text-slate-400 hover:text-white"
                        >
                          Cancel
                        </button>
                        <button
                          type="button"
                          disabled={busy}
                          onClick={() => void saveEdit(asset.id)}
                          className="rounded-lg bg-emerald-600 px-3 py-1 text-xs font-medium text-white hover:bg-emerald-500"
                        >
                          Save & Lock Truth
                        </button>
                      </div>
                    </div>
                  ) : (
                    <dl className="mt-2 space-y-1 text-[11px]">
                      {Object.entries(asset.profile_json).map(([k, v]) => (
                        <div key={k} className="flex justify-between gap-2 text-slate-300">
                          <dt className="text-slate-500">{k}:</dt>
                          <dd className="truncate text-right font-mono text-[10px]">
                            {typeof v === "string" ? v : JSON.stringify(v)}
                          </dd>
                        </div>
                      ))}
                    </dl>
                  )}
                </div>
              );
            })}
          </div>
        )}
      </div>
    </aside>
  );
}
