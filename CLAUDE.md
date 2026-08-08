# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

`readlist` scores a private Calibre library (2,054 books) across **7 independent
dimensions** and publishes public book lists = weighted "preset" profiles over those
dimensions, each book also tagged with reading status. The public site only ever
serves **books that made a list** — never the full library catalog. Comments,
commits, and docs are in Chinese; match that when editing docs/comments (Go
identifiers stay English).

Docs are split by reader: `README.md` is the visitor/owner-facing entry point,
`docs/guide/` is the owner's operational manual (`operating.md` — add a list, debug an
empty list, run the pipeline against the real library, which metrics matter;
`deploy.md` — going live), `docs/spec/` holds the specs, `docs/archive/` holds the three
review records (historical, NOT current spec — never cite them as the rule).
`docs/spec/system-design.md` is the authoritative architecture spec; the superseded
§3–§5 of `docs/spec/architecture.md` have been deleted, only §1/§2/§6/§7 remain.

## Commands

```bash
make check          # fmt-check + vet + go test — run this before considering work done
make build           # go build -> bin/readlist
make run             # build + ./bin/readlist serve (:8080, embedded SPA)
make test-go         # go test ./cmd/... ./internal/...  (offline, GOPROXY=off)
make test-race       # same, with -race
make fmt / fmt-check # gofmt over cmd/ internal/
make vet
make smoke           # build + seed + score + dryrun against demo corpus (no k8s)
make e2e             # full kind cluster e2e: build image, load, deploy, assert API (scripts/e2e-kind.sh)
make pipeline SOURCE_METADATA_DB=... SOURCE_APP_DB=...  # snapshot+ingest+score against a real calibre library
```

Single test: `GOPROXY=off go test ./internal/score/ -run TestCombineCoverageRenormalizes -v`

**Never run `go test ./...`** — it scans `internal/api/dist` and other non-package
dirs. Always scope to `./cmd/... ./internal/...` (this is what `GOPKGS` in the
Makefile does).

The SPA is a pure view layer: rank, TBS and coverage all arrive precomputed from the
server and are rendered as-is. Keep it that way. There used to be a weight-slider that
reimplemented `score.Combine` in `app.js`, which forced a `make test-spa` target and a
hard Node dependency in CI just to keep two copies of one formula in sync — for a
feature that could only reorder the ≤20 books already selected. If you are tempted to
re-add client-side scoring, note that changing the weights properly means re-running
*selection* (de-dup + diversity caps), which is not a client-side dot product.

## CLI (`cmd/readlist/main.go`)

Single binary, subcommand dispatch, default command is `serve`:

```
readlist snapshot   # ONLY command that touches the Calibre volumes; no network. VACUUM INTO + minimal reading-status export.
readlist ingest      # ONLY command that makes network calls (Google Books / OpenLibrary / HN Algolia); quota-budgeted, resumable.
readlist score        # judgement + selection + publish; MUST NOT make network requests (enforced by convention, not code — don't break it)
readlist dryrun       # counts only: per-dim measured %, per-preset candidate pool size — run this first when tuning weights
readlist diff A B     # rank diff between two runs (required material for scoring-standard version bumps)
readlist serve         # read-only API + embedded SPA; self-scores once if no run has been published yet
readlist seed          # writes 50-work demo corpus, idempotent — local/kind demo only, never in prod initContainer
readlist init           # seed + first score, idempotent (skips scoring if a run is already published) — used by the kind initContainer
```

In production these run as separate nightly CronJobs (`snapshot → ingest → score`),
deliberately split so **the job that can reach the Calibre PVCs has no network
egress, and the job with network egress never mounts the Calibre PVCs** — this
isolation is the load-bearing security boundary of the whole system (see
`docs/spec/architecture.md` §2 and `docs/spec/system-design.md` §11).

## Architecture: three layers, not two

The scoring pipeline is deliberately split into three layers with different change
frequencies (`docs/spec/system-design.md` §0 has the full diagram):

1. **facts** (`internal/facts`, `internal/calibre`) — raw measurable data + source/confidence.
   Cheapest to get wrong, most expensive to change (external quota-bound). Cached as
   raw JSON with TTL; recomputation never re-hits external APIs.
2. **judgement** (`internal/score`) — dimension scores = facts run through
   versioned formulas + corpus-relative normalization (0–100). Changing a formula =
   bumping `standard_version`; this layer recomputes in seconds, fully offline.
3. **selection** (`internal/selection`, `internal/preset`) — list = judgement +
   per-dimension admission gates + de-dup + diversity constraints + truncation +
   deterministic reason string. Adding a list = adding YAML to `internal/preset/presets.yaml`,
   no code change, no rescoring (`docs/spec/system-design.md` §5).

Package map:

```
cmd/readlist/          CLI entrypoint, command dispatch, run summaries
internal/calibre/       reads the two Calibre SQLite DBs (metadata.db via VACUUM INTO, app.db minimal 3-table export)
internal/corpus/        snapshot import, work clustering (edition -> work), publisher normalization, half-life rules
internal/facts/         external evidence ingestion — Google Books / OpenLibrary / HN Algolia, quota + TTL cache
internal/preset/        loads + validates presets.yaml (weights sum to 1, bands ⊆ weights, dim names/states valid)
internal/score/         the 7 dimensions (dims.go), CDF normalization + mid-rank (norm.go), per-book renormalize + band (combine.go), Engine orchestrates a full run (engine.go)
internal/selection/     admission (needs/min_coverage) + diversity caps (max_per_topic/max_per_author) + reason strings
internal/api/            read-only HTTP API + embedded SPA (dist/ is prebuilt static app.js/index.html/style.css — no frontend build step)
internal/store/          SQLite open/migrate (single connection, WAL, migrations/*.sql applied in a transaction each)
internal/config/         all runtime config, env-var driven (see config.go for the full list)
```

### The 7 dimensions and their evidence states

Dims (`internal/score/dims.go`): `A` acclaim (Bayesian-shrunk cross-source rating),
`C` community (HN mentions, time-decayed), `F` freshness (topic half-life decay from
trusted pubdate), `T` trust (publisher tier × author known-ness), `D` depth /
`P` practicality (LLM-labeled, confidence-gated), `readability` (local metadata
completeness, always 100% computable).

**`D` and `P` have no production data source yet.** They read from the `labels` table,
and the only writer of `labels` in the whole repo is `corpus.Seed` (the 50-work demo
corpus) — LLM labeling is roadmap step 6 and is not implemented. On the real library
both dims are permanently `unknown`, and so are `works.level` (written as `""` by
`corpus.Import` on purpose) and `WorkInput.Topics` (also labels-only). Consequences to
keep in mind: no shipping preset may put weight on `D`/`P` or filter on `level` /
`topics_any` — such a list is structurally empty in production while looking fine
against the demo corpus, and a dead weight also shows up as a false claim in the
list page's public 口径 line. `score.Grade` is insulated from this: it only grades the
dims that actually rank (`score.GradedDims` = union of non-zero preset weights), so
D/P having no data no longer collapses every book onto the same letter.
**Verify anything scoring-related against a labels-free DB**, not just `make smoke`:
`readlist seed && sqlite3 db "DELETE FROM labels" && readlist score`.

Every `(work, dim)` pair carries one of three states — this is the core invariant of
the whole scoring system, not just a data quality footnote:

| state | meaning | in ranking? |
|---|---|---|
| `measured` | real evidence | yes, discriminating |
| `shrunk` | no evidence, Bayesian-shrunk to corpus prior | yes, zero discriminating power |
| `unknown` | even shrinking is unjustified (polluted pubdate, unknown author) | **no** — dimension is renormalized out of that book's weights entirely |

A preset's `needs:` map is a **per-book hard gate** ("this dim must be at least
`measured` for this list"), independent of the preset's `weights:` (a dim can be
required via `needs` without carrying any weight — see `fresh-releases` in
`presets.yaml`). The single-letter A/B/C/D grade (`score.Grade`) is a **UI badge
only**, graded over `GradedDims(presets)` — the dims carrying non-zero weight in some
preset, never all seven. **It must never gate admission.** That was a real bug: it
silently excluded 23% of the library (`docs/archive/review-2026-08-05.md`). If you
touch admission logic, keep it in `needs` + `min_coverage`, not the grade.

### Work identity, not book identity

Scoring and lists key on **`work_id`** (clustered by OpenLibrary work id > ISBN-13
family > normalized title+author), not Calibre's `book_id`. This exists because
Calibre `book_id`s drift (deletions/re-imports) and because rating counts must be
summed at the work level before Bayesian shrinkage — splitting one book's 3,000
ratings across 3 editions and shrinking each separately systematically
under-scores multi-edition books.

### Runs are immutable, publish is atomic

Every snapshot/ingest/score writes to a new `run_id`; `published_run` is a
single-row pointer swapped atomically. `(corpus_id, standard_version, facts_hash)`
together determine reproducibility. `KeepRuns` (config, default 5) controls how many
old runs survive garbage collection at publish time — don't lower it without
checking the rollback story still holds.

### `pubdate` provenance is load-bearing, don't relax it

477 books in the real library have `pubdate` silently populated from file mtime
during a 2026-07 metadata backfill, not actual publication date. `corpus.Import`
tags every pubdate with `pubdate_source` (`calibre` / `mtime-fallback` / `unknown` /
`google` / `openlibrary`); **only externally-sourced pubdates count as
`measured` for the freshness dimension** — none of the snapshot-only sources are
trusted. Don't add a new pubdate source to the trusted set without external
corroboration; this is the mechanism that keeps the freshness dimension honest.

### Public surface = union of public lists, no full-catalog endpoint

`GET /api/v1/catalog`, `/lists`, `/works/{id}` all resolve from the **same
snapshot**, defined as the union of `visibility: public` lists in the current run
(`internal/api/server.go`, `docs/spec/system-design.md` §10). There is intentionally no
"all books" endpoint — requesting an unlisted `work_id` 404s. If you add an endpoint
that reads from `works`/`editions` directly instead of the published-lists
snapshot, you are reopening a full-library enumeration leak that was already found
and closed once. Total library size is only ever exposed via `/metrics`
(`readlist_works_total`), which is an ops signal, not content.

### API server snapshot cache

`internal/api/server.go` caches the published run's content in-process
(`Server.cached`) because the SQLite connection pool is capped at 1
(`store.Open` — single-writer safety for concurrent cron jobs). Every content
handler must go through `loadSnapshot`, which also handles ETag / `If-None-Match`
304s (`writeRunCache`) — a cache miss under crawler load can starve `/healthz` past
the liveness probe timeout and get the single replica killed. If you add a new
content endpoint, reuse this path rather than querying tables directly.

## Deployment shape (context for infra-adjacent changes)

Single Go binary = cron ingestion worker + scoring worker + read-only API + embedded
SPA + SQLite/WAL, deployed as a single `Recreate` replica (SQLite single-writer
lock — never make this a rolling update or multi-replica). CGO is disabled
(`modernc.org/sqlite`, pure Go) specifically so cross-compiling to `linux/arm64`
needs no QEMU — the target cluster is ARM-only and its Kyverno policy rejects
`latest`-tagged images (see `.github/workflows/image.yml`).

**This repo owns app source, scoring spec, and image CI. The
[homelab](https://github.com/meirongdev/homelab) repo is the sole source of truth
for deployed manifests** (`cloud/oracle/manifests/`) — `deploy/` in this repo is
kind-only / reference, not what's actually running. Don't treat `deploy/oracle/` as
deployable on its own.
