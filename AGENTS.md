# Sub2API Agent Notes

This file defines repository-specific instructions for coding agents working in `sub2api`.

## Scope

- Keep instructions here limited to repo-specific workflow constraints.
- Do not duplicate the full release handbook here; link to the canonical docs instead.

## Release And Rollout

- Official releases are built from the GitHub-side release pipeline, not from a maintainer's local machine.
- The canonical release workflow is `.github/workflows/release.yml`.
- Normal release flow is:
  1. commit the intended changes
  2. push the branch
  3. create and push an annotated `v*` tag, or use `workflow_dispatch`
  4. let GitHub Actions + GoReleaser build and publish the release artifacts and GHCR image
  5. pull the published GHCR image on the target host and roll it out there
- Treat GitHub-built images as the source of truth for production rollout.
- When updating deployments, prefer the pinned GHCR digest instead of a floating tag.

## Local Build Policy

- `deploy/build_image.sh` is for manual local debugging only.
- Do not use local `docker build`, local GoReleaser runs, or ad hoc local image builds as the normal production release path.
- If a task asks for "publish", "release", or "上线", default to the GitHub Actions / GHCR flow unless the user explicitly says this is only a local debug build.

## Canonical Docs

- Release handbook: `docs/RELEASE_CN.md`
- English release playbook: `docs/RELEASE.md`
- Repo-local release skill: `.codex/skills/sub2api-release/SKILL.md`

## Deployment Host Assumption

- The current maintainer host deployment directory is `/root/sub2api-deploy`.
- Production rollout means updating the deployed image reference there to the newly published GHCR digest, then recreating the `sub2api` service.
