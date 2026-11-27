"use client";

import Link from "next/link";
import { useEffect, useState } from "react";
import { Coins, FlaskConical, FolderKanban, Terminal } from "lucide-react";
import { listProjectJobs, listProjects } from "@/lib/api";
import type { Project, StoryJob } from "@/lib/types";

interface ProjectJobSummary {
  project: Project;
  latestJob: StoryJob | null;
}

export default function AnalyticsPage() {
  const [rows, setRows] = useState<ProjectJobSummary[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    let active = true;
    async function load() {
      try {
        const res = await listProjects(30, 0);
        const enriched = await Promise.all(
          (res.items || []).map(async (project) => {
            const jobsRes = await listProjectJobs(project.id).catch(() => ({ items: [] }));
            return {
              project,
              latestJob: jobsRes.items?.[0] || null,
            };
          }),
        );
        if (active) setRows(enriched);
      } catch (err) {
        if (active) setError(err instanceof Error ? err.message : "Failed to load analytics");
      } finally {
        if (active) setLoading(false);
      }
    }
    void load();
    return () => {
      active = false;
    };
  }, []);

  const totalCost = rows.reduce((sum, r) => sum + (r.latestJob?.total_cost_usd || 0), 0);
  const completedCount = rows.filter((r) => r.latestJob?.status === "SUCCEEDED").length;

  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-xl font-semibold text-white">Cost Ledger, Traceability & Benchmark Eval</h1>
        <p className="mt-1 text-xs text-slate-400">
          Real telemetry aggregated from Go Gateway usage_ledger and PostgreSQL job records.
        </p>
      </div>

      <div className="grid gap-4 sm:grid-cols-3">
        <div className="rounded-2xl border border-[var(--border-subtle)] bg-[var(--bg-panel)] p-5">
          <div className="flex items-center gap-2 text-xs text-slate-400">
            <FolderKanban className="h-4 w-4 text-indigo-400" />
            Active Projects
          </div>
          <div className="mt-2 font-mono text-2xl font-semibold text-white">{rows.length}</div>
        </div>

        <div className="rounded-2xl border border-[var(--border-subtle)] bg-[var(--bg-panel)] p-5">
          <div className="flex items-center gap-2 text-xs text-slate-400">
            <Coins className="h-4 w-4 text-emerald-400" />
            Observed Latest-Job Spend
          </div>
          <div className="mt-2 font-mono text-2xl font-semibold text-emerald-300">
            ${totalCost.toFixed(4)}
          </div>
        </div>

        <div className="rounded-2xl border border-[var(--border-subtle)] bg-[var(--bg-panel)] p-5">
          <div className="flex items-center gap-2 text-xs text-slate-400">
            <FlaskConical className="h-4 w-4 text-amber-400" />
            Completed Storyboards
          </div>
          <div className="mt-2 font-mono text-2xl font-semibold text-white">{completedCount}</div>
        </div>
      </div>

      {error && (
        <div className="rounded-xl border border-rose-500/40 bg-rose-500/10 px-4 py-2.5 text-xs text-rose-300">
          {error}
        </div>
      )}

      <div className="overflow-hidden rounded-2xl border border-[var(--border-subtle)] bg-[var(--bg-panel)]">
        <div className="border-b border-[var(--border-subtle)] px-5 py-3 text-xs font-semibold uppercase tracking-wider text-slate-400">
          Job Execution & Trace Ledger
        </div>
        {loading ? (
          <div className="p-6 text-xs text-slate-400">Loading ledger records...</div>
        ) : rows.length === 0 ? (
          <div className="p-6 text-xs text-slate-500">No jobs recorded yet.</div>
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full text-left text-xs">
              <thead className="border-b border-[var(--border-subtle)] bg-[var(--bg-elevated)] text-slate-400">
                <tr>
                  <th className="px-4 py-2.5">Project</th>
                  <th className="px-4 py-2.5">Stage / Status</th>
                  <th className="px-4 py-2.5">Trace ID</th>
                  <th className="px-4 py-2.5">Spend / Cap</th>
                  <th className="px-4 py-2.5">Studio</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-[var(--border-subtle)]">
                {rows.map(({ project, latestJob }) => (
                  <tr key={project.id} className="hover:bg-slate-800/30">
                    <td className="px-4 py-3 font-medium text-white">{project.title}</td>
                    <td className="px-4 py-3 font-mono text-slate-300">
                      {latestJob ? `${latestJob.current_stage} / ${latestJob.status}` : "NO_JOB"}
                    </td>
                    <td className="px-4 py-3 font-mono text-slate-400">
                      {latestJob?.trace_id || "—"}
                    </td>
                    <td className="px-4 py-3 font-mono text-emerald-300">
                      {latestJob
                        ? `$${latestJob.total_cost_usd.toFixed(4)} / $${latestJob.budget_limit_usd.toFixed(2)}`
                        : "—"}
                    </td>
                    <td className="px-4 py-3">
                      <Link
                        href={`/projects/${project.id}/studio`}
                        className="text-indigo-400 hover:text-indigo-300"
                      >
                        Open
                      </Link>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </div>

      <div className="rounded-2xl border border-[var(--border-subtle)] bg-[var(--bg-panel)] p-5">
        <div className="flex items-center gap-2 text-sm font-semibold text-white">
          <Terminal className="h-4 w-4 text-indigo-400" />
          Baseline vs EchoCanvas Benchmark Evaluation (`eval/run_eval.py`)
        </div>
        <p className="mt-2 text-xs leading-relaxed text-slate-400">
          Benchmark metrics are strictly populated only from live VLM-as-a-Judge runs across the 15-story dataset in{" "}
          <code className="rounded bg-black/40 px-1.5 py-0.5 text-slate-200">
            eval/datasets/benchmark_stories.json
          </code>
          . Once commercial LLM and Flux API keys are configured in{" "}
          <code className="rounded bg-black/40 px-1.5 py-0.5 text-slate-200">.env</code>, run:
        </p>
        <pre className="mt-3 rounded-xl border border-[var(--border-subtle)] bg-[#0b0d12] p-3 font-mono text-xs text-slate-200">
          python eval/run_eval.py --mode live
        </pre>
      </div>
    </div>
  );
}
