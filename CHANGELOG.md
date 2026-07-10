# Changelog

## v3.1.0 - 2026-07-10 - Secure Durable Storage

### Breaking upgrade requirements

- `DATA_ENCRYPTION_KEY` is now mandatory. It must be the canonical standard-Base64 encoding of exactly 32 random bytes; startup fails when it is missing, malformed, or cannot decrypt the existing database.
- Before the first upgrade from v3.0.x, stop accepting work and let every old in-memory queued/running job finish. Those jobs were never stored on disk and cannot be migrated.
- Public deployments must terminate TLS at a trusted reverse proxy and set `PUBLIC_BASE_URL` to the external `https://` URL.
- Keep the encryption key outside the database backup directory and back it up separately. Losing it makes encrypted credentials, sessions, and job details unrecoverable.

### SQLite storage and encryption

- SQLite is now the only active server persistence source for administrators, users, sessions, PikPak accounts, CDKs, settings, jobs, cleanup state, migrations, and backup status.
- PikPak passwords and sessions, application secrets, and complete job payloads use AES-256-GCM field encryption with purpose/record-bound authenticated data.
- Admin and user cookies are represented in SQLite only by SHA-256 token digests; raw session tokens are not persisted.
- Key rotation is supported with comma-separated `DATA_ENCRYPTION_PREVIOUS_KEYS`: configure the new current key together with every old key still needed to read retained records or backups.
- SQLite disk connections now enforce WAL mode, foreign keys, a busy timeout, full synchronous durability, and immediate write transactions.

### Migration and durable jobs

- The first v3.1.0 start snapshots the legacy database, `auth.json`, account JSON, and session files into a checksummed migration backup before importing them into encrypted SQLite.
- The plaintext migration backup remains pending until an administrator verifies the new database and explicitly confirms deletion in Settings.
- Parent and child resolve jobs persist across process restarts. Nonterminal jobs are never auto-resumed; startup records them as `failed/service_restart`.
- Complete job details remain available for 3 hours, then sensitive payloads are scrubbed while audit metadata remains for up to 30 days.
- The user portal remembers the current job ID in `sessionStorage` and shows explicit restart-interrupted and expired-detail states without exposing backend errors.

### Verified backups and recovery

- A verified SQLite snapshot runs every 24 hours by default, with seven successful backups retained. Each snapshot is integrity-checked, hashed, synced, and atomically published.
- Administrators can inspect backup status, trigger a snapshot, and confirm deletion of the legacy migration backup from Settings.
- Added offline recovery commands:
  - `storage restore-db --backup <file> --yes`
  - `storage restore-migration --backup <dir> --yes`
- Restore commands require the service to be stopped and save a safety copy of current data before replacement.
- Restoring either snapshot is point-in-time recovery: all users, quota usage, tasks, account state, and configuration changes made after that snapshot are lost.

## 2026-06-17 - Multi-Link Parallel Resolution

**Resolve many links in one submission**
- The resolve box now accepts multiple links separated by newlines; each line is detected independently (magnet **or** PikPak share link) and resolved on its own
- Each link becomes its own job enqueued in the global resolve queue, so the lines fan out in parallel under the admin's concurrency limit (`maxResolveConcurrency`)
- A multi-link submission creates a parent batch job that is not itself queued; it merges its children's results once they finish. Single-link submissions keep the existing flow (multi-file links still pause for a manual file selection)
- Each link's files auto-resolve to download links (no per-file selection); a share with multiple roots restores them all
- Partial failures are tolerated: only successful links are returned, and the UI shows **解析成功 x/x 条**
- Results render as a folder tree where **each link is a sibling top-level folder**, on both the admin page and the CDK user portal
- Proxy links for batch results point at the parent job and carry the resolving account so they keep working across accounts and after a child job is evicted
- Available on both the admin resolve page and the CDK user portal

## 2026-06-17 - Monthly Traffic Limits (accounts) + Traffic-based CDKs

**Per-account monthly downstream traffic budget**
- Each PikPak account now has a monthly downstream-traffic budget (default 700G; 1G = 1024³ bytes)
- On a successful direct-link return (admin and CDK users alike), the resource size is counted against the resolving account's budget
- An account that reaches its budget is marked **到达限行流量** and is excluded from new resolves; the counter (and the state) auto-reset at the start of each calendar month — no scheduler needed
- Admins can set the budget when adding an account, and change it later per account on the account page (`PATCH /api/accounts/{id}`)
- Available-account count excludes capped accounts

**CDK metering switched from count to traffic**
- CDKs now carry a downstream-traffic allowance (GB) instead of a parse count; traffic is charged on successful resolve (a small overage is possible if parallel jobs race, then the CDK is blocked)
- Admin CDK create/edit and the user portal now show traffic (GB) instead of counts
- **One-time DB migration**: existing CDKs are converted at `1 credit = 2G`. The `cdks` table is rebuilt automatically on first start after upgrade. **Back up `data/pikpak.db` before upgrading** if you want a rollback point.

## 2026-06-17 - CDK User Error Disclosure Fix

**Stop leaking PikPak account info to CDK users**
- A failed parse previously surfaced the raw "all PikPak accounts failed: `<email>`: ..." error (and the "starting with `<email>`" progress message) straight to CDK end users, exposing platform account usernames
- `toUserJobView` now collapses any failure into a single generic message and reconstructs the progress message from the job's status/stage only — the raw `Error`/`Message` are never forwarded to CDK users
- Admin views are unchanged and still show full error detail for debugging

## 2026-06-16 - Parallel Resolution

**Admin-Controllable Parallel Resolution**
- Link resolution can now run multiple jobs in parallel instead of strictly serial
- New Settings panel control for the parallel concurrency (1 = serial, up to 32)
- The value is persisted in the database and restored on restart; `RESOLVE_CONCURRENCY` only seeds the initial default
- New admin endpoints: `GET /api/settings` and `PUT /api/settings`
- In parallel mode, resolution round-robins across available PikPak accounts (via a rotating cursor) instead of always starting on the first account; failed accounts are tried last
- Per-job timeout switches automatically with the mode: `QUEUE_TIMEOUT` (45s) while serial, `PARALLEL_QUEUE_TIMEOUT` (2m) while parallel, so parallel jobs that share PikPak's attention get a larger budget
- Per-job data isolation (results, CDK refunds, temp folders) is preserved under concurrency

## 2026-06-13 - Security & UX Improvements

### 🔒 Security Enhancements

**Optional Access Password Protection**
- Added `ACCESS_PASSWORD` environment variable for optional authentication
- When set, UI and API endpoints require session-based login
- Cookie-based HttpOnly sessions with 30-day expiration
- New endpoints: `POST /api/auth/login` and `POST /api/auth/logout`
- `/api/config` reports `auth_required` and `authenticated` status
- Proxy links remain accessible without login (for external downloaders)
- Startup warning logged when `ACCESS_PASSWORD` is not set
- Removed confusing unused `POST /api/login` alias

**Per-Job Proxy Tokens**
- Each proxy link now includes a unique random token (`?token=...`)
- Proxy handler validates token before serving files
- Closes the open-proxy vulnerability while keeping downloads accessible

**Memory Leak Prevention**
- Job store now bounded to 200 entries (configurable)
- Oldest jobs automatically evicted when limit reached
- Prevents unbounded memory growth from never-cleaned job history

### 🎨 Frontend Improvements

**Dark Mode Support**
- Full dark theme with optimized color palette
- Theme toggle button in sidebar (auto/light/dark)
- Persisted to localStorage
- Honors `prefers-color-scheme` in auto mode
- All components adapted with CSS variables

**Authentication UI**
- Login overlay shown when authentication is required
- Logout button in sidebar when auth is enabled
- Session state tracked and displayed
- Auth errors handled gracefully

**Link Detection**
- Real-time link type detection as user types
- Badge shows "磁力链接 / PikPak 分享链接 / 无法识别"
- Visual feedback (green for valid, red for invalid)
- Improves input validation UX

**Toast Notifications**
- Non-intrusive toast messages for actions
- Success/error/info variants with color coding
- Auto-dismiss after 3 seconds
- Replaces modal alerts for copy feedback

**Delete Confirmation**
- Account deletion now requires confirmation dialog
- Prevents accidental data loss
- Shows account username in confirmation prompt

**Layout Polish**
- Metric bar hidden on Logs page (more console space)
- Theme toggle and logout controls in sidebar
- Improved spacing and visual hierarchy
- Better mobile responsiveness

### 📝 Documentation

**README Updates**
- Documented new `ACCESS_PASSWORD` configuration
- Added security recommendations section
- Updated usage instructions with new features
- Systemd service example includes ACCESS_PASSWORD
- Explained proxy token protection

**.env.example Updates**
- Added `ACCESS_PASSWORD` with explanatory comment
- Clarified proxy link behavior with tokens

### 🔧 Technical Changes

**Backend (Go)**
- `internal/app/auth.go`: New auth session store with token generation
- `internal/app/config.go`: Added `AccessPassword` field and `AuthEnabled()` method
- `internal/app/server.go`: Auth middleware, login/logout handlers, token validation
- `internal/app/jobs.go`: Bounded job store with capacity management, proxy tokens
- `cmd/server/main.go`: Security warning when auth is disabled

**Frontend (JavaScript)**
- Theme management with localStorage persistence
- Auth state tracking and overlay management
- Real-time link detection with regex validation
- Toast notification system
- Delete confirmation dialogs
- Enhanced error handling

**Frontend (HTML/CSS)**
- Auth overlay modal structure
- Theme toggle and logout buttons
- Link type indicator badge
- Toast container and animations
- CSS variables for theme support
- Dark mode color palette

### ✅ Testing

- All Go tests pass
- Build successful on Windows/Go 1.26.2
- No breaking changes to existing deployments
- Backward compatible (auth is opt-in)
