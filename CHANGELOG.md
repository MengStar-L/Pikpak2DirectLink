# Changelog

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
