# Contributing

Thanks for contributing to Cloud Native Mail Service (CNMS).

## Development setup

Requirements: Go 1.26+, Node.js 20+ (for the docs site). On NixOS the Go
toolchain can be obtained with `nix-shell -p go_1_26` (or newer). If your home
directory does not allow creating new top-level directories, point Go's caches
at a writable location, e.g.:

```sh
export GOPATH="$HOME/.cache/go" GOMODCACHE="$HOME/.cache/go-mod"
```

Common targets (run from the repository root):

```sh
make test            # unit + envtest tests
make lint            # golangci-lint (installs the pinned version into ./bin)
make license-check   # dependency license check (fails on restricted/forbidden/unknown)
make license-report  # regenerate docs/reference/third-party-licenses.md
make docs-build      # build the Docusaurus site under website/
make test-e2e        # e2e on an isolated kind cluster (needs kind + docker)
```

## Commits and PRs

- Conventional Commits (`feat:`, `fix:`, `docs:`, `chore:`, ...), subject <= 50
  chars, body wrapped at 72.
- Small, reviewable commits. Do not mix refactors with features.
- Docs are part of every change: new or changed behaviour needs a doc page or
  update, and the docs site must build without broken links.
- New dependencies need a license check: run `make license-check`, and record
  the decision (version, license) in `docs/decisions/`.
- Engine and tool versions are pinned in the Makefile, Dockerfile, and
  workflows (pinned by commit SHA). Renovate proposes updates; engine updates
  always open a PR and must pass the golden-file and config-validation tests.

## Project layout

See `AGENTS.md` for the project guide (kubebuilder conventions, marker
reference, controller design rules) and `TASK.md` for the project decisions
and phase plan.
