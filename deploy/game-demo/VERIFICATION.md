# GameShelf verification record · 2026-11-13

Environment: Windows with Docker Desktop and Linux containers. The demo used isolated synthetic data and dedicated service volumes.

## Passed

| Check | Evidence / result |
| --- | --- |
| Independent PostgreSQL seed and oracle | `demo.ps1 -Action verify -LocalBuild`: raw-order SQL asserts sales 12000, cost 7200, margin 4800; individual shops 6000/3000 and 6000/4200; missing date NULL, covered zero, three ambiguous title matches. |
| Genuine game source + existing analysis engine | Real PostgreSQL integration test passes main margin case (50% / 30%, combined 40%), ambiguity resolution, parameter binding, unknown/foreign scope and mismatched SKU rejection, missing versus zero, and database SQLSTATE 25006 on attempted write. No AssistantFixtureSource used in this path. |
| Source identity regression | `TestGameDemoConnectionChangeFailsBeforeFixtureFallback`: observed failure before fix, then pass. A connection edited after admission fails the original source fingerprint check. |
| Existing assistant regressions | Linux test binary ran `TestAssistant|TestGameDemo`: **40 top-level passes, 3 explicit skips** in that run. Subtests are not added to this count. Integration cases requiring unrelated test infrastructure were skipped, not presented as passed. |
| Frontend | **56 tests in 7 files passed**, covering assistant forms/cards, demo defaults, stale product identity prevention, neutral branding and existing Chinese copy/error formatting. New demo CSS and final client build succeeded. |
| Prompt identity | `go test ./internal/constants -count=1` passes. Development demo system/recommendation/visualization prompts use GameShelf while preserving SQL rules and one language directive; production defaults are unchanged. This is a deterministic prompt test, not a model call. |
| Isolated application runtime | Six GameShelf services started on project-scoped network/volumes; loopback ports 5300, 55173, 55432. Mongo/Redis/Qdrant have no host port exposure. The earlier task-created validation stack is stopped; unrelated services were not stopped. |
| Authenticated API chain | Login → new chat → connection → actual PostgreSQL source → stored report passes; `contract_version=game-shop-demo-v1`, sales **12000**, margin **4800**. Report JSON in `.agent-runs/game-demo/api-report.json`; tokens are not saved there. |
| Browser / visible branding | Fresh headless Chrome login → existing chat → actual stored database report → knowledge editor → click product workflow and create a **new** real-database report in the UI passes. Title, login, sidebar, report and knowledge editor have no original product branding in visible text. No page JavaScript errors. Four screenshots saved and visually inspected; numbers and synthetic-data notice are legible. |
| Attribution | Neutral favicon/banner and GameShelf copy in the demo build; original product artwork is excluded. Login links to the preserved NeoBase/upstream NOTICE and LICENSE. |

Reproduction commands and the four-person 10-minute script are in [README.md](README.md). Game identity/physical-edition sources are in [SOURCES.md](SOURCES.md).

Screenshots: [login](screenshots/01-login.png), [report](screenshots/02-report-page.png), [metrics table](screenshots/03-metrics-table.png), [knowledge editor](screenshots/04-knowledge.png).

## Failures encountered and disposition

- Native Windows backend tests fail because the existing `pkg/sourcegate` implementation has Linux build constraints. No lock/security code was weakened; Linux cross-compiled tests and Linux application containers were used successfully.
- Standard Docker source builds stalled downloading npm/Go dependencies; only this task's build/start processes were stopped. `prepare-local.ps1` + `compose.local.yml` successfully use locally compiled code and cached official runtime images. The standard full-download build is not claimed verified.
- Initial Redis configuration and custom embedding collection names prevented startup without a provider. Explicit matching demo Redis password and default collection names on physically isolated Qdrant resolved this; no provider key was invented or reused.
- A verification runner initially recreated its database dependency, briefly causing DNS failure during a concurrent API test. The runner now uses `--no-deps`; the subsequent standalone API test and workflow verification pass.
- Early screenshots were captured during connection/scroll transitions. Readiness and scroll positioning were corrected; replaced screenshots were visually inspected.
- Fresh review identified Redis password fallback, stale natural-mode product ID, and changed-connection fixture routing. All three findings were fixed; the latter two have observed RED→GREEN regressions. No reviewer finding remains deferred.

## Not run / remaining limits

- **Real NL→SQL, model-generated explanations and embedding/RAG end-to-end calls are not run**: no new model API/embedding credentials were supplied or configured. No paid model call is claimed. The knowledge editor correctly shows the index is not ready; no production knowledge/vector data was copied.
- Dedicated workflow natural input is product search, while its dates/scope are form parameters. Do not present it as an arbitrary natural-language planner.
- The dedicated source supports payment quantity, sales, acquisition cost, margin, margin rate and average price only. Refunds, outstock, currency conversion, demand prediction and causal attribution are outside this demo contract.
- Dedicated workflow coverage is conservative per shop/window. Use the daily view or a single-day report to show the exact missing date; do not claim full daily coverage recovery for a shop with a missing import.
- Human usability testing, sample size, success rate and commercial impact are not measured or fabricated. README provides an evaluation procedure for the group.
- The transactional reset script is delivered; initial seed and oracle are verified. A full stop/reset/restart exercise has not been run as a separate acceptance case.

## Schema RAG update · 2026-11-13

- Rebuilt and replaced only the backend container. API health and frontend HTTP checks passed; PostgreSQL, MongoDB, Redis and Qdrant containers were preserved.
- Authenticated demo report matched the independent expected totals: sales 12000 and margin 4800.
- Refreshed the new smoke chat after selecting tables and views. The active generation contained 13 points with 1024-dimensional embeddings; Qdrant reported BM25 scoring on both text indexes, and BM25 + RRF found the selected reporting.v_game_daily view.
- Embedding settings are supplied by a Git-ignored runtime file. The demo uses its own databases and Qdrant collections. No chat generation model is configured by this update.
