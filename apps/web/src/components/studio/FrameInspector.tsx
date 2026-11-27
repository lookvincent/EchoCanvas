"use client";

import { useState } from "react";
import { GitBranch, Layers, Receipt, Wand2 } from "lucide-react";
import type {
  EpisodicEntityState,
  FrameVersion,
  JobUsageSummary,
  StoryFrame,
} from "@/lib/types";

interface FrameInspectorProps {
  frame: StoryFrame | null;
  versions: FrameVersion[];
  states: EpisodicEntityState[];
  usage: JobUsageSummary | null;
  busy: boolean;
  onRegenerate: (frameId: string, note: string, cascadeDownstream: boolean) => Promise<void>;
}

export function FrameInspector({
  frame,
  versions,
  states,
  usage,
  busy,
  onRegenerate,
}: FrameInspectorProps) {
  const [editNote, setEditNote] = useState("");

  const frameVersions = frame
    ? versions
        .filter((v) => v.frame_id === frame.id)
        .sort((a, b) => a.version_no - b.version_no)
    : [];
  const activeVer =
    frameVersions.find((v) => v.id === frame?.active_version_id) ||
    frameVersions[frameVersions.length - 1];
  const frameStates = frame
    ? states.filter((s) => s.frame_index === frame.frame_index)
    : [];

  async function submitRegen(cascade: boolean) {
    if (!frame || !editNote.trim()) return;
    await onRegenerate(frame.id, editNote.trim(), cascade);
    setEditNote("");
  }

  return (
    <aside className="space-y-4">
      {frame ? (
        <>
          <div className="rounded-2xl border border-[var(--border-subtle)] bg-[var(--bg-panel)] p-4">
            <div className="flex items-center justify-between">
              <h2 className="text-xs font-semibold uppercase tracking-wider text-slate-400">
                Frame {String(frame.frame_index).padStart(2, "0")} Inspector
              </h2>
              {activeVer && (
                <span className="rounded bg-indigo-500/20 px-2 py-0.5 font-mono text-[10px] text-indigo-300">
                  Active v{activeVer.version_no}
                </span>
              )}
            </div>

            <div className="mt-3 space-y-2 text-xs">
              <div className="rounded-lg bg-[var(--bg-elevated)] p-2.5">
                <div className="text-[11px] font-medium text-slate-400">Composition & Lighting</div>
                <p className="mt-1 text-slate-200">{frame.script_json.composition}</p>
                <p className="mt-1 text-[11px] text-slate-400">Lighting: {frame.script_json.lighting}</p>
              </div>
            </div>
          </div>

          <div className="rounded-2xl border border-[var(--border-subtle)] bg-[var(--bg-panel)] p-4">
            <div className="flex items-center gap-2">
              <Layers className="h-3.5 w-3.5 text-emerald-400" />
              <h3 className="text-xs font-semibold uppercase tracking-wider text-slate-400">
                Episodic Entity Continuity (FR-C04)
              </h3>
            </div>
            {frameStates.length === 0 ? (
              <p className="mt-2.5 text-xs text-slate-500">
                Entity state snapshots will populate after Gate 1 script breakdown.
              </p>
            ) : (
              <div className="mt-3 space-y-2">
                {frameStates.map((st) => (
                  <div
                    key={st.id}
                    className="rounded-xl border border-[var(--border-subtle)] bg-[var(--bg-elevated)] p-2.5 text-xs"
                  >
                    <div className="font-mono text-[11px] font-semibold text-emerald-300">
                      {st.entity_key}
                    </div>
                    <dl className="mt-1.5 space-y-1 text-[11px]">
                      {Object.entries(st.state_json).map(([k, v]) => (
                        <div key={k} className="flex justify-between gap-2 text-slate-300">
                          <dt className="text-slate-500">{k}:</dt>
                          <dd className="text-right font-mono text-[10px]">
                            {typeof v === "string" ? v : JSON.stringify(v)}
                          </dd>
                        </div>
                      ))}
                    </dl>
                    {st.diff_from_prev && Object.keys(st.diff_from_prev).length > 0 && (
                      <div className="mt-2 border-t border-[var(--border-subtle)] pt-1.5 text-[10px] text-amber-300">
                        Delta vs N-1: {Object.keys(st.diff_from_prev).join(", ")}
                      </div>
                    )}
                  </div>
                ))}
              </div>
            )}
          </div>

          <div className="rounded-2xl border border-[var(--border-subtle)] bg-[var(--bg-panel)] p-4">
            <div className="flex items-center gap-2">
              <Wand2 className="h-3.5 w-3.5 text-indigo-400" />
              <h3 className="text-xs font-semibold uppercase tracking-wider text-slate-400">
                Gate 3 Local Regeneration
              </h3>
            </div>
            <textarea
              rows={3}
              value={editNote}
              onChange={(e) => setEditNote(e.target.value)}
              placeholder="e.g. Stand coat collar up, add silver flask in left hand, warm streetlamp rim light..."
              className="mt-3 w-full rounded-xl border border-[var(--border-subtle)] bg-[var(--bg-canvas)] p-2.5 text-xs text-white placeholder:text-slate-500 focus:border-indigo-500 focus:outline-none"
            />
            <div className="mt-2.5 grid gap-2">
              <button
                type="button"
                disabled={busy || !editNote.trim()}
                onClick={() => void submitRegen(false)}
                className="flex items-center justify-center gap-1.5 rounded-lg bg-indigo-600 px-3 py-2 text-xs font-medium text-white transition hover:bg-indigo-500 disabled:opacity-50"
              >
                <Wand2 className="h-3.5 w-3.5" />
                Regenerate Frame Only
              </button>
              <button
                type="button"
                disabled={busy || !editNote.trim()}
                onClick={() => void submitRegen(true)}
                className="flex items-center justify-center gap-1.5 rounded-lg border border-amber-500/40 bg-amber-500/10 px-3 py-2 text-xs font-medium text-amber-200 transition hover:bg-amber-500/20 disabled:opacity-50"
              >
                <GitBranch className="h-3.5 w-3.5" />
                Regenerate & Cascade Downstream
              </button>
            </div>

            {activeVer && (
              <div className="mt-3 border-t border-[var(--border-subtle)] pt-3">
                <div className="text-[10px] font-medium text-slate-500">Assembled Prompt Snapshot</div>
                <pre className="mt-1 max-h-28 overflow-auto whitespace-pre-wrap rounded-lg bg-[#0b0d12] p-2 font-mono text-[10px] text-slate-400">
                  {activeVer.prompt_snapshot}
                </pre>
              </div>
            )}
          </div>
        </>
      ) : (
        <div className="rounded-2xl border border-[var(--border-subtle)] bg-[var(--bg-panel)] p-4 text-xs text-slate-400">
          Select a storyboard frame from the gallery to inspect entity states and run Gate 3 local regeneration.
        </div>
      )}

      <div className="rounded-2xl border border-[var(--border-subtle)] bg-[var(--bg-panel)] p-4">
        <div className="flex items-center justify-between">
          <div className="flex items-center gap-2">
            <Receipt className="h-3.5 w-3.5 text-amber-400" />
            <h3 className="text-xs font-semibold uppercase tracking-wider text-slate-400">
              Cost & Usage Ledger (FR-F02)
            </h3>
          </div>
          {usage && (
            <span className="font-mono text-xs font-semibold text-white">
              ${usage.total_cost_usd.toFixed(4)}
            </span>
          )}
        </div>

        {!usage || usage.breakdown.length === 0 ? (
          <p className="mt-2.5 text-xs text-slate-500">
            No AIGC proxy calls recorded yet for this job.
          </p>
        ) : (
          <div className="mt-3 space-y-2">
            {usage.breakdown.map((b, idx) => (
              <div
                key={`${b.provider}-${b.model}-${b.call_type}-${idx}`}
                className="flex items-center justify-between rounded-lg bg-[var(--bg-elevated)] px-2.5 py-2 text-[11px]"
              >
                <div>
                  <div className="font-medium text-slate-200">
                    {b.call_type} · {b.provider}
                  </div>
                  <div className="font-mono text-[10px] text-slate-500">
                    {b.model} ({b.call_count}x)
                  </div>
                </div>
                <div className="text-right font-mono text-xs text-emerald-300">
                  ${b.cost_usd.toFixed(4)}
                </div>
              </div>
            ))}
          </div>
        )}
      </div>
    </aside>
  );
}
