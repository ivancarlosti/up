# Commit Convention

This project uses [semantic-release](https://semantic-release.gitbook.io/semantic-release) with the [Conventional Commits](https://www.conventionalcommits.org/) specification to automate versioning and releases.

## Format

```
<type>[(scope)]: <description>

[optional body]

[optional footer(s)]
```

## Types & Version Impact

| Type | Bump | Example |
|------|------|---------|
| `feat` | **MINOR** | `feat(auth): add OAuth2 social login` |
| `fix` | **PATCH** | `fix(upload): handle zero-byte files gracefully` |
| `perf` | **PATCH** | `perf(db): add index on images.uploaded_at` |
| `refactor` | **PATCH** | `refactor: extract image resize logic` |
| `docs` | **PATCH** | `docs: update API authentication guide` |
| `style` | **PATCH** | `style: format blade templates consistently` |
| `test` | **PATCH** | `test: add integration tests for image upload` |
| `build` | **PATCH** | `build: update tailwindcss to v4` |
| `ci` | **PATCH** | `ci: add docker layer caching` |
| `chore` | **none** | `chore: update .gitignore` |

## Breaking Changes — MAJOR bump

Add `!` after the type, or `BREAKING CHANGE:` in the footer:

```
feat!(api): drop support for legacy token auth

BREAKING CHANGE: Legacy tokens are no longer accepted.
Use OAuth2 bearer tokens instead.
```

or simply:

```
feat!: remove deprecated /v1/image endpoint
```

## Rules

1. **Every commit to `main` must follow this format.** PR squash-merge commits must use a conventional prefix.
2. The `type` is case-sensitive and must be lowercase.
3. The `scope` is optional but encouraged (e.g., `auth`, `upload`, `db`, `docker`).
4. The description should be imperative, present tense: `"add"` not `"added"`.
5. Breaking changes (`!` or `BREAKING CHANGE:` footer) trigger a **MAJOR** version bump.
6. `chore` commits do **not** trigger a release.

## Examples

```bash
git commit -m "feat(images): add WebP thumbnail generation"
git commit -m "fix(auth): prevent session fixation on login"
git commit -m "perf: cache image metadata queries in Redis"
git commit -m "feat!(api): remove GET /legacy/images endpoint"
git commit -m "docs: add docker deployment guide"
git commit -m "chore: bump Laravel to v11.10"
```

## Automated Release Flow

```
Developer pushes to main
        │
        ▼
semantic-release.yml runs
        │
        ├── Analyzes commits since last tag
        ├── Determines next version (MAJOR / MINOR / PATCH)
        ├── Stamps version into project metadata files
        ├── Commits version-stamped files (skip ci)
        ├── Creates git tag (vX.Y.Z)
        └── Creates GitHub Release with changelog
                │
                └──▶ Dispatches build.yml
                        │
                        ├── Uploads ZIP package to release
                        └── Builds & pushes Docker image to ghcr.io
```
