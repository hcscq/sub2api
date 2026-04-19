---
name: sub2api-release
description: Use when you need to publish a new hcscq/sub2api release, monitor the GitHub Release workflow without gh CLI, then pull, verify, and roll out the GHCR image from the current host.
---

# Sub2API Release

Use this skill when the task is to release `hcscq/sub2api`, monitor the GitHub image build, and validate or roll out the result locally.

## Read First

- For the full maintainer playbook, read `../../../docs/RELEASE_CN.md`.
- The release workflow is `.github/workflows/release.yml`.
- The current maintainer host deploy directory is `/root/sub2api-deploy`.

## Required Workflow

1. Inspect repo state before doing anything:
   - `git status --short --branch`
   - `git remote -v`
   - `git tag --sort=-creatordate | head`
2. If the worktree is dirty, do not revert user changes. Commit only the files that belong to the release task.
3. Push the branch commit before pushing the release tag.
4. Create an annotated tag. The tag body becomes the GitHub Release notes.
5. Push the tag to `fork`.
6. Monitor the workflow with:

```bash
python3 .codex/skills/sub2api-release/scripts/watch_release.py --repo hcscq/sub2api --tag <tag>
```

7. After success:
   - `docker pull ghcr.io/hcscq/sub2api:<tag>`
   - capture the digest with `docker image inspect --format '{{index .RepoDigests 0}}'`
8. For local rollout on the maintainer host:
   - update `/root/sub2api-deploy/docker-compose.yml` to the new pinned digest
   - `docker compose pull sub2api`
   - `docker compose up -d sub2api`
   - validate `/health` and `/app/sub2api -version`

## Manual Dispatch

If the release is started with `workflow_dispatch` instead of tag push, prefer passing the run id to the watcher:

```bash
python3 .codex/skills/sub2api-release/scripts/watch_release.py --repo hcscq/sub2api --tag <tag> --run-id <id>
```

## Validation

Use this validation order:

1. GitHub Actions run reaches `completed/success`
2. GitHub Release for the target tag exists
3. `docker pull ghcr.io/hcscq/sub2api:<tag>` succeeds
4. The image reports the expected version
5. The deployed container becomes healthy
6. `curl -fsS http://127.0.0.1:8080/health` succeeds

## Rollback

Rollback means restoring the previous pinned digest in `/root/sub2api-deploy/docker-compose.yml` and recreating the `sub2api` service only. Never guess the previous digest; fetch it from deployment history or the compose file backup.
