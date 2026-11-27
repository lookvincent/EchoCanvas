"use client";

import Link from "next/link";
import { useRouter } from "next/navigation";
import { useEffect, useState } from "react";
import { ArrowRight, Plus, Sparkles, Trash2 } from "lucide-react";
import { createProject, deleteProject, listProjects } from "@/lib/api";
import { DEMO_TEMPLATES, type StoryTemplate } from "@/lib/templates";
import type { Project } from "@/lib/types";

const STYLE_PRESETS = [
  { value: "cinematic_melancholy", label: "Cinematic Melancholy (冷峻电影感)" },
  { value: "warm_film_grain", label: "Warm Film Grain (胶片暖调)" },
  { value: "anime_cyber_noir", label: "Anime Cyber Noir (霓虹赛博)" },
  { value: "watercolor_storybook", label: "Watercolor Storybook (水彩绘本)" },
];

export default function ProjectsPage() {
  const router = useRouter();
  const [projects, setProjects] = useState<Project[]>([]);
  const [loading, setLoading] = useState(true);
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const [title, setTitle] = useState("");
  const [rawStory, setRawStory] = useState("");
  const [aspectRatio, setAspectRatio] = useState<"16:9" | "9:16" | "1:1">("16:9");
  const [targetFrames, setTargetFrames] = useState(4);
  const [stylePreset, setStylePreset] = useState("cinematic_melancholy");

  async function refreshProjects() {
    setLoading(true);
    setError(null);
    try {
      const res = await listProjects(30, 0);
      setProjects(res.items || []);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to load projects");
    } finally {
      setLoading(false);
    }
  }

  useEffect(() => {
    void refreshProjects();
  }, []);

  function applyTemplate(tpl: StoryTemplate) {
    setTitle(tpl.title);
    setRawStory(tpl.raw_story);
    setAspectRatio(tpl.aspect_ratio);
    setTargetFrames(tpl.target_frames);
    setStylePreset(tpl.style_preset);
  }

  async function handleCreate(e: React.FormEvent) {
    e.preventDefault();
    if (!title.trim() || !rawStory.trim()) {
      setError("Title and story text are required.");
      return;
    }
    setSubmitting(true);
    setError(null);
    try {
      const created = await createProject({
        title: title.trim(),
        raw_story: rawStory.trim(),
        aspect_ratio: aspectRatio,
        style_preset: stylePreset,
        target_frames: targetFrames,
      });
      router.push(`/projects/${created.id}/studio`);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to create project");
      setSubmitting(false);
    }
  }

  async function handleDelete(projectId: string) {
    try {
      await deleteProject(projectId);
      setProjects((prev) => prev.filter((p) => p.id !== projectId));
    } catch (err) {
      setError(err instanceof Error ? err.message : "Delete failed");
    }
  }

  return (
    <div className="space-y-8">
      <section className="rounded-2xl border border-[var(--border-subtle)] bg-[var(--bg-panel)] p-6">
        <div className="flex flex-wrap items-start justify-between gap-4 border-b border-[var(--border-subtle)] pb-5">
          <div>
            <h1 className="text-xl font-semibold text-white">Create Visual Narrative Project</h1>
            <p className="mt-1 text-sm text-slate-400">
              Start from a built-in showcase template or paste your own dream fragment / story script (100–5000 chars).
            </p>
          </div>
          <div className="flex flex-wrap gap-2">
            {DEMO_TEMPLATES.map((tpl) => (
              <button
                key={tpl.id}
                type="button"
                onClick={() => applyTemplate(tpl)}
                className="flex items-center gap-1.5 rounded-lg border border-indigo-500/30 bg-indigo-500/10 px-3 py-1.5 text-xs font-medium text-indigo-300 transition hover:bg-indigo-500/20"
              >
                <Sparkles className="h-3.5 w-3.5" />
                <span>{tpl.label}</span>
                <span className="rounded bg-indigo-500/20 px-1.5 py-0.5 text-[10px] text-indigo-200">
                  {tpl.badge}
                </span>
              </button>
            ))}
          </div>
        </div>

        <form onSubmit={handleCreate} className="mt-5 grid gap-4 lg:grid-cols-12">
          <div className="space-y-4 lg:col-span-8">
            <div>
              <label className="mb-1.5 block text-xs font-medium text-slate-300">Project Title</label>
              <input
                type="text"
                value={title}
                onChange={(e) => setTitle(e.target.value)}
                placeholder="e.g. 2016 北京冬夜过街天桥"
                maxLength={120}
                className="w-full rounded-xl border border-[var(--border-subtle)] bg-[var(--bg-canvas)] px-3.5 py-2.5 text-sm text-white placeholder:text-slate-500 focus:border-indigo-500 focus:outline-none"
              />
            </div>

            <div>
              <label className="mb-1.5 block text-xs font-medium text-slate-300">
                Narrative / Dream Script
              </label>
              <textarea
                rows={5}
                value={rawStory}
                onChange={(e) => setRawStory(e.target.value)}
                placeholder="Describe recurring characters, props, lighting, and chronological scene beats..."
                className="w-full rounded-xl border border-[var(--border-subtle)] bg-[var(--bg-canvas)] px-3.5 py-2.5 text-sm leading-relaxed text-white placeholder:text-slate-500 focus:border-indigo-500 focus:outline-none"
              />
            </div>
          </div>

          <div className="flex flex-col justify-between space-y-4 rounded-xl border border-[var(--border-subtle)] bg-[var(--bg-elevated)] p-4 lg:col-span-4">
            <div className="space-y-3">
              <div>
                <label className="mb-1 block text-xs font-medium text-slate-300">Aspect Ratio</label>
                <select
                  value={aspectRatio}
                  onChange={(e) => setAspectRatio(e.target.value as "16:9" | "9:16" | "1:1")}
                  className="w-full rounded-lg border border-[var(--border-subtle)] bg-[var(--bg-canvas)] px-3 py-2 text-sm text-white focus:border-indigo-500 focus:outline-none"
                >
                  <option value="16:9">16:9 Widescreen Cinema</option>
                  <option value="9:16">9:16 Vertical Mobile Comic</option>
                  <option value="1:1">1:1 Square Storybook</option>
                </select>
              </div>

              <div>
                <label className="mb-1 block text-xs font-medium text-slate-300">
                  Target Storyboard Frames ({targetFrames})
                </label>
                <input
                  type="range"
                  min={2}
                  max={8}
                  value={targetFrames}
                  onChange={(e) => setTargetFrames(Number(e.target.value))}
                  className="w-full accent-indigo-500"
                />
              </div>

              <div>
                <label className="mb-1 block text-xs font-medium text-slate-300">Visual Style Preset</label>
                <select
                  value={stylePreset}
                  onChange={(e) => setStylePreset(e.target.value)}
                  className="w-full rounded-lg border border-[var(--border-subtle)] bg-[var(--bg-canvas)] px-3 py-2 text-sm text-white focus:border-indigo-500 focus:outline-none"
                >
                  {STYLE_PRESETS.map((s) => (
                    <option key={s.value} value={s.value}>
                      {s.label}
                    </option>
                  ))}
                </select>
              </div>
            </div>

            {error && (
              <div className="rounded-lg border border-rose-500/40 bg-rose-500/10 px-3 py-2 text-xs text-rose-300">
                {error}
              </div>
            )}

            <button
              type="submit"
              disabled={submitting}
              className="flex w-full items-center justify-center gap-2 rounded-xl bg-indigo-600 px-4 py-2.5 text-sm font-medium text-white transition hover:bg-indigo-500 disabled:opacity-50"
            >
              <Plus className="h-4 w-4" />
              {submitting ? "Creating Project..." : "Create & Open Studio"}
            </button>
          </div>
        </form>
      </section>

      <section className="space-y-4">
        <div className="flex items-center justify-between">
          <h2 className="text-base font-semibold text-white">Your Storyboard Projects</h2>
          <span className="text-xs text-slate-400">{projects.length} projects</span>
        </div>

        {loading ? (
          <div className="grid gap-4 md:grid-cols-2 lg:grid-cols-3">
            {[1, 2, 3].map((k) => (
              <div
                key={k}
                className="h-40 animate-pulse rounded-2xl border border-[var(--border-subtle)] bg-[var(--bg-panel)]"
              />
            ))}
          </div>
        ) : projects.length === 0 ? (
          <div className="rounded-2xl border border-dashed border-[var(--border-subtle)] bg-[var(--bg-panel)] p-10 text-center text-sm text-slate-400">
            No projects yet. Click one of the showcase templates above to launch your first storyboard in seconds.
          </div>
        ) : (
          <div className="grid gap-4 md:grid-cols-2 lg:grid-cols-3">
            {projects.map((p) => (
              <div
                key={p.id}
                className="flex flex-col justify-between rounded-2xl border border-[var(--border-subtle)] bg-[var(--bg-panel)] p-5 transition hover:border-indigo-500/50"
              >
                <div>
                  <div className="flex items-start justify-between gap-2">
                    <h3 className="text-sm font-semibold text-white">{p.title}</h3>
                    <span className="rounded-md bg-slate-800 px-2 py-0.5 text-[11px] font-mono text-slate-300">
                      {p.aspect_ratio} · {p.target_frames}F
                    </span>
                  </div>
                  <p className="mt-2 line-clamp-3 text-xs leading-relaxed text-slate-400">
                    {p.raw_story}
                  </p>
                </div>

                <div className="mt-4 flex items-center justify-between border-t border-[var(--border-subtle)] pt-3">
                  <span className="text-[11px] text-slate-500">{p.style_preset}</span>
                  <div className="flex items-center gap-2">
                    <button
                      type="button"
                      onClick={() => void handleDelete(p.id)}
                      className="rounded-lg p-1.5 text-slate-500 transition hover:bg-rose-500/15 hover:text-rose-400"
                      title="Delete project"
                    >
                      <Trash2 className="h-3.5 w-3.5" />
                    </button>
                    <Link
                      href={`/projects/${p.id}/studio`}
                      className="flex items-center gap-1 rounded-lg bg-indigo-500/15 px-3 py-1.5 text-xs font-medium text-indigo-300 transition hover:bg-indigo-500/25"
                    >
                      Open Studio <ArrowRight className="h-3.5 w-3.5" />
                    </Link>
                  </div>
                </div>
              </div>
            ))}
          </div>
        )}
      </section>
    </div>
  );
}
