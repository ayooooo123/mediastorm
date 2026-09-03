# Repository Guidelines

## Project Structure & Module Organization
- `backend/`: Go server (`main.go`) with HTTP handlers in `handlers/`, business logic in `services/`, shared/domain models in `models/`, and core packages in `internal/`.
- `frontend/`: Expo React Native app. Routes in `app/`, UI in `components/`, reusable logic in `hooks/` and `services/`, native modules in `modules/`, automation scripts in `scripts/`.
- `docs/`: local planning and operational notes. Vikunja at `https://todo.godver3.xyz` is the canonical TODO; use the tracked `scripts/vikunja-task.py` helper for task workflow operations.
- `.github/workflows/`: Android artifact builds and backend Docker publish pipeline.

## Repository Boundaries
- Root (`/Users/liamhughes/strmr`) is the backend/docs/ops repo.
- `frontend/` is a separate Git repository with its own `.git` directory.
- `ksplayer/` is a gitignored symlink to a separate local clone at `~/ksplayer/` for iOS/tvOS native player work.
- Run frontend git commands from `frontend/` (or with `git -C frontend ...`), not from root.
- The root repo ignores `frontend/`, so frontend file changes will not stage from the root repo.
- Handle backend and frontend git operations separately. Root repo diffs/status/logs do not include frontend changes, and frontend repo commands do not include root changes.

## Build, Test, and Development Commands
- Backend (`cd backend`): `make run`, `make test`, `make check`, `make build`.
- Frontend (`cd frontend`): `npm ci`, `npm run start`, `npm run start:tv`, `npm run test`, `npm run lint`.
- Full stack local workflow (repo root): `./dev.sh start|stop|restart [backend|frontend]`.
- Debug logs: `.logs/backend.log` and `.logs/frontend.log` (example: `tail -f .logs/backend.log`).
- After backend Go changes, agents can restart the backend when needed or when asked using `~/strmr/dev.sh restart backend` from the repo root. If the script reports port `7777` is still held by an old backend PID, inspect with `lsof -nP -iTCP:7777 -sTCP:LISTEN`, terminate the stuck backend process when appropriate, then rerun the restart. The restart may require escalation because it accesses Docker/PostgreSQL; rerun with approval if Docker socket access is denied. Verify with `curl -L --max-time 5 -s -o /dev/null -w '%{http_code}' http://127.0.0.1:7777/health` and expect `200`. Frontend JS/TS changes usually apply via hot reload.
- Local `go build` in `backend/` creates a `mediastorm` binary artifact that should not be kept.

## Coding Style & Naming Conventions
- Go: format with `go fmt ./...`; keep handlers thin and move logic into `services`.
- TypeScript/React Native: Prettier + ESLint are authoritative (`tabWidth: 2`, single quotes, semicolons, trailing commas).
- Naming: React components `PascalCase.tsx`, hooks `useFeature.ts`, tests as `*_test.go` or `*.test.ts(x)`.
- Prefer small, focused files; split oversized files proactively.

## Testing Guidelines
- Backend changes should include tests in the same package area (for example `handlers/*_test.go`, `services/*/service_test.go`, `internal/*_test.go`).
- Run `cd backend && go test ./...` before opening a PR.
- Run package-scoped backend tests when iterating quickly, for example `cd backend && go test ./handlers/... -v`.
- Frontend uses `jest-expo`; run `cd frontend && npm run test`.
- Validate TV-focused UX changes (focus order, remote navigation, playback transitions).
- For Go tests, keep tests in the same directory as the code under test and use `_test.go` suffixes.

## Commit & Pull Request Guidelines
- Use Conventional Commit style where possible (seen in history): `feat(frontend): ...`, `fix(backend): ...`, `style(frontend): ...`.
- PRs should include summary, rationale, related issue, and test evidence.
- Include screenshots or GIFs for UI changes, especially TV/mobile flows.

## Security, Config, and Operations
- Do not expose backend directly to the public internet; use private networking/VPN.
- Never commit secrets (API keys, tokens, credentials).
- Docker Hub image is `godver3/mediastorm`; build backend image from repo root with `-f backend/Dockerfile`.
- Vikunja project `MediaStorm` (identifier `MEDIASTORM`) is the sole writable source of truth for TODO work. Do not update `docs/TODO.md` or `docs/todo-data/TODO.md`; the latter is retained only as the legacy migration source/backup.
- TODO automation authenticates to `http://127.0.0.1:8082/api/v2` (or `https://todo.godver3.xyz/api/v2`) with the scoped `VIKUNJA_API_TOKEN` stored in `docs/.env`. `docs/.env`, `docs/todo-data/TODO.md`, and all of `docs/` are intentionally local and gitignored. Never print, stage, commit, or force-add their secrets/data.
- While public registration is enabled, the local `com.liamhughes.vikunja-auto-enroll` LaunchAgent automatically adds each active local account to `mediastorm developers`; that team has Read & Write access to `MediaStorm`. Boot out the worker before allowing registrations that should not receive project access.
- Use `python3 scripts/vikunja-task.py` for normal Vikunja work. It loads `docs/.env` without printing secrets, discovers the `MediaStorm` project and Kanban IDs dynamically, handles API v2 pagination, and combines each lifecycle transition into one command. Use raw API calls only when the helper cannot perform the required operation.
- Useful commands are `status TASK`, `create TITLE --state "In Progress" --comment TEXT`, `claim TASK --state "In Progress" --comment TEXT`, `testing TASK --comment TEST_STEPS`, `in-progress TASK --comment REASON`, `comment TASK --comment TEXT`, and `done TASK --verification USER_CONFIRMATION --commit HASH`. Run `--help` for all options.
- The helper verifies assignment by re-reading the task. A successful `PUT /projects/{project}/views/{view}/buckets/{bucket}/tasks` response is authoritative for a workflow move; do not add follow-up bucket/task-list queries because Vikunja may report `bucket_id: 0` or omit embedded bucket tasks.
- Agents must use these Kanban buckets as workflow state: `Backlog` for unclaimed planned work, `Claimed` after an owner reserves it, `In Progress` while implementation is active, `Testing` when implementation is ready for human/device verification, and `Done` only after the user confirms the result.
- Before starting work, use `create` or `claim` to confirm ownership and enter `Claimed` or `In Progress`. Add comments only when claiming/starting, handing off to Testing, reopening after failed verification, recording a material blocker, or closing. Move completed implementation to `Testing`; use `done` only after user confirmation.
- Labels classify the kind or area of work; they do not represent workflow. Current general labels are `bug`, `feature`, and `someday`; add focused area labels such as `frontend`, `backend`, `android-tv`, `ios-tvos`, or `needs-logs` only when useful. Never create labels such as `testing`, `claimed`, `in-progress`, or `done` because those are buckets.
- Use stable Vikunja task IDs/identifiers for updates and comments. Do not delete tasks as part of normal completion; move them through the workflow so ownership and history remain visible.
- If raw Vikunja access is necessary, API v2 paginated responses use an `items` array, task comments use `POST /tasks/{task}/comments`, assignees use `POST /tasks/{task}/assignees` with `{"user_id": <id>}`, and bucket moves use `PUT /projects/{project}/views/{view}/buckets/{bucket}/tasks` with `{"task_id": <id>}`. Local requests may require sandbox escalation even for localhost.
- Application settings, including API keys for local debugging, are stored in `backend/cache/settings.json`. `/api/settings` masks sensitive values.
- PostgreSQL is the sole datastore. Connection is via `DATABASE_URL`, and migrations live in `backend/internal/datastore/migrations/`.

## Debugging & Monitoring
- Check `.logs/backend.log` and `.logs/frontend.log` first when diagnosing issues.
- `monitor.sh` at repo root captures system metrics and backend runtime diagnostics into `.monitoring/` for instability or performance investigations.
- Local debug endpoints are available on the backend under `/api/debug/runtime` and `/api/debug/pprof/` for runtime stats, heap, and goroutine inspection.
- For Android TV ADB testing, use only the Fire TV at `100.74.1.49:5555` or a local Android TV emulator. Never connect to or issue ADB commands against the Google TV Streamer at `100.101.116.57:5555`.

## KSPlayer Notes
- The iOS/tvOS native player depends on the fork `godver3/KSPlayer` on branch `strmr-fixes`, cloned locally at `~/ksplayer/`.
- Make KSPlayer code changes in `~/ksplayer/`, not in this repo root.
- After KSPlayer changes, refresh native iOS dependencies with `cd frontend && npx expo prebuild --platform ios --clean`.
- The Expo config plugin wiring KSPlayer is `frontend/plugins/with-ksplayer.js`.
