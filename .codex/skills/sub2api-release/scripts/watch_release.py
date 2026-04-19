#!/usr/bin/env python3
from __future__ import annotations

import argparse
import json
import os
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
from typing import Any


API_ROOT = "https://api.github.com"
USER_AGENT = "sub2api-release-watch/1.0"


def now_utc() -> str:
    return time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())


def log(message: str) -> None:
    print(f"[{now_utc()}] {message}", file=sys.stderr, flush=True)


def github_get(path: str) -> Any:
    url = path if path.startswith("http://") or path.startswith("https://") else f"{API_ROOT}{path}"
    headers = {
        "Accept": "application/vnd.github+json",
        "User-Agent": USER_AGENT,
    }
    token = os.environ.get("GITHUB_TOKEN") or os.environ.get("GH_TOKEN")
    if token:
        headers["Authorization"] = f"Bearer {token}"

    req = urllib.request.Request(url, headers=headers)
    try:
        with urllib.request.urlopen(req, timeout=30) as resp:
            return json.load(resp)
    except urllib.error.HTTPError as exc:
        body = exc.read().decode("utf-8", errors="replace")
        raise RuntimeError(f"GitHub API request failed: {exc.code} {exc.reason}: {body}") from exc
    except urllib.error.URLError as exc:
        raise RuntimeError(f"GitHub API request failed: {exc.reason}") from exc


def find_run(repo: str, workflow: str, tag: str) -> dict[str, Any] | None:
    payload = github_get(
        f"/repos/{repo}/actions/workflows/{urllib.parse.quote(workflow, safe='')}/runs"
        "?per_page=30&exclude_pull_requests=true"
    )
    for run in payload.get("workflow_runs", []):
        if run.get("head_branch") == tag:
            return run
    return None


def fetch_run(repo: str, run_id: int) -> dict[str, Any]:
    return github_get(f"/repos/{repo}/actions/runs/{run_id}")


def fetch_jobs(jobs_url: str) -> list[dict[str, Any]]:
    payload = github_get(jobs_url)
    return payload.get("jobs", [])


def fetch_release(repo: str, tag: str) -> dict[str, Any] | None:
    try:
        return github_get(f"/repos/{repo}/releases/tags/{urllib.parse.quote(tag, safe='')}")
    except RuntimeError as exc:
        if "404" in str(exc):
            return None
        raise


def compact_jobs(jobs: list[dict[str, Any]]) -> list[dict[str, Any]]:
    return [
        {
            "name": job.get("name"),
            "status": job.get("status"),
            "conclusion": job.get("conclusion"),
            "html_url": job.get("html_url"),
        }
        for job in jobs
    ]


def wait_for_release(repo: str, tag: str, interval: int, deadline: float) -> dict[str, Any] | None:
    while time.time() < deadline:
        release = fetch_release(repo, tag)
        if release is not None:
            return release
        log(f"release {tag} not published yet, retrying in {interval}s")
        time.sleep(interval)
    return None


def main() -> int:
    parser = argparse.ArgumentParser(description="Watch the Sub2API GitHub release workflow.")
    parser.add_argument("--repo", default="hcscq/sub2api", help="GitHub repo in owner/name format")
    parser.add_argument("--tag", required=True, help="Target tag, for example v0.1.132")
    parser.add_argument("--workflow", default="release.yml", help="Workflow file name")
    parser.add_argument("--run-id", type=int, default=0, help="Existing workflow run id")
    parser.add_argument("--interval", type=int, default=15, help="Polling interval in seconds")
    parser.add_argument("--timeout", type=int, default=1800, help="Timeout in seconds")
    args = parser.parse_args()

    deadline = time.time() + args.timeout
    run: dict[str, Any] | None = None
    last_status: tuple[str | None, str | None] | None = None

    while time.time() < deadline:
        if args.run_id:
            run = fetch_run(args.repo, args.run_id)
        else:
            run = find_run(args.repo, args.workflow, args.tag)

        if run is None:
            log(f"waiting for workflow {args.workflow} for tag {args.tag}")
            time.sleep(args.interval)
            continue

        status_pair = (run.get("status"), run.get("conclusion"))
        if status_pair != last_status:
            log(
                f"run {run.get('id')} status={run.get('status')} conclusion={run.get('conclusion')} "
                f"url={run.get('html_url')}"
            )
            last_status = status_pair

        if run.get("status") == "completed":
            jobs = compact_jobs(fetch_jobs(run["jobs_url"]))
            release = wait_for_release(args.repo, args.tag, args.interval, min(deadline, time.time() + 300))
            summary = {
                "repo": args.repo,
                "tag": args.tag,
                "workflow": args.workflow,
                "run_id": run.get("id"),
                "run_url": run.get("html_url"),
                "status": run.get("status"),
                "conclusion": run.get("conclusion"),
                "jobs": jobs,
                "release_url": None if release is None else release.get("html_url"),
                "published_at": None if release is None else release.get("published_at"),
            }
            print(json.dumps(summary, ensure_ascii=True, indent=2))
            return 0 if run.get("conclusion") == "success" else 1

        time.sleep(args.interval)

    log(f"timed out after {args.timeout}s waiting for tag {args.tag}")
    return 124


if __name__ == "__main__":
    raise SystemExit(main())
