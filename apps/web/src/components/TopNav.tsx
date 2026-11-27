"use client";

import Link from "next/link";
import { usePathname } from "next/navigation";
import { useEffect, useState } from "react";
import { Activity, BarChart3, Film, FolderKanban } from "lucide-react";
import { checkHealth } from "@/lib/api";

export function TopNav() {
  const pathname = usePathname();
  const [healthy, setHealthy] = useState<boolean | null>(null);

  useEffect(() => {
    let active = true;
    checkHealth()
      .then(() => {
        if (active) setHealthy(true);
      })
      .catch(() => {
        if (active) setHealthy(false);
      });
    return () => {
      active = false;
    };
  }, []);

  const navItems = [
    { href: "/projects", label: "Projects Studio", icon: FolderKanban },
    { href: "/analytics", label: "Cost & Eval Analytics", icon: BarChart3 },
  ];

  return (
    <header className="sticky top-0 z-30 border-b border-[var(--border-subtle)] bg-[#0d1017]/90 backdrop-blur">
      <div className="mx-auto flex h-14 max-w-[1600px] items-center justify-between px-4 sm:px-6">
        <div className="flex items-center gap-6">
          <Link href="/projects" className="flex items-center gap-2.5 text-sm font-semibold tracking-tight text-white">
            <span className="flex h-8 w-8 items-center justify-center rounded-lg bg-indigo-500/20 text-indigo-400 ring-1 ring-indigo-500/40">
              <Film className="h-4 w-4" />
            </span>
            <span>
              EchoCanvas <span className="hidden text-xs font-normal text-slate-400 sm:inline">浮生绘卷</span>
            </span>
          </Link>

          <nav className="flex items-center gap-1">
            {navItems.map((item) => {
              const active = pathname === item.href || pathname.startsWith(`${item.href}/`);
              const Icon = item.icon;
              return (
                <Link
                  key={item.href}
                  href={item.href}
                  className={`flex items-center gap-2 rounded-lg px-3 py-1.5 text-xs font-medium transition ${
                    active
                      ? "bg-indigo-500/20 text-indigo-300 ring-1 ring-indigo-500/30"
                      : "text-slate-400 hover:bg-slate-800/70 hover:text-slate-200"
                  }`}
                >
                  <Icon className="h-3.5 w-3.5" />
                  {item.label}
                </Link>
              );
            })}
          </nav>
        </div>

        <div className="flex items-center gap-2 rounded-full border border-[var(--border-subtle)] bg-[var(--bg-panel)] px-3 py-1 text-xs">
          <Activity
            className={`h-3.5 w-3.5 ${
              healthy === true
                ? "text-emerald-400"
                : healthy === false
                  ? "text-rose-400"
                  : "text-amber-400"
            }`}
          />
          <span className="text-slate-300">
            {healthy === true
              ? "Go Gateway Online"
              : healthy === false
                ? "Gateway Offline"
                : "Checking Gateway..."}
          </span>
        </div>
      </div>
    </header>
  );
}
