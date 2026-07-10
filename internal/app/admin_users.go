package app

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	defaultAdminUserPageSize = 50
	maxAdminUserPageSize     = 100

	adminSubscriptionActive     = "active"
	adminSubscriptionExhausted  = "exhausted"
	adminSubscriptionExpired    = "expired"
	adminSubscriptionTerminated = "terminated"
)

var (
	errAdminSubscriptionNotFound   = errors.New("subscription not found")
	errAdminSubscriptionConflict   = errors.New("subscription revision conflict")
	errAdminSubscriptionTerminated = errors.New("subscription has been terminated")
)

type AdminSubscription struct {
	ID             string `json:"id"`
	Source         string `json:"source"`
	SourceCDKCode  string `json:"source_cdk_code,omitempty"`
	RemainingBytes int64  `json:"remaining_bytes"`
	RemainingLabel string `json:"remaining_label"`
	UsedBytes      int64  `json:"used_bytes"`
	UsedLabel      string `json:"used_label"`
	ReservedBytes  int64  `json:"reserved_bytes"`
	ReservedLabel  string `json:"reserved_label"`
	ExpiresAt      string `json:"expires_at"`
	CreatedAt      string `json:"created_at"`
	DaysLeft       int    `json:"days_left"`
	Expired        bool   `json:"expired"`
	Status         string `json:"status"`
	AllowProxy     bool   `json:"allow_proxy"`
	Revision       int64  `json:"revision"`
	TerminatedAt   string `json:"terminated_at,omitempty"`
}

type adminUserView struct {
	ID          string `json:"id"`
	Email       string `json:"email,omitempty"`
	DisplayName string `json:"display_name"`
	AvatarURL   string `json:"avatar_url,omitempty"`
	Disabled    bool   `json:"disabled"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

type adminUserSummary struct {
	User                    adminUserView `json:"user"`
	AuthProviders           []string      `json:"auth_providers"`
	Quota                   UserQuota     `json:"quota"`
	SubscriptionCount       int           `json:"subscription_count"`
	ActiveSubscriptionCount int           `json:"active_subscription_count"`
}

type adminUserDetail struct {
	User                    adminUserView       `json:"user"`
	AuthProviders           []string            `json:"auth_providers"`
	Quota                   UserQuota           `json:"quota"`
	SubscriptionCount       int                 `json:"subscription_count"`
	ActiveSubscriptionCount int                 `json:"active_subscription_count"`
	Subscriptions           []AdminSubscription `json:"subscriptions"`
}

type adminUserListResponse struct {
	Users  []adminUserSummary `json:"users"`
	Total  int                `json:"total"`
	Limit  int                `json:"limit"`
	Offset int                `json:"offset"`
}

type adminCreateSubscriptionRequest struct {
	RemainingBytes *int64  `json:"remaining_bytes"`
	ExpiresAt      *string `json:"expires_at"`
	AllowProxy     *bool   `json:"allow_proxy"`
}

type adminUpdateSubscriptionRequest struct {
	ExpectedRevision int64   `json:"expected_revision"`
	RemainingBytes   *int64  `json:"remaining_bytes,omitempty"`
	ExpiresAt        *string `json:"expires_at,omitempty"`
	AllowProxy       *bool   `json:"allow_proxy,omitempty"`
}

type adminTerminateSubscriptionRequest struct {
	ExpectedRevision int64 `json:"expected_revision"`
}

type adminSubscriptionRow struct {
	id              string
	userID          string
	sourceCDKCode   string
	remainingBytes  int64
	usedBytes       int64
	expiresAt       int64
	createdAt       int64
	allowProxy      bool
	quotaGeneration int64
	revision        int64
	terminatedAt    sql.NullInt64
	reservedBytes   int64
}

func (s *Server) handleAdminListUsers(w http.ResponseWriter, r *http.Request) {
	limit, offset, err := parseAdminUserPagination(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	page, err := s.users.listAdminUsers(strings.TrimSpace(r.URL.Query().Get("q")), limit, offset, s.now())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list users")
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func (s *Server) handleAdminGetUser(w http.ResponseWriter, r *http.Request) {
	detail, ok, err := s.users.adminUserDetail(r.PathValue("userID"), s.now())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to read user")
		return
	}
	if !ok {
		writeError(w, http.StatusNotFound, "user not found")
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

func (s *Server) handleAdminCreateSubscription(w http.ResponseWriter, r *http.Request) {
	var req adminCreateSubscriptionRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	now := s.now()
	expiresAt, err := validateAdminCreateSubscription(req, now)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	sub, err := s.users.createAdminSubscription(r.PathValue("userID"), *req.RemainingBytes, expiresAt, *req.AllowProxy, now)
	if err != nil {
		writeAdminSubscriptionError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, sub)
}

func (s *Server) handleAdminUpdateSubscription(w http.ResponseWriter, r *http.Request) {
	var req adminUpdateSubscriptionRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	now := s.now()
	if err := validateAdminUpdateSubscription(req, now); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	var expiresAt *time.Time
	if req.ExpiresAt != nil {
		parsed, _ := parseFutureRFC3339(*req.ExpiresAt, now)
		expiresAt = &parsed
	}
	sub, err := s.users.updateAdminSubscription(
		r.PathValue("userID"),
		r.PathValue("subscriptionID"),
		req.ExpectedRevision,
		req.RemainingBytes,
		expiresAt,
		req.AllowProxy,
		now,
	)
	if err != nil {
		writeAdminSubscriptionError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, sub)
}

func (s *Server) handleAdminTerminateSubscription(w http.ResponseWriter, r *http.Request) {
	var req adminTerminateSubscriptionRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.ExpectedRevision < 1 {
		writeError(w, http.StatusBadRequest, "expected_revision must be at least 1")
		return
	}
	sub, err := s.users.terminateAdminSubscription(
		r.PathValue("userID"),
		r.PathValue("subscriptionID"),
		req.ExpectedRevision,
		s.now(),
	)
	if err != nil {
		writeAdminSubscriptionError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, sub)
}

func parseAdminUserPagination(r *http.Request) (int, int, error) {
	limit := defaultAdminUserPageSize
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > maxAdminUserPageSize {
			return 0, 0, fmt.Errorf("limit must be between 1 and %d", maxAdminUserPageSize)
		}
		limit = parsed
	}
	offset := 0
	if raw := strings.TrimSpace(r.URL.Query().Get("offset")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 0 {
			return 0, 0, errors.New("offset must be a non-negative integer")
		}
		offset = parsed
	}
	return limit, offset, nil
}

func validateAdminCreateSubscription(req adminCreateSubscriptionRequest, now time.Time) (time.Time, error) {
	if req.RemainingBytes == nil || req.ExpiresAt == nil || req.AllowProxy == nil {
		return time.Time{}, errors.New("remaining_bytes, expires_at, and allow_proxy are required")
	}
	if *req.RemainingBytes <= 0 {
		return time.Time{}, errors.New("remaining_bytes must be greater than zero")
	}
	return parseFutureRFC3339(*req.ExpiresAt, now)
}

func validateAdminUpdateSubscription(req adminUpdateSubscriptionRequest, now time.Time) error {
	if req.ExpectedRevision < 1 {
		return errors.New("expected_revision must be at least 1")
	}
	if req.RemainingBytes == nil && req.ExpiresAt == nil && req.AllowProxy == nil {
		return errors.New("at least one subscription field must be provided")
	}
	if req.RemainingBytes != nil && *req.RemainingBytes < 0 {
		return errors.New("remaining_bytes must be non-negative")
	}
	if req.ExpiresAt != nil {
		if _, err := parseFutureRFC3339(*req.ExpiresAt, now); err != nil {
			return err
		}
	}
	return nil
}

func parseFutureRFC3339(value string, now time.Time) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(value))
	if err != nil {
		return time.Time{}, errors.New("expires_at must be a valid RFC3339 timestamp")
	}
	if !parsed.After(now) {
		return time.Time{}, errors.New("expires_at must be in the future")
	}
	return parsed.UTC(), nil
}

func writeAdminSubscriptionError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errUserNotFound), errors.Is(err, errAdminSubscriptionNotFound):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, errAdminSubscriptionConflict), errors.Is(err, errAdminSubscriptionTerminated):
		writeError(w, http.StatusConflict, err.Error())
	default:
		writeError(w, http.StatusInternalServerError, "failed to update subscription")
	}
}

func (s *userStore) listAdminUsers(query string, limit, offset int, now time.Time) (adminUserListResponse, error) {
	where, searchArgs := adminUserSearchClause(query)
	var total int
	countArgs := append([]any(nil), searchArgs...)
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM users u `+where, countArgs...).Scan(&total); err != nil {
		return adminUserListResponse{}, err
	}

	args := append([]any(nil), searchArgs...)
	args = append(args, limit, offset, now.Unix(), now.Unix(), now.Unix(), now.Unix())
	rows, err := s.db.Query(
		`WITH paged_users AS (
		    SELECT u.id, COALESCE(u.email, '') AS email, u.display_name, u.avatar_url,
		           u.disabled, u.created_at, u.updated_at
		    FROM users u
		    `+where+`
		    ORDER BY u.created_at DESC, u.id
		    LIMIT ? OFFSET ?
		), identity_groups AS (
		    SELECT user_id, GROUP_CONCAT(provider) AS providers
		    FROM (
		        SELECT DISTINCT i.user_id, i.provider
		        FROM user_identities i
		        INNER JOIN paged_users p ON p.id=i.user_id
		        ORDER BY i.user_id, i.provider
		    )
		    GROUP BY user_id
		), subscription_totals AS (
		    SELECT s.user_id,
		           COUNT(*) AS subscription_count,
		           SUM(CASE WHEN s.terminated_at IS NULL AND s.expires_at>? AND s.remaining_bytes>0 THEN 1 ELSE 0 END) AS active_count,
		           SUM(CASE WHEN s.terminated_at IS NULL AND s.expires_at>? AND s.remaining_bytes>0 THEN s.remaining_bytes ELSE 0 END) AS total_remaining,
		           MIN(CASE WHEN s.terminated_at IS NULL AND s.expires_at>? AND s.remaining_bytes>0 THEN s.expires_at END) AS next_expires_at,
		           MAX(CASE WHEN s.terminated_at IS NULL AND s.expires_at>? AND s.remaining_bytes>0 AND s.allow_proxy=1 THEN 1 ELSE 0 END) AS allow_proxy
		    FROM user_subscriptions s
		    INNER JOIN paged_users p ON p.id=s.user_id
		    GROUP BY s.user_id
		)
		SELECT p.id, p.email, p.display_name, p.avatar_url, p.disabled, p.created_at, p.updated_at,
		       COALESCE(i.providers, ''), COALESCE(st.subscription_count, 0), COALESCE(st.active_count, 0),
		       COALESCE(st.total_remaining, 0), st.next_expires_at, COALESCE(st.allow_proxy, 0)
		FROM paged_users p
		LEFT JOIN identity_groups i ON i.user_id=p.id
		LEFT JOIN subscription_totals st ON st.user_id=p.id
		ORDER BY p.created_at DESC, p.id`,
		args...,
	)
	if err != nil {
		return adminUserListResponse{}, err
	}
	defer rows.Close()

	out := adminUserListResponse{Users: make([]adminUserSummary, 0), Total: total, Limit: limit, Offset: offset}
	for rows.Next() {
		var summary adminUserSummary
		var providers string
		var disabled int
		var createdAt, updatedAt int64
		var nextExpiresAt sql.NullInt64
		var allowProxy int
		if err := rows.Scan(
			&summary.User.ID,
			&summary.User.Email,
			&summary.User.DisplayName,
			&summary.User.AvatarURL,
			&disabled,
			&createdAt,
			&updatedAt,
			&providers,
			&summary.SubscriptionCount,
			&summary.ActiveSubscriptionCount,
			&summary.Quota.TotalRemainingBytes,
			&nextExpiresAt,
			&allowProxy,
		); err != nil {
			return adminUserListResponse{}, err
		}
		summary.User.Disabled = disabled != 0
		summary.User.CreatedAt = formatUnixRFC3339(createdAt)
		summary.User.UpdatedAt = formatUnixRFC3339(updatedAt)
		summary.AuthProviders = splitAuthProviders(providers)
		summary.Quota.TotalRemainingLabel = formatTrafficLabel(summary.Quota.TotalRemainingBytes)
		summary.Quota.AllowProxyAvailable = allowProxy != 0
		if nextExpiresAt.Valid {
			summary.Quota.NextExpiresAt = formatUnixRFC3339(nextExpiresAt.Int64)
		}
		out.Users = append(out.Users, summary)
	}
	if err := rows.Err(); err != nil {
		return adminUserListResponse{}, err
	}
	return out, nil
}

func adminUserSearchClause(query string) (string, []any) {
	query = strings.TrimSpace(query)
	if query == "" {
		return "", nil
	}
	pattern := "%" + escapeSQLLike(query) + "%"
	return `WHERE (
		 u.id LIKE ? ESCAPE '\' COLLATE NOCASE
		 OR COALESCE(u.email, '') LIKE ? ESCAPE '\' COLLATE NOCASE
		 OR u.display_name LIKE ? ESCAPE '\' COLLATE NOCASE
		 OR EXISTS (
		     SELECT 1 FROM user_identities search_identity
		     WHERE search_identity.user_id=u.id
		       AND (
		           COALESCE(search_identity.email, '') LIKE ? ESCAPE '\' COLLATE NOCASE
		           OR search_identity.username LIKE ? ESCAPE '\' COLLATE NOCASE
		           OR search_identity.display_name LIKE ? ESCAPE '\' COLLATE NOCASE
		       )
		 )
	)`, []any{pattern, pattern, pattern, pattern, pattern, pattern}
}

func escapeSQLLike(value string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return replacer.Replace(value)
}

func splitAuthProviders(value string) []string {
	if strings.TrimSpace(value) == "" {
		return []string{}
	}
	providers := strings.Split(value, ",")
	sort.Strings(providers)
	return providers
}

func (s *userStore) adminUserDetail(userID string, now time.Time) (adminUserDetail, bool, error) {
	userID = strings.TrimSpace(userID)
	user, ok, err := s.get(userID)
	if err != nil || !ok {
		return adminUserDetail{}, ok, err
	}
	providers, err := s.adminUserAuthProviders(userID)
	if err != nil {
		return adminUserDetail{}, false, err
	}
	subscriptions, err := s.listAdminSubscriptions(userID, now)
	if err != nil {
		return adminUserDetail{}, false, err
	}
	detail := adminUserDetail{
		User:              makeAdminUserView(user),
		AuthProviders:     providers,
		SubscriptionCount: len(subscriptions),
		Subscriptions:     subscriptions,
	}
	var nextExpires time.Time
	for _, sub := range subscriptions {
		if sub.Status != adminSubscriptionActive {
			continue
		}
		detail.ActiveSubscriptionCount++
		detail.Quota.TotalRemainingBytes += sub.RemainingBytes
		detail.Quota.AllowProxyAvailable = detail.Quota.AllowProxyAvailable || sub.AllowProxy
		expiresAt, err := time.Parse(time.RFC3339, sub.ExpiresAt)
		if err == nil && (nextExpires.IsZero() || expiresAt.Before(nextExpires)) {
			nextExpires = expiresAt
		}
	}
	detail.Quota.TotalRemainingLabel = formatTrafficLabel(detail.Quota.TotalRemainingBytes)
	if !nextExpires.IsZero() {
		detail.Quota.NextExpiresAt = nextExpires.UTC().Format(time.RFC3339)
	}
	return detail, true, nil
}

func makeAdminUserView(user User) adminUserView {
	return adminUserView{
		ID:          user.ID,
		Email:       user.Email,
		DisplayName: user.DisplayName,
		AvatarURL:   user.AvatarURL,
		Disabled:    user.Disabled,
		CreatedAt:   formatUnixRFC3339(user.CreatedAt),
		UpdatedAt:   formatUnixRFC3339(user.UpdatedAt),
	}
}

func (s *userStore) adminUserAuthProviders(userID string) ([]string, error) {
	rows, err := s.db.Query(
		`SELECT DISTINCT provider FROM user_identities WHERE user_id=? ORDER BY provider`,
		strings.TrimSpace(userID),
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	providers := make([]string, 0)
	for rows.Next() {
		var provider string
		if err := rows.Scan(&provider); err != nil {
			return nil, err
		}
		providers = append(providers, provider)
	}
	return providers, rows.Err()
}

func (s *userStore) listAdminSubscriptions(userID string, now time.Time) ([]AdminSubscription, error) {
	rows, err := s.db.Query(adminSubscriptionSelectSQL+`
		WHERE s.user_id=?
		GROUP BY s.id
		ORDER BY s.created_at DESC, s.id`, strings.TrimSpace(userID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	subscriptions := make([]AdminSubscription, 0)
	for rows.Next() {
		row, err := scanAdminSubscriptionRow(rows)
		if err != nil {
			return nil, err
		}
		subscriptions = append(subscriptions, row.view(now))
	}
	return subscriptions, rows.Err()
}

const adminSubscriptionSelectSQL = `SELECT
		s.id, s.user_id, COALESCE(s.source_cdk_code, ''), s.remaining_bytes, s.used_bytes,
		s.expires_at, s.created_at, s.allow_proxy, s.quota_generation, s.revision, s.terminated_at,
		COALESCE(SUM(CASE WHEN r.quota_generation>=0 THEN r.reserved_bytes ELSE 0 END), 0)
	FROM user_subscriptions s
	LEFT JOIN user_quota_reservations r ON r.subscription_id=s.id`

type adminSubscriptionScanner interface {
	Scan(dest ...any) error
}

func scanAdminSubscriptionRow(scanner adminSubscriptionScanner) (adminSubscriptionRow, error) {
	var row adminSubscriptionRow
	var allowProxy int
	if err := scanner.Scan(
		&row.id,
		&row.userID,
		&row.sourceCDKCode,
		&row.remainingBytes,
		&row.usedBytes,
		&row.expiresAt,
		&row.createdAt,
		&allowProxy,
		&row.quotaGeneration,
		&row.revision,
		&row.terminatedAt,
		&row.reservedBytes,
	); err != nil {
		return adminSubscriptionRow{}, err
	}
	row.allowProxy = allowProxy != 0
	return row, nil
}

func (row adminSubscriptionRow) view(now time.Time) AdminSubscription {
	view := AdminSubscription{
		ID:             row.id,
		Source:         "admin",
		SourceCDKCode:  row.sourceCDKCode,
		RemainingBytes: row.remainingBytes,
		RemainingLabel: formatTrafficLabel(row.remainingBytes),
		UsedBytes:      row.usedBytes,
		UsedLabel:      formatTrafficLabel(row.usedBytes),
		ReservedBytes:  row.reservedBytes,
		ReservedLabel:  formatTrafficLabel(row.reservedBytes),
		ExpiresAt:      formatUnixRFC3339(row.expiresAt),
		CreatedAt:      formatUnixRFC3339(row.createdAt),
		Expired:        row.expiresAt <= now.Unix(),
		AllowProxy:     row.allowProxy,
		Revision:       row.revision,
	}
	if row.sourceCDKCode != "" {
		view.Source = "cdk"
	}
	if row.terminatedAt.Valid {
		view.TerminatedAt = formatUnixRFC3339(row.terminatedAt.Int64)
		view.Status = adminSubscriptionTerminated
	} else if view.Expired {
		view.Status = adminSubscriptionExpired
	} else if row.remainingBytes <= 0 {
		view.Status = adminSubscriptionExhausted
	} else {
		view.Status = adminSubscriptionActive
		view.DaysLeft = int((row.expiresAt - now.Unix() + 86399) / 86400)
	}
	return view
}

func formatUnixRFC3339(unix int64) string {
	return time.Unix(unix, 0).UTC().Format(time.RFC3339)
}

func (s *userStore) createAdminSubscription(userID string, remaining int64, expiresAt time.Time, allowProxy bool, now time.Time) (AdminSubscription, error) {
	userID = strings.TrimSpace(userID)
	tx, err := s.db.Begin()
	if err != nil {
		return AdminSubscription{}, err
	}
	defer tx.Rollback()
	var exists int
	if err := tx.QueryRow(`SELECT 1 FROM users WHERE id=?`, userID).Scan(&exists); errors.Is(err, sql.ErrNoRows) {
		return AdminSubscription{}, errUserNotFound
	} else if err != nil {
		return AdminSubscription{}, err
	}
	id := newSubscriptionID()
	if _, err := tx.Exec(
		`INSERT INTO user_subscriptions
		 (id, user_id, source_cdk_code, remaining_bytes, used_bytes, expires_at, created_at,
		  allow_proxy, quota_generation, revision, terminated_at)
		 VALUES(?,?,NULL,?,0,?,?,?,0,1,NULL)`,
		id, userID, remaining, expiresAt.Unix(), now.Unix(), b2i(allowProxy),
	); err != nil {
		return AdminSubscription{}, err
	}
	row, err := loadAdminSubscriptionTx(tx, userID, id)
	if err != nil {
		return AdminSubscription{}, err
	}
	if err := tx.Commit(); err != nil {
		return AdminSubscription{}, err
	}
	return row.view(now), nil
}

func (s *userStore) updateAdminSubscription(
	userID, subscriptionID string,
	expectedRevision int64,
	remaining *int64,
	expiresAt *time.Time,
	allowProxy *bool,
	now time.Time,
) (AdminSubscription, error) {
	userID = strings.TrimSpace(userID)
	subscriptionID = strings.TrimSpace(subscriptionID)
	tx, err := s.db.Begin()
	if err != nil {
		return AdminSubscription{}, err
	}
	defer tx.Rollback()
	current, err := loadAdminSubscriptionTx(tx, userID, subscriptionID)
	if err != nil {
		return AdminSubscription{}, err
	}
	if current.terminatedAt.Valid {
		return AdminSubscription{}, errAdminSubscriptionTerminated
	}
	if current.revision != expectedRevision {
		return AdminSubscription{}, errAdminSubscriptionConflict
	}

	newRemaining := current.remainingBytes
	newExpiresAt := current.expiresAt
	newAllowProxy := current.allowProxy
	newGeneration := current.quotaGeneration
	remainingReset := remaining != nil
	disableProxy := allowProxy != nil && !*allowProxy && current.allowProxy
	if remainingReset {
		newRemaining = *remaining
		newGeneration++
	}
	if expiresAt != nil {
		newExpiresAt = expiresAt.Unix()
	}
	if allowProxy != nil {
		newAllowProxy = *allowProxy
	}
	if disableProxy && !remainingReset {
		var released int64
		if err := tx.QueryRow(
			`SELECT COALESCE(SUM(reserved_bytes), 0)
			 FROM user_quota_reservations
			 WHERE subscription_id=? AND require_proxy=1 AND quota_generation=?`,
			subscriptionID, current.quotaGeneration,
		).Scan(&released); err != nil {
			return AdminSubscription{}, err
		}
		newRemaining += released
	}
	result, err := tx.Exec(
		`UPDATE user_subscriptions
		 SET remaining_bytes=?, expires_at=?, allow_proxy=?, quota_generation=?, revision=revision+1
		 WHERE id=? AND user_id=? AND revision=? AND terminated_at IS NULL`,
		newRemaining, newExpiresAt, b2i(newAllowProxy), newGeneration,
		subscriptionID, userID, expectedRevision,
	)
	if err != nil {
		return AdminSubscription{}, err
	}
	if changed, err := result.RowsAffected(); err != nil {
		return AdminSubscription{}, err
	} else if changed != 1 {
		return AdminSubscription{}, errAdminSubscriptionConflict
	}
	if disableProxy {
		if _, err := tx.Exec(
			`UPDATE user_quota_reservations
			 SET quota_generation=?
			 WHERE subscription_id=? AND require_proxy=1 AND quota_generation>=0`,
			invalidQuotaReservationGeneration, subscriptionID,
		); err != nil {
			return AdminSubscription{}, err
		}
	}
	updated, err := loadAdminSubscriptionTx(tx, userID, subscriptionID)
	if err != nil {
		return AdminSubscription{}, err
	}
	if err := tx.Commit(); err != nil {
		return AdminSubscription{}, err
	}
	return updated.view(now), nil
}

func (s *userStore) terminateAdminSubscription(userID, subscriptionID string, expectedRevision int64, now time.Time) (AdminSubscription, error) {
	userID = strings.TrimSpace(userID)
	subscriptionID = strings.TrimSpace(subscriptionID)
	tx, err := s.db.Begin()
	if err != nil {
		return AdminSubscription{}, err
	}
	defer tx.Rollback()
	current, err := loadAdminSubscriptionTx(tx, userID, subscriptionID)
	if err != nil {
		return AdminSubscription{}, err
	}
	if current.terminatedAt.Valid {
		return AdminSubscription{}, errAdminSubscriptionTerminated
	}
	if current.revision != expectedRevision {
		return AdminSubscription{}, errAdminSubscriptionConflict
	}
	result, err := tx.Exec(
		`UPDATE user_subscriptions
		 SET remaining_bytes=0, expires_at=?, terminated_at=?, quota_generation=quota_generation+1, revision=revision+1
		 WHERE id=? AND user_id=? AND revision=? AND terminated_at IS NULL`,
		now.Unix(), now.Unix(), subscriptionID, userID, expectedRevision,
	)
	if err != nil {
		return AdminSubscription{}, err
	}
	if changed, err := result.RowsAffected(); err != nil {
		return AdminSubscription{}, err
	} else if changed != 1 {
		return AdminSubscription{}, errAdminSubscriptionConflict
	}
	if _, err := tx.Exec(
		`UPDATE user_quota_reservations
		 SET quota_generation=?
		 WHERE subscription_id=? AND quota_generation>=0`,
		invalidQuotaReservationGeneration, subscriptionID,
	); err != nil {
		return AdminSubscription{}, err
	}
	terminated, err := loadAdminSubscriptionTx(tx, userID, subscriptionID)
	if err != nil {
		return AdminSubscription{}, err
	}
	if err := tx.Commit(); err != nil {
		return AdminSubscription{}, err
	}
	return terminated.view(now), nil
}

func loadAdminSubscriptionTx(tx *sql.Tx, userID, subscriptionID string) (adminSubscriptionRow, error) {
	row, err := scanAdminSubscriptionRow(tx.QueryRow(
		adminSubscriptionSelectSQL+`
		WHERE s.id=? AND s.user_id=?
		GROUP BY s.id`,
		subscriptionID, userID,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return adminSubscriptionRow{}, errAdminSubscriptionNotFound
	}
	return row, err
}
