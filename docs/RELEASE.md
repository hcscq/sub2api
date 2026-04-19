# Release Playbook

This document describes the Sub2API release flow used in `hcscq/sub2api`, including Git tag creation, GitHub Actions monitoring, GHCR validation, and local rollout verification.

## Release Model

- The release workflow lives in `.github/workflows/release.yml`.
- It runs on:
  - `push` of tags matching `v*`
  - `workflow_dispatch`
- The workflow:
  - writes the release version into `backend/cmd/server/VERSION`
  - builds the frontend artifact
  - runs GoReleaser
  - publishes the GitHub Release
  - publishes GHCR images
  - syncs `backend/cmd/server/VERSION` back to the default branch after a successful release

## Recommended Path

Use an annotated tag push. The tag body becomes the GitHub Release notes.

## Before You Release

1. Make sure the intended changes are committed.
2. If the worktree is dirty, commit only the files that belong to the release.
3. Push the branch commit before pushing the tag.
4. Decide whether this is:
   - a normal release: full GoReleaser config from `.goreleaser.yaml`
   - a simple release: x86_64-only GHCR image from `.goreleaser.simple.yaml`

## Standard Release Steps

### 1. Sync and inspect

```bash
git fetch fork --tags
git status --short --branch
git log --oneline --decorate -n 5
git tag --sort=-creatordate | head -10
```

### 2. Push the branch commit

```bash
git push fork main
```

### 3. Create an annotated tag

The tag title should be short. The tag body should contain the release notes you want to appear on GitHub.

```bash
git tag -a v0.1.132 -m "Document the release flow and add a release skill" -m "- add maintainer release playbooks in docs/RELEASE*.md
- add a repo-local Codex skill for releasing, monitoring, and local rollout
- add a GitHub API watcher script for release workflow tracking"
```

### 4. Push the tag

```bash
git push fork v0.1.132
```

## Monitor the GitHub Release Workflow

This repo includes a local watcher script:

```bash
python3 .codex/skills/sub2api-release/scripts/watch_release.py \
  --repo hcscq/sub2api \
  --tag v0.1.132
```

Useful options:

- `--interval 15`: poll every 15 seconds
- `--timeout 3600`: wait up to one hour
- `--run-id <id>`: monitor a specific `workflow_dispatch` run

The script waits for the `release.yml` workflow run, prints status transitions, and emits a JSON summary with:

- workflow run URL
- final conclusion
- per-job results
- GitHub Release URL and publish timestamp when available

## Verify the Published Image

After the workflow succeeds:

```bash
docker pull ghcr.io/hcscq/sub2api:v0.1.132
docker image inspect ghcr.io/hcscq/sub2api:v0.1.132 \
  --format '{{index .RepoDigests 0}}'
```

The inspect output returns the pinned digest form:

```text
ghcr.io/hcscq/sub2api:v0.1.132@sha256:...
```

Use that exact digest in production rollout when possible.

## Local Smoke Test

At minimum, verify the image can report its embedded version:

```bash
docker run --rm ghcr.io/hcscq/sub2api:v0.1.132 /app/sub2api -version
```

If you are validating the maintained host that already runs Sub2API, roll the deployment forward from the deployment directory. On the current host this directory is `/root/sub2api-deploy`.

1. Update the `sub2api` image reference in `docker-compose.yml` to the new pinned digest.
2. Pull and recreate only the application container.

```bash
cd /root/sub2api-deploy
docker compose pull sub2api
docker compose up -d sub2api
docker compose ps sub2api
curl -fsS http://127.0.0.1:8080/health
docker exec sub2api /app/sub2api -version
```

If the host is not the canonical maintainer host, replace `/root/sub2api-deploy` with the correct deployment directory.

## Rollback

Rollback is done by restoring the previous pinned image digest in the deployment compose file and recreating the container:

```bash
cd /root/sub2api-deploy
docker compose up -d sub2api
docker compose ps sub2api
curl -fsS http://127.0.0.1:8080/health
```

Keep the previous digest in commit history or deployment notes so rollback does not require guessing.

## Simple Release

`release.yml` supports a "simple release" mode that builds only the x86_64 GHCR image and skips the wider artifact set.

- `push tag` path: controlled by the repository variable `SIMPLE_RELEASE`
- `workflow_dispatch` path: controlled by the `simple_release` input

Use simple release only when you explicitly want the reduced artifact set.

## Notes

- `backend/cmd/server/VERSION` is synchronized by the workflow after a successful release. Pull `main` again after the workflow completes if you want the local file to match the published version.
- The tag body is treated as the release note body. Write it carefully before pushing the tag.
- If you need manual triggering instead of tag push, use GitHub Actions UI or call the `workflow_dispatch` endpoint with a token that can run workflows.
