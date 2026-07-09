package app

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	userSessionCookieName = "user_session"
	userSessionMaxAge     = 30 * 24 * time.Hour
)

var (
	errUserNotFound       = errors.New("user not found")
	errUserDisabled       = errors.New("user is disabled")
	errEmailExists        = errors.New("email is already registered")
	errInvalidCredentials = errors.New("invalid email or password")
	errVoucherRedeemed    = errors.New("CDK has already been redeemed")
	errVoucherRevoked     = errors.New("CDK has been revoked")
	errUserQuotaExhausted = errors.New("user quota exhausted")
)

type errUserQuotaOverdraw struct {
	size      int64
	remaining int64
}

func (e errUserQuotaOverdraw) Error() string {
	return fmt.Sprintf("selected size %s exceeds remaining user quota (%s remaining)", formatTrafficLabel(e.size), formatTrafficLabel(e.remaining))
}

type User struct {
	ID          string `json:"id"`
	Email       string `json:"email,omitempty"`
	DisplayName string `json:"display_name"`
	AvatarURL   string `json:"avatar_url,omitempty"`
	Disabled    bool   `json:"disabled"`
	CreatedAt   int64  `json:"created_at"`
	UpdatedAt   int64  `json:"updated_at"`
}

type LinuxDoProfile struct {
	ID          string
	Username    string
	Name        string
	DisplayName string
	Email       string
	AvatarURL   string
}

type UserSubscription struct {
	ID             string `json:"id"`
	UserID         string `json:"-"`
	SourceCDKCode  string `json:"source_cdk_code,omitempty"`
	RemainingBytes int64  `json:"remaining_bytes"`
	UsedBytes      int64  `json:"used_bytes"`
	RemainingLabel string `json:"remaining_label"`
	UsedLabel      string `json:"used_label"`
	ExpiresAt      string `json:"expires_at"`
	CreatedAt      string `json:"created_at"`
	DaysLeft       int    `json:"days_left"`
	Expired        bool   `json:"expired"`
	AllowProxy     bool   `json:"allow_proxy"`
}

type UserQuota struct {
	TotalRemainingBytes int64  `json:"total_remaining_bytes"`
	TotalRemainingLabel string `json:"total_remaining_label"`
	NextExpiresAt       string `json:"next_expires_at,omitempty"`
	AllowProxyAvailable bool   `json:"allow_proxy_available"`
}

type userStore struct {
	db *sql.DB
}

func newUserStore(db *sql.DB) *userStore {
	return &userStore{db: db}
}

func newUserID() string {
	return "usr_" + strings.ReplaceAll(uuid.NewString(), "-", "")
}

func newSubscriptionID() string {
	return "sub_" + strings.ReplaceAll(uuid.NewString(), "-", "")
}

func normalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

func (s *userStore) createEmailUser(email, password string, now time.Time) (User, error) {
	email = normalizeEmail(email)
	if email == "" || !strings.Contains(email, "@") {
		return User{}, errors.New("valid email is required")
	}
	if len(password) < 6 {
		return User{}, errors.New("password must be at least 6 characters")
	}

	rec, err := hashPasswordRecord(password)
	if err != nil {
		return User{}, err
	}
	recJSON, err := json.Marshal(rec)
	if err != nil {
		return User{}, err
	}

	tx, err := s.db.Begin()
	if err != nil {
		return User{}, err
	}
	defer tx.Rollback()

	if _, ok, err := loadUserIdentity(tx, "email", email); err != nil {
		return User{}, err
	} else if ok {
		return User{}, errEmailExists
	}

	user := User{
		ID:          newUserID(),
		Email:       email,
		DisplayName: email,
		CreatedAt:   now.Unix(),
		UpdatedAt:   now.Unix(),
	}
	if _, err := tx.Exec(
		`INSERT INTO users(id, email, display_name, avatar_url, disabled, created_at, updated_at)
		 VALUES(?,?,?,?,?,?,?)`,
		user.ID, user.Email, user.DisplayName, "", 0, user.CreatedAt, user.UpdatedAt,
	); err != nil {
		return User{}, err
	}
	if _, err := tx.Exec(
		`INSERT INTO user_identities(provider, provider_user_id, user_id, email, username, display_name, avatar_url, password_hash, created_at, updated_at)
		 VALUES(?,?,?,?,?,?,?,?,?,?)`,
		"email", email, user.ID, email, email, email, "", string(recJSON), user.CreatedAt, user.UpdatedAt,
	); err != nil {
		return User{}, err
	}
	if err := tx.Commit(); err != nil {
		return User{}, err
	}
	return user, nil
}

func (s *userStore) verifyEmailLogin(email, password string) (User, error) {
	email = normalizeEmail(email)
	identity, ok, err := loadUserIdentity(s.db, "email", email)
	if err != nil {
		return User{}, err
	}
	if !ok || strings.TrimSpace(identity.passwordHash) == "" {
		return User{}, errInvalidCredentials
	}
	var rec credentialRecord
	if err := json.Unmarshal([]byte(identity.passwordHash), &rec); err != nil {
		return User{}, errInvalidCredentials
	}
	if !verifyPasswordRecord(rec, password) {
		return User{}, errInvalidCredentials
	}
	user, ok, err := s.get(identity.userID)
	if err != nil {
		return User{}, err
	}
	if !ok {
		return User{}, errUserNotFound
	}
	if user.Disabled {
		return User{}, errUserDisabled
	}
	return user, nil
}

func (s *userStore) upsertLinuxDoUser(profile LinuxDoProfile, now time.Time) (User, bool, error) {
	providerID := strings.TrimSpace(profile.ID)
	if providerID == "" {
		return User{}, false, errors.New("LinuxDo user id is missing")
	}

	tx, err := s.db.Begin()
	if err != nil {
		return User{}, false, err
	}
	defer tx.Rollback()

	displayName := firstNonEmpty(profile.DisplayName, profile.Name, profile.Username, "LinuxDo User")
	email := normalizeEmail(profile.Email)
	avatar := strings.TrimSpace(profile.AvatarURL)
	nowUnix := now.Unix()

	identity, ok, err := loadUserIdentity(tx, "linuxdo", providerID)
	if err != nil {
		return User{}, false, err
	}
	created := false
	var user User
	if ok {
		if _, err := tx.Exec(
			`UPDATE users SET email=COALESCE(NULLIF(?, ''), email), display_name=?, avatar_url=?, updated_at=? WHERE id=?`,
			email, displayName, avatar, nowUnix, identity.userID,
		); err != nil {
			return User{}, false, err
		}
		if _, err := tx.Exec(
			`UPDATE user_identities SET email=?, username=?, display_name=?, avatar_url=?, updated_at=?
			 WHERE provider='linuxdo' AND provider_user_id=?`,
			email, profile.Username, displayName, avatar, nowUnix, providerID,
		); err != nil {
			return User{}, false, err
		}
		user, ok, err = loadUserByID(tx, identity.userID)
		if err != nil {
			return User{}, false, err
		}
		if !ok {
			return User{}, false, errUserNotFound
		}
	} else {
		created = true
		user = User{
			ID:          newUserID(),
			Email:       email,
			DisplayName: displayName,
			AvatarURL:   avatar,
			CreatedAt:   nowUnix,
			UpdatedAt:   nowUnix,
		}
		if _, err := tx.Exec(
			`INSERT INTO users(id, email, display_name, avatar_url, disabled, created_at, updated_at)
			 VALUES(?,?,?,?,?,?,?)`,
			user.ID, user.Email, user.DisplayName, user.AvatarURL, 0, nowUnix, nowUnix,
		); err != nil {
			return User{}, false, err
		}
		if _, err := tx.Exec(
			`INSERT INTO user_identities(provider, provider_user_id, user_id, email, username, display_name, avatar_url, created_at, updated_at)
			 VALUES(?,?,?,?,?,?,?,?,?)`,
			"linuxdo", providerID, user.ID, email, profile.Username, displayName, avatar, nowUnix, nowUnix,
		); err != nil {
			return User{}, false, err
		}
	}
	if user.Disabled {
		return User{}, false, errUserDisabled
	}
	if err := tx.Commit(); err != nil {
		return User{}, false, err
	}
	return user, created, nil
}

func (s *userStore) linuxDoIdentityExists(providerID string) (bool, error) {
	_, ok, err := loadUserIdentity(s.db, "linuxdo", strings.TrimSpace(providerID))
	return ok, err
}

func (s *userStore) get(id string) (User, bool, error) {
	return loadUserByID(s.db, strings.TrimSpace(id))
}

func (s *userStore) createSession(userID string, now time.Time) (string, error) {
	token := generateSessionID()
	_, err := s.db.Exec(
		`INSERT INTO user_sessions(token, user_id, expires_at, created_at) VALUES(?,?,?,?)`,
		token, userID, now.Add(userSessionMaxAge).Unix(), now.Unix(),
	)
	return token, err
}

func (s *userStore) deleteSession(token string) error {
	_, err := s.db.Exec(`DELETE FROM user_sessions WHERE token=?`, strings.TrimSpace(token))
	return err
}

func (s *userStore) userForSession(token string, now time.Time) (User, bool, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return User{}, false, nil
	}
	var userID string
	var expiresAt int64
	err := s.db.QueryRow(`SELECT user_id, expires_at FROM user_sessions WHERE token=?`, token).Scan(&userID, &expiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, false, nil
	}
	if err != nil {
		return User{}, false, err
	}
	if expiresAt <= now.Unix() {
		_ = s.deleteSession(token)
		return User{}, false, nil
	}
	user, ok, err := s.get(userID)
	if err != nil || !ok || user.Disabled {
		return User{}, false, err
	}
	return user, true, nil
}

func (s *userStore) listSubscriptions(userID string, now time.Time) ([]UserSubscription, error) {
	rows, err := s.db.Query(
		`SELECT id, user_id, COALESCE(source_cdk_code, ''), remaining_bytes, used_bytes, expires_at, created_at, allow_proxy
		 FROM user_subscriptions
		 WHERE user_id=?
		 ORDER BY expires_at ASC, created_at ASC, id`,
		strings.TrimSpace(userID),
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []UserSubscription
	for rows.Next() {
		sub, err := scanUserSubscription(rows, now)
		if err != nil {
			return nil, err
		}
		out = append(out, sub)
	}
	return out, rows.Err()
}

func (s *userStore) quota(userID string, now time.Time) (UserQuota, []UserSubscription, error) {
	subs, err := s.listSubscriptions(userID, now)
	if err != nil {
		return UserQuota{}, nil, err
	}
	var quota UserQuota
	for _, sub := range subs {
		if sub.Expired || sub.RemainingBytes <= 0 {
			continue
		}
		quota.TotalRemainingBytes += sub.RemainingBytes
		quota.AllowProxyAvailable = quota.AllowProxyAvailable || sub.AllowProxy
		if quota.NextExpiresAt == "" {
			quota.NextExpiresAt = sub.ExpiresAt
		}
	}
	quota.TotalRemainingLabel = formatTrafficLabel(quota.TotalRemainingBytes)
	return quota, subs, nil
}

func (s *userStore) hasQuota(userID string, bytes int64, requireProxy bool, now time.Time) error {
	if bytes < 0 {
		bytes = 0
	}
	remaining, err := s.remainingQuota(userID, requireProxy, now)
	if err != nil {
		return err
	}
	if remaining <= 0 && bytes > 0 {
		return errUserQuotaExhausted
	}
	if remaining < bytes {
		return errUserQuotaOverdraw{size: bytes, remaining: remaining}
	}
	return nil
}

func (s *userStore) remainingQuota(userID string, requireProxy bool, now time.Time) (int64, error) {
	query := `SELECT COALESCE(SUM(remaining_bytes), 0)
		FROM user_subscriptions
		WHERE user_id=? AND expires_at>? AND remaining_bytes>0`
	args := []any{strings.TrimSpace(userID), now.Unix()}
	if requireProxy {
		query += ` AND allow_proxy=1`
	}
	var remaining int64
	if err := s.db.QueryRow(query, args...).Scan(&remaining); err != nil {
		return 0, err
	}
	return remaining, nil
}

func (s *userStore) chargeIfEnough(userID string, bytes int64, requireProxy bool, now time.Time) error {
	if bytes <= 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	query := `SELECT id, remaining_bytes
		FROM user_subscriptions
		WHERE user_id=? AND expires_at>? AND remaining_bytes>0`
	args := []any{strings.TrimSpace(userID), now.Unix()}
	if requireProxy {
		query += ` AND allow_proxy=1`
	}
	query += ` ORDER BY expires_at ASC, created_at ASC, id`

	rows, err := tx.Query(query, args...)
	if err != nil {
		return err
	}
	type bucket struct {
		id        string
		remaining int64
	}
	var buckets []bucket
	var total int64
	for rows.Next() {
		var b bucket
		if err := rows.Scan(&b.id, &b.remaining); err != nil {
			rows.Close()
			return err
		}
		total += b.remaining
		buckets = append(buckets, b)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if total <= 0 {
		return errUserQuotaExhausted
	}
	if total < bytes {
		return errUserQuotaOverdraw{size: bytes, remaining: total}
	}

	left := bytes
	for _, b := range buckets {
		if left <= 0 {
			break
		}
		take := b.remaining
		if take > left {
			take = left
		}
		if _, err := tx.Exec(
			`UPDATE user_subscriptions
			 SET remaining_bytes=remaining_bytes-?, used_bytes=used_bytes+?
			 WHERE id=? AND remaining_bytes>=?`,
			take, take, b.id, take,
		); err != nil {
			return err
		}
		left -= take
	}
	if left != 0 {
		return errUserQuotaOverdraw{size: bytes, remaining: bytes - left}
	}
	return tx.Commit()
}

func (s *userStore) redeemCDK(userID, code string, now time.Time) (UserSubscription, error) {
	code = normalizeCode(code)
	if code == "" {
		return UserSubscription{}, errCDKNotFound
	}

	tx, err := s.db.Begin()
	if err != nil {
		return UserSubscription{}, err
	}
	defer tx.Rollback()

	var (
		remaining    int64
		used         int64
		createdAt    int64
		allowProxy   int
		durationDays int
		redeemedBy   sql.NullString
		redeemedAt   sql.NullInt64
		revokedAt    sql.NullInt64
	)
	err = tx.QueryRow(
		`SELECT remaining_bytes, used_bytes, created_at, allow_proxy, duration_days, redeemed_by_user_id, redeemed_at, revoked_at
		 FROM cdks WHERE code=?`,
		code,
	).Scan(&remaining, &used, &createdAt, &allowProxy, &durationDays, &redeemedBy, &redeemedAt, &revokedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return UserSubscription{}, errCDKNotFound
	}
	if err != nil {
		return UserSubscription{}, err
	}
	if revokedAt.Valid && revokedAt.Int64 > 0 {
		return UserSubscription{}, errVoucherRevoked
	}
	if redeemedAt.Valid && redeemedAt.Int64 > 0 {
		return UserSubscription{}, errVoucherRedeemed
	}
	if remaining <= 0 {
		return UserSubscription{}, errCDKExhausted
	}
	if durationDays < 1 {
		durationDays = 1
	}

	nowUnix := now.Unix()
	expiresAt := now.Add(time.Duration(durationDays) * 24 * time.Hour).Unix()
	sub := UserSubscription{
		ID:             newSubscriptionID(),
		UserID:         userID,
		SourceCDKCode:  code,
		RemainingBytes: remaining,
		UsedBytes:      0,
		AllowProxy:     allowProxy != 0,
	}
	if _, err := tx.Exec(
		`INSERT INTO user_subscriptions(id, user_id, source_cdk_code, remaining_bytes, used_bytes, expires_at, created_at, allow_proxy)
		 VALUES(?,?,?,?,?,?,?,?)`,
		sub.ID, userID, code, remaining, 0, expiresAt, nowUnix, allowProxy,
	); err != nil {
		return UserSubscription{}, err
	}
	res, err := tx.Exec(
		`UPDATE cdks
		 SET redeemed_by_user_id=?, redeemed_at=?, expires_at=?
		 WHERE code=? AND redeemed_at IS NULL AND revoked_at IS NULL`,
		userID, nowUnix, expiresAt, code,
	)
	if err != nil {
		return UserSubscription{}, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return UserSubscription{}, errVoucherRedeemed
	}
	if err := tx.Commit(); err != nil {
		return UserSubscription{}, err
	}
	sub.ExpiresAt = time.Unix(expiresAt, 0).Format(time.RFC3339)
	sub.CreatedAt = time.Unix(nowUnix, 0).Format(time.RFC3339)
	sub.DaysLeft = durationDays
	sub.RemainingLabel = formatTrafficLabel(sub.RemainingBytes)
	sub.UsedLabel = formatTrafficLabel(sub.UsedBytes)
	return sub, nil
}

type userIdentityRecord struct {
	userID       string
	passwordHash string
}

type identityDB interface {
	QueryRow(query string, args ...any) *sql.Row
}

func loadUserIdentity(db identityDB, provider, providerUserID string) (userIdentityRecord, bool, error) {
	var rec userIdentityRecord
	err := db.QueryRow(
		`SELECT user_id, COALESCE(password_hash, '') FROM user_identities WHERE provider=? AND provider_user_id=?`,
		provider, providerUserID,
	).Scan(&rec.userID, &rec.passwordHash)
	if errors.Is(err, sql.ErrNoRows) {
		return userIdentityRecord{}, false, nil
	}
	if err != nil {
		return userIdentityRecord{}, false, err
	}
	return rec, true, nil
}

func loadUserByID(db identityDB, id string) (User, bool, error) {
	var user User
	var disabled int
	err := db.QueryRow(
		`SELECT id, COALESCE(email, ''), display_name, avatar_url, disabled, created_at, updated_at
		 FROM users WHERE id=?`,
		strings.TrimSpace(id),
	).Scan(&user.ID, &user.Email, &user.DisplayName, &user.AvatarURL, &disabled, &user.CreatedAt, &user.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, false, nil
	}
	if err != nil {
		return User{}, false, err
	}
	user.Disabled = disabled != 0
	return user, true, nil
}

type subscriptionScanner interface {
	Scan(dest ...any) error
}

func scanUserSubscription(scanner subscriptionScanner, now time.Time) (UserSubscription, error) {
	var sub UserSubscription
	var allow int
	var expiresAt int64
	var createdAt int64
	if err := scanner.Scan(&sub.ID, &sub.UserID, &sub.SourceCDKCode, &sub.RemainingBytes, &sub.UsedBytes, &expiresAt, &createdAt, &allow); err != nil {
		return UserSubscription{}, err
	}
	sub.AllowProxy = allow != 0
	sub.Expired = expiresAt <= now.Unix()
	if !sub.Expired {
		sub.DaysLeft = int((expiresAt - now.Unix() + 86399) / 86400)
	}
	sub.ExpiresAt = time.Unix(expiresAt, 0).Format(time.RFC3339)
	sub.CreatedAt = time.Unix(createdAt, 0).Format(time.RFC3339)
	sub.RemainingLabel = formatTrafficLabel(sub.RemainingBytes)
	sub.UsedLabel = formatTrafficLabel(sub.UsedBytes)
	return sub, nil
}
