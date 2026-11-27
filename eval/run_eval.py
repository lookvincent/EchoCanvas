#!/usr/bin/env python3
"""EchoCanvas Automated Benchmark Runner (Baseline vs Layered Memory + VLM QA).

Usage:
  python eval/run_eval.py --dry-run-check
  python eval/run_eval.py --mode live [--limit 15]
"""

from __future__ import annotations

import argparse
import json
import os
import re
import sys
import time
import uuid
from datetime import datetime, timezone
from pathlib import Path
from typing import Any

REPO_ROOT = Path(__file__).resolve().parents[1]
DATASET_PATH = REPO_ROOT / "eval" / "datasets" / "benchmark_stories.json"
REPORT_PATH = REPO_ROOT / "eval" / "reports" / "latest_eval_report.md"
RAW_OUTPUT_PATH = REPO_ROOT / "eval" / "artifacts" / "latest_eval_raw.json"
SCHEMAS_PATH = REPO_ROOT / "services" / "py-agent" / "app" / "schemas.py"

REQUIRED_CATEGORIES = {
    "single_protagonist",
    "dual_dialogue",
    "prop_state_transition",
    "lighting_weather_shift",
}
VALID_ASPECT_RATIOS = {"16:9", "9:16", "1:1"}


def load_dotenv(path: Path) -> dict[str, str]:
    env = os.environ.copy()
    if path.is_file():
        for raw_line in path.read_text(encoding="utf-8").splitlines():
            line = raw_line.strip()
            if not line or line.startswith("#") or "=" not in line:
                continue
            key, val = line.split("=", 1)
            key = key.strip()
            val = val.strip().strip("\"'")
            if key and key not in env:
                env[key] = val
    return env


def validate_dataset(dataset_path: Path = DATASET_PATH) -> tuple[list[dict[str, Any]], list[str]]:
    errors: list[str] = []
    if not dataset_path.is_file():
        return [], [f"Missing benchmark dataset: {dataset_path}"]

    try:
        payload = json.loads(dataset_path.read_text(encoding="utf-8"))
    except json.JSONDecodeError as exc:
        return [], [f"Invalid JSON in {dataset_path}: {exc}"]

    stories = payload.get("stories")
    if not isinstance(stories, list):
        return [], ["Dataset 'stories' field must be a list"]

    if len(stories) < 15:
        errors.append(f"Expected at least 15 benchmark stories, found {len(stories)}")

    seen_ids: set[str] = set()
    seen_categories: set[str] = set()

    for idx, story in enumerate(stories, 1):
        sid = str(story.get("id") or f"idx_{idx}")
        if sid in seen_ids:
            errors.append(f"Duplicate story id: {sid}")
        seen_ids.add(sid)

        cat = str(story.get("category") or "")
        seen_categories.add(cat)
        if cat not in REQUIRED_CATEGORIES:
            errors.append(f"Story {sid} has unknown category {cat!r}")

        ratio = str(story.get("aspect_ratio") or "")
        if ratio not in VALID_ASPECT_RATIOS:
            errors.append(f"Story {sid} invalid aspect_ratio {ratio!r}")

        target_frames = story.get("target_frames")
        if not isinstance(target_frames, int) or not (2 <= target_frames <= 10):
            errors.append(f"Story {sid} target_frames must be integer 2..10, got {target_frames!r}")

        if not str(story.get("raw_story") or "").strip():
            errors.append(f"Story {sid} raw_story is empty")

        entities = story.get("expected_entities") or []
        if not isinstance(entities, list) or len(entities) == 0:
            errors.append(f"Story {sid} must declare expected_entities")

        checkpoints = story.get("continuity_checkpoints") or []
        if not isinstance(checkpoints, list) or len(checkpoints) == 0:
            errors.append(f"Story {sid} must declare continuity_checkpoints")

    missing_cats = REQUIRED_CATEGORIES - seen_categories
    if missing_cats:
        errors.append(f"Missing required benchmark categories: {sorted(missing_cats)}")

    if SCHEMAS_PATH.is_file():
        schema_src = SCHEMAS_PATH.read_text(encoding="utf-8")
        for sym in ("VLMJudgeCompareRequest", "VLMJudgeCompareResponse", "FrameScript", "AssetCard"):
            if not re.search(rf"class\s+{sym}\b", schema_src):
                errors.append(f"Missing contract {sym} in services/py-agent/app/schemas.py")
    else:
        errors.append(f"Missing schemas file: {SCHEMAS_PATH}")

    return stories, errors


def _percentile(values: list[float], pct: float) -> float:
    if not values:
        return 0.0
    ordered = sorted(values)
    idx = min(len(ordered) - 1, max(0, int(round((pct / 100.0) * (len(ordered) - 1)))))
    return ordered[idx]


def run_dry_check() -> int:
    stories, errors = validate_dataset()
    if errors:
        print("[EVAL DRY-RUN FAIL]")
        for err in errors:
            print(f"  - {err}")
        return 1

    cat_counts: dict[str, int] = {}
    for st in stories:
        cat = st["category"]
        cat_counts[cat] = cat_counts.get(cat, 0) + 1

    print(
        f"[EVAL DRY-RUN PASS] Validated {len(stories)} stories "
        f"({', '.join(f'{k}={v}' for k, v in sorted(cat_counts.items()))}) "
        "and aligned VLMJudge contracts."
    )
    return 0


def run_live_eval(limit: int, go_url: str, py_url: str, internal_token: str) -> int:
    try:
        import httpx
    except ImportError:
        print("[ERROR] httpx is required for live eval; run inside services/py-agent/.venv.", file=sys.stderr)
        return 1

    stories, errors = validate_dataset()
    if errors:
        print("[EVAL FAIL] Dataset validation failed before live run:")
        for err in errors:
            print(f"  - {err}")
        return 1

    env = load_dotenv(REPO_ROOT / ".env")
    if not env.get("LLM_PRIMARY_API_KEY") or not env.get("VISION_PRIMARY_API_KEY"):
        print(
            "[BLOCKED] LLM_PRIMARY_API_KEY and VISION_PRIMARY_API_KEY must be set in .env "
            "before executing --mode live (zero fake metrics per BIZ-03).",
            file=sys.stderr,
        )
        return 2

    selected = stories[: max(1, limit)]
    results: list[dict[str, Any]] = []

    with httpx.Client(trust_env=False, timeout=180.0) as client:
        for story in selected:
            t0 = time.monotonic()
            trace_id = f"tr_eval_{story['id']}_{uuid.uuid4().hex[:8]}"

            # 1. Run EchoCanvas end-to-end via Go Gateway REST API
            proj_resp = client.post(
                f"{go_url}/api/v1/projects",
                json={
                    "title": f"[EVAL] {story['title']}",
                    "raw_story": story["raw_story"],
                    "aspect_ratio": story["aspect_ratio"],
                    "style_preset": story["style_preset"],
                    "target_frames": story["target_frames"],
                },
            )
            proj_resp.raise_for_status()
            project_id = proj_resp.json()["data"]["id"]

            try:
                job_resp = client.post(
                    f"{go_url}/api/v1/projects/{project_id}/jobs",
                    headers={
                        "X-Idempotency-Key": f"eval_{story['id']}_{uuid.uuid4().hex[:8]}",
                        "X-Trace-ID": trace_id,
                    },
                    json={"budget_limit_usd": 2.0},
                )
                job_resp.raise_for_status()
                job_id = job_resp.json()["data"]["id"]

                # Wait for Gate 1
                for _ in range(90):
                    detail = client.get(f"{go_url}/api/v1/jobs/{job_id}").json()["data"]
                    if detail["job"]["current_stage"] == "GATE_1":
                        break
                    if detail["job"]["status"] == "FAILED":
                        raise RuntimeError(f"EchoCanvas job failed at stage 1: {detail['job']}")
                    time.sleep(0.5)

                assets = client.get(f"{go_url}/api/v1/projects/{project_id}/assets").json()["data"]["items"]
                client.post(
                    f"{go_url}/api/v1/jobs/{job_id}/gates/1/confirm",
                    json={"user_edits": {"assets": [{**a, "is_locked": True} for a in assets]}},
                ).raise_for_status()

                client.post(f"{go_url}/api/v1/jobs/{job_id}/gates/2/confirm", json={}).raise_for_status()

                final_detail: dict[str, Any] = {}
                for _ in range(240):
                    final_detail = client.get(f"{go_url}/api/v1/jobs/{job_id}").json()["data"]
                    if final_detail["job"]["current_stage"] == "DONE":
                        break
                    time.sleep(0.5)

                latency_ms = int((time.monotonic() - t0) * 1000)
                usage = client.get(f"{go_url}/api/v1/jobs/{job_id}/usage").json()["data"]

                ver_by_id = {v["id"]: v for v in final_detail.get("versions", [])}
                echo_imgs = [
                    ver_by_id[f["active_version_id"]]["image_url"]
                    for f in sorted(final_detail["frames"], key=lambda x: x["frame_index"])
                    if f.get("active_version_id") in ver_by_id
                ]

                # 2. Run Baseline (stateless direct prompt split + image gen via Go proxy)
                split_resp = client.post(
                    f"{go_url}/proxy/v1/chat/completions",
                    headers={
                        "X-Internal-Token": internal_token,
                        "X-Job-ID": job_id,
                        "X-Trace-ID": trace_id,
                        "X-Call-Type": "LLM_TEXT",
                    },
                    json={
                        "model": env.get("LLM_PRIMARY_MODEL", "deepseek-chat"),
                        "messages": [
                            {
                                "role": "user",
                                "content": (
                                    f"Split this story into {story['target_frames']} independent image prompts "
                                    f"as JSON {{\"prompts\": [...]}}:\n{story['raw_story']}"
                                ),
                            }
                        ],
                        "response_format": {"type": "json_object"},
                    },
                )
                split_resp.raise_for_status()
                prompts = json.loads(split_resp.json()["choices"][0]["message"]["content"]).get("prompts", [])[: story["target_frames"]]
                baseline_imgs: list[str] = []
                for idx, pr in enumerate(prompts, 1):
                    img_r = client.post(
                        f"{go_url}/proxy/v1/images/generations",
                        headers={
                            "X-Internal-Token": internal_token,
                            "X-Job-ID": job_id,
                            "X-Trace-ID": trace_id,
                            "X-Call-Type": "IMAGE_GEN",
                        },
                        json={
                            "model": env.get("VISION_PRIMARY_MODEL", "black-forest-labs/FLUX.1-dev"),
                            "prompt": str(pr),
                            "aspect_ratio": story["aspect_ratio"],
                        },
                    )
                    img_r.raise_for_status()
                    d = img_r.json().get("data", [{}])[0]
                    baseline_imgs.append(d.get("url", ""))

                # 3. Call VLM-as-a-Judge comparator
                judge_r = client.post(
                    f"{py_url}/internal/v1/eval/vlm-judge",
                    headers={"X-Internal-Token": internal_token, "X-Trace-ID": trace_id},
                    json={
                        "story_summary": story["raw_story"],
                        "asset_profiles": assets,
                        "baseline_images": baseline_imgs,
                        "echocanvas_images": echo_imgs,
                        "job_id": job_id,
                    },
                )
                judge_r.raise_for_status()
                judge = judge_r.json()

                results.append(
                    {
                        "id": story["id"],
                        "category": story["category"],
                        "title": story["title"],
                        "trace_id": trace_id,
                        "latency_ms": latency_ms,
                        "echocanvas_cost_usd": float(usage["total_cost_usd"]),
                        "baseline": judge["baseline"],
                        "echocanvas": judge["echocanvas"],
                        "winner": judge["winner"],
                        "rationale": judge.get("rationale", ""),
                    }
                )
            finally:
                client.delete(f"{go_url}/api/v1/projects/{project_id}")

    RAW_OUTPUT_PATH.parent.mkdir(parents=True, exist_ok=True)
    REPORT_PATH.parent.mkdir(parents=True, exist_ok=True)
    RAW_OUTPUT_PATH.write_text(json.dumps(results, ensure_ascii=False, indent=2), encoding="utf-8")

    b_avg = sum(r["baseline"]["overall_score"] for r in results) / len(results)
    e_avg = sum(r["echocanvas"]["overall_score"] for r in results) / len(results)
    p95_ms = _percentile([float(r["latency_ms"]) for r in results], 95.0)
    avg_cost = sum(r["echocanvas_cost_usd"] for r in results) / len(results)

    lines = [
        "# EchoCanvas Automated Benchmark Eval Report",
        "",
        f"- **Executed At (UTC)**: `{datetime.now(timezone.utc).isoformat()}`",
        f"- **Stories Evaluated**: `{len(results)}`",
        f"- **Baseline Avg Overall Score**: `{b_avg:.2f} / 10`",
        f"- **EchoCanvas Avg Overall Score**: `{e_avg:.2f} / 10`",
        f"- **EchoCanvas P95 Latency**: `{p95_ms:.0f} ms`",
        f"- **EchoCanvas Avg Cost / Story**: `${avg_cost:.4f}`",
        "",
        "| Story ID | Category | Baseline Score | EchoCanvas Score | Winner | Cost (USD) |",
        "| :--- | :--- | ---: | ---: | :--- | ---: |",
    ]
    for r in results:
        lines.append(
            f"| {r['id']} | {r['category']} | {r['baseline']['overall_score']:.2f} | "
            f"{r['echocanvas']['overall_score']:.2f} | {r['winner']} | ${r['echocanvas_cost_usd']:.4f} |"
        )
    REPORT_PATH.write_text("\n".join(lines) + "\n", encoding="utf-8")
    print(f"[EVAL LIVE PASS] Report written to {REPORT_PATH}")
    return 0


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description="EchoCanvas Benchmark Eval Runner")
    parser.add_argument(
        "--dry-run-check",
        action="store_true",
        help="Validate 15-story dataset & schema contracts without calling paid models",
    )
    parser.add_argument(
        "--mode",
        choices=["dry-run", "live"],
        default="dry-run",
        help="Run dry-run validation or live Baseline vs EchoCanvas evaluation",
    )
    parser.add_argument("--limit", type=int, default=15, help="Max stories in live mode")
    return parser


def main() -> int:
    args = build_parser().parse_args()
    if args.dry_run_check or args.mode == "dry-run":
        return run_dry_check()

    env = load_dotenv(REPO_ROOT / ".env")
    go_url = f"http://127.0.0.1:{env.get('GO_GATEWAY_PORT', '8080')}"
    py_url = env.get("PY_SIDECAR_URL", "http://127.0.0.1:8001").rstrip("/")
    token = env.get("INTERNAL_AUTH_TOKEN", "")
    return run_live_eval(args.limit, go_url, py_url, token)


if __name__ == "__main__":
    raise SystemExit(main())
