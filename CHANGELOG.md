# Changelog

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
