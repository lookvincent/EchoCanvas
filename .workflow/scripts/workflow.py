#!/usr/bin/env python3
"""EchoCanvas Git-native multi-agent workflow CLI (zero external dependencies)."""

from __future__ import annotations

import argparse
import json
import os
import re
import subprocess
import sys
from datetime import datetime, timedelta, timezone
from pathlib import Path

try:
    sys.stdout.reconfigure(encoding="utf-8", errors="replace")
    sys.stderr.reconfigure(encoding="utf-8", errors="replace")
except AttributeError:
    pass

TZ_CN = timezone(timedelta(hours=8))
REPO_ROOT = Path(__file__).resolve().parents[2]
WORKFLOW_DIR = REPO_ROOT / ".workflow"
STATE_PATH = WORKFLOW_DIR / "state.json"
TASKS_PATH = WORKFLOW_DIR / "tasks.jsonl"
HANDOFFS_DIR = WORKFLOW_DIR / "handoffs"
DOTENV_PATH = REPO_ROOT / ".env"

NEXT_MILESTONE = {
    "M1": "M2",
    "M2": "M3",
    "M3": "M4",
    "M4": "DONE",
}

SKIP_DIRS = {".git", "node_modules", ".venv", "venv", "__pycache__", ".next"}
BINARY_EXTS = {
    ".png", ".jpg", ".jpeg", ".gif", ".webp", ".ico", ".pdf",
    ".zip", ".tar", ".gz", ".exe", ".dll", ".so", ".dylib", ".pyc",
}
FORBIDDEN_FILE_NAMES = {".env", ".env.local", ".env.production", "id_rsa", "id_ed25519"}
FORBIDDEN_FILE_EXTS = {".pem", ".key", ".p12", ".pfx"}

# Construct token patterns dynamically so this script does not trigger self-matches.
SECRET_PATTERNS = [
    ("OpenAI/API Key", re.compile(r"\b" + "sk" + r"-[A-Za-z0-9_-]{20,}\b")),
    ("GitHub Classic PAT", re.compile(r"\b" + "ghp" + r"_[A-Za-z0-9]{20,}\b")),
    ("GitHub OAuth Token", re.compile(r"\b" + "gho" + r"_[A-Za-z0-9]{20,}\b")),
    ("GitHub Fine-grained PAT", re.compile(r"\b" + "github_" + r"pat_[A-Za-z0-9_]{20,}\b")),
    ("AWS Access Key", re.compile(r"\b" + "AKIA" + r"[0-9A-Z]{16}\b")),
    ("Private Key Block", re.compile(r"-----BEGIN (?:RSA |EC |OPENSSH )?" + "PRIVATE KEY" + r"-----")),
    ("Langfuse Key", re.compile(r"\b(?:pk|sk)-" + "lf" + r"-[A-Za-z0-9_-]{16,}\b")),
]

LOCAL_PATH_PATTERNS = [
    ("Windows User Path", re.compile(r"[A-Za-z]:\\" + r"(?:Users|Documents and Settings)\\[^\s\"'`<>]+")),
    ("Windows Workspace Path", re.compile(r"[A-Za-z]:\\" + r"20\d{2}-[^\s\"'`<>]+")),
    ("Unix Home Path", re.compile(r"(?:^|[\s\"'`=(])/(?:Users|home)/" + r"[A-Za-z0-9._-]+/[^\s\"'`<>]*")),
]

ENV_ASSIGN_PATTERN = re.compile(
    r"^\s*(?:export\s+)?([A-Z0-9_]*(?:API_KEY|SECRET_KEY|PASSWORD|AUTH_TOKEN|ACCESS_KEY))\s*=\s*([^\s#]+)"
)
SAFE_PLACEHOLDER_PREFIXES = (
    "changeme",
    "replace_with",
    "your_",
    "<",
    "${",
    "$",
    "example",
    "dummy",
    "test_",
    "mock_",
)


def now_iso() -> str:
    return datetime.now(TZ_CN).replace(microsecond=0).isoformat()


def load_dotenv_overlay() -> dict[str, str]:
    env = os.environ.copy()
    if DOTENV_PATH.is_file():
        for raw_line in DOTENV_PATH.read_text(encoding="utf-8").splitlines():
            line = raw_line.strip()
            if not line or line.startswith("#") or "=" not in line:
                continue
            key, val = line.split("=", 1)
            key = key.strip()
            val = val.strip().strip("\"'")
            if key and key not in env:
                env[key] = val
        env.setdefault("RUN_PG_INTEGRATION", "1")
        env.setdefault("RUN_REDIS_INTEGRATION", "1")
    return env


def load_state() -> dict:
    return json.loads(STATE_PATH.read_text(encoding="utf-8"))


def save_state(state: dict) -> None:
    STATE_PATH.write_text(
        json.dumps(state, ensure_ascii=False, indent=2) + "\n", encoding="utf-8"
    )


def load_tasks() -> list[dict]:
    tasks: list[dict] = []
    for idx, line in enumerate(TASKS_PATH.read_text(encoding="utf-8").splitlines(), 1):
        stripped = line.strip()
        if not stripped:
            continue
        try:
            tasks.append(json.loads(stripped))
        except json.JSONDecodeError as exc:
            raise SystemExit(f"[ERROR] Invalid JSON at {TASKS_PATH}:{idx}: {exc}") from exc
    return tasks


def save_tasks(tasks: list[dict]) -> None:
    content = "\n".join(json.dumps(t, ensure_ascii=False) for t in tasks) + "\n"
    TASKS_PATH.write_text(content, encoding="utf-8")


def compute_ready_tasks(tasks: list[dict]) -> list[dict]:
    status_by_id = {t["id"]: t["status"] for t in tasks}
    ready: list[dict] = []
    for t in tasks:
        if t.get("status") != "todo":
            continue
        deps = t.get("depends_on") or []
        if all(status_by_id.get(dep) == "done" for dep in deps):
            ready.append(t)
    return ready


def iter_candidate_files() -> list[Path]:
    if (REPO_ROOT / ".git").exists():
        proc = subprocess.run(
            ["git", "ls-files", "-z", "--cached", "--others", "--exclude-standard"],
            cwd=str(REPO_ROOT),
            capture_output=True,
        )
        if proc.returncode == 0:
            raw_paths = [p for p in proc.stdout.decode("utf-8", errors="replace").split("\0") if p]
            return [REPO_ROOT / p for p in raw_paths if (REPO_ROOT / p).is_file()]

    files: list[Path] = []
    for path in REPO_ROOT.rglob("*"):
        if not path.is_file():
            continue
        if any(part in SKIP_DIRS for part in path.relative_to(REPO_ROOT).parts):
            continue
        files.append(path)
    return files


def scan_secrets() -> list[str]:
    findings: list[str] = []
    self_rel = Path(".workflow/scripts/workflow.py")

    for path in iter_candidate_files():
        rel = path.relative_to(REPO_ROOT)
        name_lower = path.name.lower()

        if name_lower in FORBIDDEN_FILE_NAMES or path.suffix.lower() in FORBIDDEN_FILE_EXTS:
            findings.append(f"{rel.as_posix()}: forbidden sensitive file committed/unignored")
            continue

        if path.suffix.lower() in BINARY_EXTS:
            continue

        try:
            text = path.read_text(encoding="utf-8")
        except UnicodeDecodeError:
            continue

        is_self = rel == self_rel
        for lineno, line in enumerate(text.splitlines(), 1):
            for label, pat in SECRET_PATTERNS:
                if pat.search(line):
                    findings.append(f"{rel.as_posix()}:{lineno}: matched {label}")

            if not is_self:
                for label, pat in LOCAL_PATH_PATTERNS:
                    if pat.search(line):
                        findings.append(f"{rel.as_posix()}:{lineno}: leaked {label}")

            if path.name != ".env.example":
                m_env = ENV_ASSIGN_PATTERN.match(line)
                if m_env:
                    val = m_env.group(2).strip("\"'").lower()
                    if val and not val.startswith(SAFE_PLACEHOLDER_PREFIXES):
                        findings.append(
                            f"{rel.as_posix()}:{lineno}: non-placeholder secret assignment for {m_env.group(1)}"
                        )

    return findings


def cmd_scan_secrets(_args: argparse.Namespace) -> int:
    findings = scan_secrets()
    if findings:
        print(f"[SCAN FAIL] Found {len(findings)} potential secret/privacy issue(s):")
        for item in findings:
            print(f"  - {item}")
        return 1
    print("[SCAN PASS] 0 secret or privacy leaks detected.")
    return 0


def run_cmd(cmd: list[str], cwd: Path, env: dict[str, str] | None = None) -> tuple[bool, str]:
    try:
        proc = subprocess.run(
            cmd,
            cwd=str(cwd),
            env=env,
            capture_output=True,
            text=True,
            encoding="utf-8",
            errors="replace",
        )
        output = ((proc.stdout or "") + ("\n" + proc.stderr if proc.stderr else "")).strip()
        return proc.returncode == 0, output
    except FileNotFoundError as exc:
        return False, str(exc)


def verify_task(task_id: str) -> tuple[bool, list[str]]:
    tasks = load_tasks()
    status_by_id = {t["id"]: t["status"] for t in tasks}
    target = next((t for t in tasks if t["id"] == task_id), None)
    if target is None:
        return False, [f"Task not found: {task_id}"]

    errors: list[str] = []

    # 1. Dependency gate
    unmet = [d for d in (target.get("depends_on") or []) if status_by_id.get(d) != "done"]
    if unmet:
        errors.append(f"Blocked by incomplete dependencies: {', '.join(unmet)}")

    # 2. Zero-leak security & privacy gate
    for sf in scan_secrets():
        errors.append(f"[SEC-01] {sf}")

    # 3. Required deliverables check
    for rel_file in target.get("required_files") or []:
        fpath = REPO_ROOT / rel_file
        if not fpath.is_file():
            errors.append(f"Missing required file: {rel_file}")
        elif fpath.stat().st_size == 0:
            errors.append(f"Required file is empty: {rel_file}")

    # Stop before running commands if deliverables are missing
    if errors:
        return False, errors

    # 4. Deterministic acceptance commands + anti-empty-test check
    env = load_dotenv_overlay()
    for spec in target.get("acceptance_cmds") or []:
        cwd_rel = spec.get("cwd") or "."
        cmd = spec.get("cmd") or []
        if not cmd:
            continue
        cwd = REPO_ROOT / cwd_rel
        if not cwd.is_dir():
            errors.append(f"Working directory not found for command {' '.join(cmd)}: {cwd_rel}")
            continue
        ok, out = run_cmd(cmd, cwd, env=env)
        if not ok:
            errors.append(f"Command failed ({' '.join(cmd)} in {cwd_rel}):\n{out}")
            continue

        # Anti-fake-pass check for test suites
        if cmd[0] == "go" and len(cmd) >= 2 and cmd[1] == "test":
            if "no tests to run" in out.lower():
                errors.append(f"Go test reported no tests to run ({' '.join(cmd)})")
            elif not re.search(r"^(?:ok|PASS)\b", out, re.MULTILINE):
                errors.append(f"Go test output missing PASS/ok marker ({' '.join(cmd)})")
        elif "pytest" in cmd[0] or "pytest" in cmd:
            low = out.lower()
            if "no tests ran" in low or "passed" not in low:
                errors.append(f"Pytest did not report passed tests ({' '.join(cmd)})")

    return len(errors) == 0, errors


def cmd_verify_task(args: argparse.Namespace) -> int:
    ok, errors = verify_task(args.task_id)
    if not ok:
        print(f"[TASK GATE FAIL] {args.task_id}")
        for err in errors:
            print(f"  - {err}")
        return 1
    print(f"[TASK GATE PASS] {args.task_id}")
    return 0


def cmd_status(_args: argparse.Namespace) -> int:
    state = load_state()
    tasks = load_tasks()
    counts = {"done": 0, "in_progress": 0, "todo": 0}
    for t in tasks:
        st = t.get("status", "todo")
        counts[st] = counts.get(st, 0) + 1

    ready = compute_ready_tasks(tasks)
    in_prog = [t for t in tasks if t.get("status") == "in_progress"]

    print(f"=== {state.get('project', 'EchoCanvas')} Workflow Status ===")
    print(f"Stage / Milestone : {state.get('current_stage')} / {state.get('current_milestone')}")
    print(f"Active Task ID    : {state.get('active_task_id') or 'None'}")
    print(f"Latest Handoff    : {state.get('latest_handoff')}")
    print(f"Updated At / By   : {state.get('updated_at')} ({state.get('updated_by')})")
    print(
        f"Tasks Summary     : total={len(tasks)} | done={counts.get('done', 0)} "
        f"| in_progress={counts.get('in_progress', 0)} | todo={counts.get('todo', 0)}"
    )
    gates_str = ", ".join(f"{k}={v}" for k, v in state.get("gates", {}).items())
    print(f"Gates             : {gates_str}")

    if in_prog:
        print("\n[In Progress]")
        for t in in_prog:
            print(f"  - {t['id']} [{t['milestone']}] {t['title']} (claimed_by={t.get('claimed_by')})")

    print(f"\n[Ready Tasks ({len(ready)})]")
    if not ready:
        print("  (none)")
    else:
        for t in ready:
            print(f"  - {t['id']} [{t['milestone']}] {t['title']}")
    return 0


def cmd_ready(_args: argparse.Namespace) -> int:
    tasks = load_tasks()
    ready = compute_ready_tasks(tasks)
    if not ready:
        print("No unblocked ready tasks found.")
        return 0

    print(f"=== Ready Tasks ({len(ready)}) ===")
    for t in ready:
        print(f"\n[{t['id']}] ({t['milestone']}) {t['title']}")
        print(f"  FR Refs       : {', '.join(t.get('fr_refs') or [])}")
        print(f"  Depends On    : {', '.join(t.get('depends_on') or []) or 'None'}")
        print(f"  Context Files : {', '.join(t.get('context_files') or [])}")
        print(f"  Verify Cmd    : {t.get('verify_cmd')}")
    return 0


def cmd_claim(args: argparse.Namespace) -> int:
    state = load_state()
    tasks = load_tasks()
    status_by_id = {t["id"]: t["status"] for t in tasks}

    active_id = state.get("active_task_id")
    if active_id and active_id != args.task_id and not args.force:
        print(
            f"[ERROR] Another task is already active: {active_id}. Complete it or pass --force.",
            file=sys.stderr,
        )
        return 1

    target = next((t for t in tasks if t["id"] == args.task_id), None)
    if target is None:
        print(f"[ERROR] Task not found: {args.task_id}", file=sys.stderr)
        return 1
    if target["status"] == "done":
        print(f"[ERROR] Task {args.task_id} is already done.", file=sys.stderr)
        return 1

    unmet = [d for d in (target.get("depends_on") or []) if status_by_id.get(d) != "done"]
    if unmet:
        print(
            f"[ERROR] Task {args.task_id} is blocked by incomplete dependencies: {', '.join(unmet)}",
            file=sys.stderr,
        )
        return 1

    ts = now_iso()
    target["status"] = "in_progress"
    target["claimed_by"] = args.agent
    target["claimed_at"] = ts
    save_tasks(tasks)

    state["active_task_id"] = args.task_id
    state["current_milestone"] = target["milestone"]
    state["updated_at"] = ts
    state["updated_by"] = args.agent
    save_state(state)

    print(f"[OK] Claimed {args.task_id} by {args.agent}")
    print(f"Context files to read: {', '.join(target.get('context_files') or [])}")
    return 0


def cmd_complete(args: argparse.Namespace) -> int:
    if not args.evidence.strip():
        print("[ERROR] --evidence must not be empty.", file=sys.stderr)
        return 1

    if not args.skip_verify:
        ok, errors = verify_task(args.task_id)
        if not ok:
            print(f"[TASK GATE FAIL] Cannot complete {args.task_id}:", file=sys.stderr)
            for err in errors:
                print(f"  - {err}", file=sys.stderr)
            return 1
        print(f"[TASK GATE PASS] {args.task_id}")

    state = load_state()
    tasks = load_tasks()
    target = next((t for t in tasks if t["id"] == args.task_id), None)
    if target is None:
        print(f"[ERROR] Task not found: {args.task_id}", file=sys.stderr)
        return 1
    if target["status"] == "done":
        print(f"[WARN] Task {args.task_id} was already marked done; updating evidence.")

    ts = now_iso()
    target["status"] = "done"
    target["evidence"] = args.evidence.strip()
    target["completed_at"] = ts
    save_tasks(tasks)

    if state.get("active_task_id") == args.task_id:
        state["active_task_id"] = None
    state["updated_at"] = ts
    save_state(state)

    print(f"[OK] Completed {args.task_id}")
    next_ready = compute_ready_tasks(tasks)
    if next_ready:
        next_ids = ", ".join(f"{t['id']} ({t['milestone']})" for t in next_ready)
        print(f"[AUTO-ADVANCE] Ready next: {next_ids}")
    else:
        print("[AUTO-ADVANCE] No remaining ready tasks (milestone/project check due).")
    return 0


def next_handoff_filename(date_str: str) -> str:
    HANDOFFS_DIR.mkdir(parents=True, exist_ok=True)
    pattern = re.compile(rf"^{re.escape(date_str)}_(\d{{3}})_.*\.md$")
    max_seq = 0
    for item in HANDOFFS_DIR.iterdir():
        m = pattern.match(item.name)
        if m:
            max_seq = max(max_seq, int(m.group(1)))
    return f"{date_str}_{max_seq + 1:03d}_handoff.md"


def cmd_handoff(args: argparse.Namespace) -> int:
    state = load_state()
    tasks = load_tasks()
    ts = now_iso()
    date_str = ts[:10]
    filename = next_handoff_filename(date_str)
    rel_path = f".workflow/handoffs/{filename}"
    out_path = HANDOFFS_DIR / filename

    done_tasks = [t for t in tasks if t.get("status") == "done"]
    in_prog = [t for t in tasks if t.get("status") == "in_progress"]
    ready = compute_ready_tasks(tasks)

    top_files: list[str] = [rel_path, ".workflow/decisions.md"]
    if args.files:
        for f in args.files.split(","):
            cleaned = f.strip()
            if cleaned and cleaned not in top_files:
                top_files.append(cleaned)
    elif ready:
        for f in ready[0].get("context_files") or []:
            if f not in top_files:
                top_files.append(f)

    next_task_id = in_prog[0]["id"] if in_prog else (ready[0]["id"] if ready else "<NEXT_TASK_ID>")
    agent_tag = f"{args.agent}/{args.model}"

    done_lines = "\n".join(
        f"- [x] `{t['id']}` {t['title']}（证据：{t.get('evidence') or 'N/A'}）"
        for t in done_tasks[-5:]
    ) or "- （暂无）"
    in_prog_lines = "\n".join(
        f"- [ ] `{t['id']}` {t['title']}（claimed_by={t.get('claimed_by')}）"
        for t in in_prog
    ) or "- （无进行中任务）"
    top_files_md = "\n".join(f"{i}. `{p}`" for i, p in enumerate(top_files[:5], 1))

    content = f"""# 会话交接快照（{filename[:-3]}）

* **交接编号**：`{filename[:-11]}`
* **交接时间**：`{ts}`
* **移交 Agent / 模型**：`{agent_tag}`
* **当前阶段与里程碑**：`{state.get('current_stage')} / {state.get('current_milestone')}`

---

## 1. 本轮摘要与最新已完成事项
{args.summary.strip()}

### 最近已完成任务（最多展示最近 5 项）
{done_lines}

### 当前进行中任务
{in_prog_lines}

## 2. 下一步计划与交接指引
{args.next.strip()}

## 3. 已知阻塞与踩坑（Pitfalls）
{(args.pitfalls or "无新增阻塞").strip()}

## 4. 下一任 Agent 必读文件（上下文防火墙）
{top_files_md}

## 5. 推荐接力命令
```bash
python .workflow/scripts/workflow.py status
python .workflow/scripts/workflow.py ready
python .workflow/scripts/workflow.py claim {next_task_id} --agent "<your_tool>/<your_model>"
```
"""
    out_path.write_text(content, encoding="utf-8")

    state["latest_handoff"] = rel_path
    state["updated_at"] = ts
    state["updated_by"] = agent_tag
    save_state(state)

    print(f"[OK] Handoff created: {rel_path}")
    return 0


def check_milestone_tasks_done(tasks: list[dict], milestone: str) -> list[str]:
    return [
        t["id"]
        for t in tasks
        if t.get("milestone") == milestone and t.get("status") != "done"
    ]


def cmd_verify_gate(args: argparse.Namespace) -> int:
    stage = args.stage.upper()
    tasks = load_tasks()
    errors: list[str] = []
    env = load_dotenv_overlay()

    secret_findings = scan_secrets()
    for sf in secret_findings:
        errors.append(f"[SEC-01] {sf}")

    if stage == "S1":
        charter = REPO_ROOT / "docs" / "01-CHARTER.md"
        if not charter.is_file():
            errors.append("Missing docs/01-CHARTER.md")
        incomplete = check_milestone_tasks_done(tasks, "S1")
        if incomplete:
            errors.append(f"Incomplete S1 tasks: {', '.join(incomplete)}")

    elif stage == "S2":
        prd = REPO_ROOT / "docs" / "02-PRD.md"
        if not prd.is_file():
            errors.append("Missing docs/02-PRD.md")
        else:
            text = prd.read_text(encoding="utf-8")
            required = [
                "FR-A", "FR-B", "FR-C", "FR-D", "FR-E", "FR-F", "FR-G",
                "Gate 1", "Gate 2", "Gate 3",
                "AC-01", "AC-02", "AC-03", "AC-04", "AC-05", "AC-06",
            ]
            missing = [token for token in required if token not in text]
            if missing:
                errors.append(f"docs/02-PRD.md missing required markers: {', '.join(missing)}")
        incomplete = check_milestone_tasks_done(tasks, "S2")
        if incomplete:
            errors.append(f"Incomplete S2 tasks: {', '.join(incomplete)}")

    elif stage == "S3":
        tdd = REPO_ROOT / "docs" / "03-TDD.md"
        if not tdd.is_file():
            errors.append("Missing docs/03-TDD.md")
        else:
            text = tdd.read_text(encoding="utf-8")
            tables = [
                "projects",
                "asset_style_profiles",
                "story_jobs",
                "story_frames",
                "frame_versions",
                "frame_tasks",
                "episodic_entity_states",
                "usage_ledger",
            ]
            for tbl in tables:
                if not re.search(rf"CREATE\s+TABLE\s+{tbl}\b", text):
                    errors.append(f"docs/03-TDD.md missing DDL for table: {tbl}")
            for adr in ("ADR-01", "ADR-02", "ADR-03", "ADR-04"):
                if adr not in text:
                    errors.append(f"docs/03-TDD.md missing {adr}")
        incomplete = check_milestone_tasks_done(tasks, "S3")
        if incomplete:
            errors.append(f"Incomplete S3 tasks: {', '.join(incomplete)}")

    elif stage == "M1":
        required_files = [
            REPO_ROOT / "deploy" / "docker-compose.yml",
            REPO_ROOT / "deploy" / "sql" / "001_init.sql",
            REPO_ROOT / "services" / "go-gateway" / "go.mod",
        ]
        for rf in required_files:
            if not rf.is_file():
                errors.append(f"Missing required M1 file: {rf.relative_to(REPO_ROOT)}")
        incomplete = check_milestone_tasks_done(tasks, "M1")
        if incomplete:
            errors.append(f"Incomplete M1 tasks: {', '.join(incomplete)}")
        go_dir = REPO_ROOT / "services" / "go-gateway"
        if (go_dir / "go.mod").is_file():
            ok_vet, out_vet = run_cmd(["go", "vet", "./..."], go_dir, env=env)
            if not ok_vet:
                errors.append(f"go vet ./... failed:\n{out_vet}")
            ok_test, out_test = run_cmd(["go", "test", "-count=1", "./..."], go_dir, env=env)
            if not ok_test:
                errors.append(f"go test -count=1 ./... failed:\n{out_test}")

    elif stage == "M2":
        py_dir = REPO_ROOT / "services" / "py-agent"
        if not (py_dir / "pyproject.toml").is_file():
            errors.append("Missing services/py-agent/pyproject.toml")
        incomplete = check_milestone_tasks_done(tasks, "M2")
        if incomplete:
            errors.append(f"Incomplete M2 tasks: {', '.join(incomplete)}")
        if (py_dir / "pyproject.toml").is_file():
            ok, out = run_cmd([sys.executable, "-m", "pytest"], py_dir, env=env)
            if not ok:
                errors.append(f"pytest failed:\n{out}")

    elif stage == "M3":
        report = REPO_ROOT / "eval" / "reports" / "latest_eval_report.md"
        if not report.is_file():
            errors.append("Missing eval/reports/latest_eval_report.md")
        incomplete = check_milestone_tasks_done(tasks, "M3")
        if incomplete:
            errors.append(f"Incomplete M3 tasks: {', '.join(incomplete)}")

    elif stage == "M4":
        pkg = REPO_ROOT / "apps" / "web" / "package.json"
        if not pkg.is_file():
            errors.append("Missing apps/web/package.json")
        incomplete = check_milestone_tasks_done(tasks, "M4")
        if incomplete:
            errors.append(f"Incomplete M4 tasks: {', '.join(incomplete)}")

    else:
        print(f"[ERROR] Unknown gate stage: {stage}", file=sys.stderr)
        return 1

    if errors:
        print(f"[GATE FAIL] {stage}")
        for err in errors:
            print(f"  - {err}")
        return 1

    if args.update_state:
        state = load_state()
        state.setdefault("gates", {})[stage] = "passed"
        if stage in NEXT_MILESTONE and state.get("current_milestone") == stage:
            nxt = NEXT_MILESTONE[stage]
            if nxt == "DONE":
                state["current_stage"] = "S6"
            elif nxt == "M4":
                state["current_stage"] = "S5"
            state["current_milestone"] = nxt
        state["updated_at"] = now_iso()
        save_state(state)

    print(f"[GATE PASS] {stage}")
    return 0


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description="EchoCanvas Git-native workflow CLI")
    sub = parser.add_subparsers(dest="command", required=True)

    p_status = sub.add_parser("status", help="Show workflow status")
    p_status.set_defaults(func=cmd_status)

    p_ready = sub.add_parser("ready", help="List unblocked tasks")
    p_ready.set_defaults(func=cmd_ready)

    p_claim = sub.add_parser("claim", help="Claim an unblocked task")
    p_claim.add_argument("task_id", help="Task ID, e.g. T-M1-01")
    p_claim.add_argument("--agent", required=True, help="Agent/Model label, e.g. codex/gpt-5")
    p_claim.add_argument("--force", action="store_true", help="Override active task guard")
    p_claim.set_defaults(func=cmd_claim)

    p_vtask = sub.add_parser("verify-task", help="Run strict acceptance gate for a single task")
    p_vtask.add_argument("task_id", help="Task ID, e.g. T-M1-03")
    p_vtask.set_defaults(func=cmd_verify_task)

    p_complete = sub.add_parser("complete", help="Verify acceptance gate and mark task done")
    p_complete.add_argument("task_id", help="Task ID, e.g. T-M1-01")
    p_complete.add_argument("--evidence", required=True, help="Verification command/result summary")
    p_complete.add_argument("--skip-verify", action="store_true", help="Skip verify-task check (discouraged)")
    p_complete.set_defaults(func=cmd_complete)

    p_handoff = sub.add_parser("handoff", help="Create structured handoff snapshot")
    p_handoff.add_argument("--agent", required=True, help="Tool name, e.g. claude-code")
    p_handoff.add_argument("--model", required=True, help="Model name, e.g. sonnet")
    p_handoff.add_argument("--summary", required=True, help="What was done and verified")
    p_handoff.add_argument("--next", required=True, help="Recommended next step")
    p_handoff.add_argument("--pitfalls", default="", help="Known pitfalls or blockers")
    p_handoff.add_argument("--files", default="", help="Comma-separated priority files for next agent")
    p_handoff.set_defaults(func=cmd_handoff)

    p_scan = sub.add_parser("scan-secrets", help="Audit repository for leaked secrets or private info")
    p_scan.set_defaults(func=cmd_scan_secrets)

    p_gate = sub.add_parser("verify-gate", help="Run executable stage-gate checks")
    p_gate.add_argument("stage", help="Stage/Milestone: S1, S2, S3, M1, M2, M3, M4")
    p_gate.add_argument("--update-state", action="store_true", help="Mark gate passed in state.json")
    p_gate.set_defaults(func=cmd_verify_gate)

    return parser


def main() -> int:
    parser = build_parser()
    args = parser.parse_args()
    return args.func(args)


if __name__ == "__main__":
    raise SystemExit(main())
