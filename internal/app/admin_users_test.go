package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func newAdminUsersTestServer(t *testing.T) (*Server, time.Time) {
	t.Helper()
	db, err := openDatabase(":memory:")
	if err != nil {
		t.Fatalf("openDatabase: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	return &Server{users: newUserStore(db), nowFunc: func() time.Time { return now }}, now
}

func insertAdminTestUser(t *testing.T, s *Server, user User, identities ...string) {
	t.Helper()
	if _, err := s.users.db.Exec(
		`INSERT INTO users(id, email, display_name, avatar_url, disabled, created_at, updated_at)
		 VALUES(?,?,?,?,?,?,?)`,
		user.ID, user.Email, user.DisplayName, user.AvatarURL, b2i(user.Disabled), user.CreatedAt, user.UpdatedAt,
	); err != nil {
		t.Fatalf("insert user %s: %v", user.ID, err)
	}
	for _, provider := range identities {
		providerID := provider + "-private-" + user.ID
		passwordHash := ""
		if provider == "email" {
			passwordHash = "private-password-hash"
		}
		if _, err := s.users.db.Exec(
			`INSERT INTO user_identities
			 (provider, provider_user_id, user_id, email, username, display_name, avatar_url, password_hash, created_at, updated_at)
			 VALUES(?,?,?,?,?,?,?,?,?,?)`,
			provider, providerID, user.ID, user.Email, provider+"-private-name", user.DisplayName, user.AvatarURL,
			passwordHash, user.CreatedAt, user.UpdatedAt,
		); err != nil {
			t.Fatalf("insert %s identity for %s: %v", provider, user.ID, err)
		}
	}
}

func insertAdminTestSubscription(
	t *testing.T,
	s *Server,
	id, userID, sourceCDK string,
	remaining, used int64,
	expiresAt, createdAt time.Time,
	allowProxy bool,
	revision int64,
	terminatedAt *time.Time,
) {
	t.Helper()
	var terminated any
	if terminatedAt != nil {
		terminated = terminatedAt.Unix()
	}
	if _, err := s.users.db.Exec(
		`INSERT INTO user_subscriptions
		 (id, user_id, source_cdk_code, remaining_bytes, used_bytes, expires_at, created_at,
		  allow_proxy, quota_generation, revision, terminated_at)
		 VALUES(?,?,?,?,?,?,?,?,0,?,?)`,
		id, userID, nullIfEmpty(sourceCDK), remaining, used, expiresAt.Unix(), createdAt.Unix(),
		b2i(allowProxy), revision, terminated,
	); err != nil {
		t.Fatalf("insert subscription %s: %v", id, err)
	}
}

func nullIfEmpty(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func adminUsersRequest(t *testing.T, method, target string, payload any, userID, subscriptionID string) *http.Request {
	t.Helper()
	var body *bytes.Reader
	if payload == nil {
		body = bytes.NewReader(nil)
	} else {
		data, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("marshal request: %v", err)
		}
		body = bytes.NewReader(data)
	}
	req := httptest.NewRequest(method, target, body)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if userID != "" {
		req.SetPathValue("userID", userID)
	}
	if subscriptionID != "" {
		req.SetPathValue("subscriptionID", subscriptionID)
	}
	return req
}

func TestAdminListUsersSearchPaginationAndPrivacy(t *testing.T) {
	s, now := newAdminUsersTestServer(t)
	users := []User{
		{ID: "usr_alpha", Email: "alpha@example.com", DisplayName: "Alpha", AvatarURL: "https://example.com/a.png", CreatedAt: now.Add(-3 * time.Hour).Unix(), UpdatedAt: now.Unix()},
		{ID: "usr_beta", Email: "beta@example.com", DisplayName: "Beta", CreatedAt: now.Add(-2 * time.Hour).Unix(), UpdatedAt: now.Unix()},
		{ID: "usr_gamma", Email: "gamma@example.com", DisplayName: "Gamma", Disabled: true, CreatedAt: now.Add(-time.Hour).Unix(), UpdatedAt: now.Unix()},
	}
	insertAdminTestUser(t, s, users[0], "linuxdo", "email")
	insertAdminTestUser(t, s, users[1], "email")
	insertAdminTestUser(t, s, users[2], "linuxdo")
	insertAdminTestSubscription(t, s, "sub_alpha", users[0].ID, "CDK-ALPHA", 100, 20, now.Add(24*time.Hour), now, true, 1, nil)
	insertAdminTestSubscription(t, s, "sub_alpha_empty", users[0].ID, "", 0, 100, now.Add(48*time.Hour), now, false, 1, nil)
	if _, err := s.users.db.Exec(
		`INSERT INTO user_sessions(token_hash, user_id, expires_at, created_at) VALUES(?,?,?,?)`,
		"private-session-token", users[0].ID, now.Add(time.Hour).Unix(), now.Unix(),
	); err != nil {
		t.Fatalf("insert session: %v", err)
	}

	rec := httptest.NewRecorder()
	s.handleAdminListUsers(rec, adminUsersRequest(t, http.MethodGet, "/api/users?limit=2&offset=0", nil, "", ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("list status = %d body=%s", rec.Code, rec.Body.String())
	}
	var page adminUserListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if page.Total != 3 || page.Limit != 2 || page.Offset != 0 || len(page.Users) != 2 {
		t.Fatalf("page = %+v", page)
	}
	if page.Users[0].User.ID != "usr_gamma" || page.Users[1].User.ID != "usr_beta" {
		t.Fatalf("user order = %s, %s", page.Users[0].User.ID, page.Users[1].User.ID)
	}
	if page.Users[0].User.CreatedAt != formatUnixRFC3339(users[2].CreatedAt) || page.Users[0].User.UpdatedAt != formatUnixRFC3339(users[2].UpdatedAt) {
		t.Fatalf("admin user timestamps are not RFC3339: %+v", page.Users[0].User)
	}

	rec = httptest.NewRecorder()
	s.handleAdminListUsers(rec, adminUsersRequest(t, http.MethodGet, "/api/users?q=ALPHA&limit=50&offset=0", nil, "", ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("search status = %d body=%s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode search: %v", err)
	}
	if page.Total != 1 || len(page.Users) != 1 {
		t.Fatalf("search page = %+v", page)
	}
	summary := page.Users[0]
	if strings.Join(summary.AuthProviders, ",") != "email,linuxdo" {
		t.Fatalf("providers = %v", summary.AuthProviders)
	}
	if summary.SubscriptionCount != 2 || summary.ActiveSubscriptionCount != 1 {
		t.Fatalf("subscription counts = %d/%d", summary.SubscriptionCount, summary.ActiveSubscriptionCount)
	}
	if summary.Quota.TotalRemainingBytes != 100 || !summary.Quota.AllowProxyAvailable {
		t.Fatalf("quota = %+v", summary.Quota)
	}
	body := rec.Body.String()
	for _, secret := range []string{"private-password-hash", "linuxdo-private-usr_alpha", "private-session-token"} {
		if strings.Contains(body, secret) {
			t.Fatalf("response leaked %q: %s", secret, body)
		}
	}
}

func TestAdminUserDetailReturnsSubscriptionStates(t *testing.T) {
	s, now := newAdminUsersTestServer(t)
	user := User{ID: "usr_detail", Email: "detail@example.com", DisplayName: "Detail", CreatedAt: now.Add(-time.Hour).Unix(), UpdatedAt: now.Unix()}
	insertAdminTestUser(t, s, user, "email", "linuxdo")
	terminatedAt := now.Add(-30 * time.Minute)
	insertAdminTestSubscription(t, s, "sub_active", user.ID, "CDK-DETAIL", 100, 25, now.Add(24*time.Hour), now.Add(-time.Hour), true, 4, nil)
	insertAdminTestSubscription(t, s, "sub_exhausted", user.ID, "", 0, 200, now.Add(48*time.Hour), now.Add(-time.Hour), false, 2, nil)
	insertAdminTestSubscription(t, s, "sub_expired", user.ID, "", 50, 10, now.Add(-time.Minute), now.Add(-2*time.Hour), true, 3, nil)
	insertAdminTestSubscription(t, s, "sub_terminated", user.ID, "", 0, 0, terminatedAt, now.Add(-3*time.Hour), true, 5, &terminatedAt)
	if _, err := s.users.db.Exec(
		`INSERT INTO user_quota_reservations
		 (job_id, subscription_id, user_id, reserved_bytes, require_proxy, created_at, quota_generation)
		 VALUES('job_detail', 'sub_active', ?, 30, 1, ?, 0)`,
		user.ID, now.Unix(),
	); err != nil {
		t.Fatalf("insert reservation: %v", err)
	}

	rec := httptest.NewRecorder()
	s.handleAdminGetUser(rec, adminUsersRequest(t, http.MethodGet, "/api/users/usr_detail", nil, user.ID, ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("detail status = %d body=%s", rec.Code, rec.Body.String())
	}
	var detail adminUserDetail
	if err := json.Unmarshal(rec.Body.Bytes(), &detail); err != nil {
		t.Fatalf("decode detail: %v", err)
	}
	if detail.User.ID != user.ID || detail.SubscriptionCount != 4 || detail.ActiveSubscriptionCount != 1 {
		t.Fatalf("detail summary = %+v", detail)
	}
	byID := make(map[string]AdminSubscription)
	for _, sub := range detail.Subscriptions {
		byID[sub.ID] = sub
	}
	if got := byID["sub_active"]; got.Status != adminSubscriptionActive || got.Source != "cdk" || got.SourceCDKCode != "CDK-DETAIL" || got.ReservedBytes != 30 || got.Revision != 4 {
		t.Fatalf("active subscription = %+v", got)
	}
	if got := byID["sub_exhausted"]; got.Status != adminSubscriptionExhausted || got.Source != "admin" {
		t.Fatalf("exhausted subscription = %+v", got)
	}
	if got := byID["sub_expired"]; got.Status != adminSubscriptionExpired || !got.Expired {
		t.Fatalf("expired subscription = %+v", got)
	}
	if got := byID["sub_terminated"]; got.Status != adminSubscriptionTerminated || got.TerminatedAt == "" {
		t.Fatalf("terminated subscription = %+v", got)
	}

	rec = httptest.NewRecorder()
	s.handleAdminGetUser(rec, adminUsersRequest(t, http.MethodGet, "/api/users/missing", nil, "missing", ""))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing detail status = %d, want 404", rec.Code)
	}
}

func TestAdminSubscriptionCreateUpdateAndRevisionConflict(t *testing.T) {
	s, now := newAdminUsersTestServer(t)
	user := User{ID: "usr_write", Email: "write@example.com", DisplayName: "Write", CreatedAt: now.Unix(), UpdatedAt: now.Unix()}
	other := User{ID: "usr_other", Email: "other@example.com", DisplayName: "Other", CreatedAt: now.Unix(), UpdatedAt: now.Unix()}
	insertAdminTestUser(t, s, user, "email")
	insertAdminTestUser(t, s, other, "email")

	create := adminCreateSubscriptionRequest{RemainingBytes: int64Ptr(200), ExpiresAt: stringPtr(now.Add(30 * 24 * time.Hour).Format(time.RFC3339)), AllowProxy: boolPtr(true)}
	rec := httptest.NewRecorder()
	s.handleAdminCreateSubscription(rec, adminUsersRequest(t, http.MethodPost, "/api/users/usr_write/subscriptions", create, user.ID, ""))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d body=%s", rec.Code, rec.Body.String())
	}
	var created AdminSubscription
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode created: %v", err)
	}
	if created.Source != "admin" || created.SourceCDKCode != "" || created.RemainingBytes != 200 || created.UsedBytes != 0 || created.Revision != 1 {
		t.Fatalf("created subscription = %+v", created)
	}

	update := adminUpdateSubscriptionRequest{ExpectedRevision: created.Revision, RemainingBytes: int64Ptr(300), AllowProxy: boolPtr(false)}
	rec = httptest.NewRecorder()
	s.handleAdminUpdateSubscription(rec, adminUsersRequest(t, http.MethodPatch, "/api/users/usr_write/subscriptions/"+created.ID, update, user.ID, created.ID))
	if rec.Code != http.StatusOK {
		t.Fatalf("update status = %d body=%s", rec.Code, rec.Body.String())
	}
	var updated AdminSubscription
	if err := json.Unmarshal(rec.Body.Bytes(), &updated); err != nil {
		t.Fatalf("decode updated: %v", err)
	}
	if updated.RemainingBytes != 300 || updated.AllowProxy || updated.Revision != 2 || updated.UsedBytes != 0 {
		t.Fatalf("updated subscription = %+v", updated)
	}

	rec = httptest.NewRecorder()
	s.handleAdminUpdateSubscription(rec, adminUsersRequest(t, http.MethodPatch, "/api/users/usr_write/subscriptions/"+created.ID, update, user.ID, created.ID))
	if rec.Code != http.StatusConflict {
		t.Fatalf("stale update status = %d body=%s", rec.Code, rec.Body.String())
	}

	crossUser := adminUpdateSubscriptionRequest{ExpectedRevision: updated.Revision, RemainingBytes: int64Ptr(400)}
	rec = httptest.NewRecorder()
	s.handleAdminUpdateSubscription(rec, adminUsersRequest(t, http.MethodPatch, "/api/users/usr_other/subscriptions/"+created.ID, crossUser, other.ID, created.ID))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("cross-user update status = %d body=%s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	s.handleAdminCreateSubscription(rec, adminUsersRequest(t, http.MethodPost, "/api/users/missing/subscriptions", create, "missing", ""))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown-user create status = %d", rec.Code)
	}

	invalid := adminCreateSubscriptionRequest{RemainingBytes: int64Ptr(-1), ExpiresAt: stringPtr(now.Add(-time.Minute).Format(time.RFC3339)), AllowProxy: boolPtr(true)}
	rec = httptest.NewRecorder()
	s.handleAdminCreateSubscription(rec, adminUsersRequest(t, http.MethodPost, "/api/users/usr_write/subscriptions", invalid, user.ID, ""))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid create status = %d", rec.Code)
	}

	zero := adminCreateSubscriptionRequest{RemainingBytes: int64Ptr(0), ExpiresAt: stringPtr(now.Add(time.Hour).Format(time.RFC3339)), AllowProxy: boolPtr(true)}
	rec = httptest.NewRecorder()
	s.handleAdminCreateSubscription(rec, adminUsersRequest(t, http.MethodPost, "/api/users/usr_write/subscriptions", zero, user.ID, ""))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("zero quota create status = %d", rec.Code)
	}
}

func TestAdminRemainingResetKeepsInflightReservation(t *testing.T) {
	s, now := newAdminUsersTestServer(t)
	user := User{ID: "usr_baseline", Email: "baseline@example.com", DisplayName: "Baseline", CreatedAt: now.Unix(), UpdatedAt: now.Unix()}
	insertAdminTestUser(t, s, user, "email")
	insertAdminTestSubscription(t, s, "sub_baseline", user.ID, "", 100, 0, now.Add(time.Hour), now, true, 1, nil)
	if err := s.users.reserveQuota("job_baseline", user.ID, 40, false, now); err != nil {
		t.Fatalf("reserve: %v", err)
	}

	rec := httptest.NewRecorder()
	update := adminUpdateSubscriptionRequest{ExpectedRevision: 2, RemainingBytes: int64Ptr(200)}
	s.handleAdminUpdateSubscription(rec, adminUsersRequest(t, http.MethodPatch, "/api/users/usr_baseline/subscriptions/sub_baseline", update, user.ID, "sub_baseline"))
	if rec.Code != http.StatusOK {
		t.Fatalf("reset status = %d body=%s", rec.Code, rec.Body.String())
	}
	if settled, err := s.users.settleQuotaReservation("job_baseline"); err != nil || settled != 40 {
		t.Fatalf("settle after reset = %d, %v", settled, err)
	}
	var remaining, used, revision int64
	if err := s.users.db.QueryRow(
		`SELECT remaining_bytes, used_bytes, revision FROM user_subscriptions WHERE id='sub_baseline'`,
	).Scan(&remaining, &used, &revision); err != nil {
		t.Fatalf("read baseline: %v", err)
	}
	if remaining != 200 || used != 40 || revision != 4 {
		t.Fatalf("baseline after settle = remaining:%d used:%d revision:%d", remaining, used, revision)
	}

	if err := s.users.reserveQuota("job_baseline_release", user.ID, 30, false, now); err != nil {
		t.Fatalf("reserve before second reset: %v", err)
	}
	rec = httptest.NewRecorder()
	update = adminUpdateSubscriptionRequest{ExpectedRevision: 5, RemainingBytes: int64Ptr(250)}
	s.handleAdminUpdateSubscription(rec, adminUsersRequest(t, http.MethodPatch, "/api/users/usr_baseline/subscriptions/sub_baseline", update, user.ID, "sub_baseline"))
	if rec.Code != http.StatusOK {
		t.Fatalf("second reset status = %d body=%s", rec.Code, rec.Body.String())
	}
	if released, err := s.users.releaseQuotaReservation("job_baseline_release"); err != nil || released != 0 {
		t.Fatalf("release after reset = %d, %v; want baseline to absorb old reservation", released, err)
	}
	if err := s.users.db.QueryRow(
		`SELECT remaining_bytes, used_bytes, revision FROM user_subscriptions WHERE id='sub_baseline'`,
	).Scan(&remaining, &used, &revision); err != nil {
		t.Fatalf("read second baseline: %v", err)
	}
	if remaining != 250 || used != 40 || revision != 7 {
		t.Fatalf("baseline after release = remaining:%d used:%d revision:%d", remaining, used, revision)
	}
}

func TestAdminCanRestoreExpiredSubscription(t *testing.T) {
	s, now := newAdminUsersTestServer(t)
	user := User{ID: "usr_restore", Email: "restore@example.com", DisplayName: "Restore", CreatedAt: now.Unix(), UpdatedAt: now.Unix()}
	insertAdminTestUser(t, s, user, "email")
	insertAdminTestSubscription(t, s, "sub_restore", user.ID, "", 50, 0, now.Add(-time.Minute), now.Add(-time.Hour), true, 1, nil)

	restoredExpiry := now.Add(24 * time.Hour).Format(time.RFC3339)
	rec := httptest.NewRecorder()
	update := adminUpdateSubscriptionRequest{ExpectedRevision: 1, ExpiresAt: &restoredExpiry}
	s.handleAdminUpdateSubscription(rec, adminUsersRequest(t, http.MethodPatch, "/api/users/usr_restore/subscriptions/sub_restore", update, user.ID, "sub_restore"))
	if rec.Code != http.StatusOK {
		t.Fatalf("restore status = %d body=%s", rec.Code, rec.Body.String())
	}
	var restored AdminSubscription
	if err := json.Unmarshal(rec.Body.Bytes(), &restored); err != nil {
		t.Fatalf("decode restored: %v", err)
	}
	if restored.Status != adminSubscriptionActive || restored.Expired || restored.Revision != 2 || restored.ExpiresAt != restoredExpiry {
		t.Fatalf("restored subscription = %+v", restored)
	}
}

func TestAdminDisableProxyOnlyInvalidatesProxyReservations(t *testing.T) {
	s, now := newAdminUsersTestServer(t)
	user := User{ID: "usr_proxy_edit", Email: "proxy-edit@example.com", DisplayName: "Proxy", CreatedAt: now.Unix(), UpdatedAt: now.Unix()}
	insertAdminTestUser(t, s, user, "email")
	insertAdminTestSubscription(t, s, "sub_proxy_edit", user.ID, "", 300, 0, now.Add(time.Hour), now, true, 1, nil)
	if err := s.users.reserveQuota("job_direct", user.ID, 50, false, now); err != nil {
		t.Fatalf("reserve direct: %v", err)
	}
	if err := s.users.reserveQuota("job_proxy", user.ID, 60, true, now); err != nil {
		t.Fatalf("reserve proxy: %v", err)
	}

	rec := httptest.NewRecorder()
	update := adminUpdateSubscriptionRequest{ExpectedRevision: 3, AllowProxy: boolPtr(false)}
	s.handleAdminUpdateSubscription(rec, adminUsersRequest(t, http.MethodPatch, "/api/users/usr_proxy_edit/subscriptions/sub_proxy_edit", update, user.ID, "sub_proxy_edit"))
	if rec.Code != http.StatusOK {
		t.Fatalf("disable proxy status = %d body=%s", rec.Code, rec.Body.String())
	}
	if settled, err := s.users.settleQuotaReservation("job_direct"); err != nil || settled != 50 {
		t.Fatalf("settle direct = %d, %v", settled, err)
	}
	if _, err := s.users.settleQuotaReservation("job_proxy"); !errors.Is(err, errQuotaReservationInvalidated) {
		t.Fatalf("settle proxy error = %v, want invalidated", err)
	}
	var remaining, used, revision int64
	if err := s.users.db.QueryRow(
		`SELECT remaining_bytes, used_bytes, revision FROM user_subscriptions WHERE id='sub_proxy_edit'`,
	).Scan(&remaining, &used, &revision); err != nil {
		t.Fatalf("read proxy edit: %v", err)
	}
	if remaining != 250 || used != 50 || revision != 5 {
		t.Fatalf("proxy edit result = remaining:%d used:%d revision:%d", remaining, used, revision)
	}
}

func TestAdminDisableProxyDoesNotRefundStaleGenerationReservation(t *testing.T) {
	s, now := newAdminUsersTestServer(t)
	user := User{ID: "usr_proxy_baseline", Email: "proxy-baseline@example.com", DisplayName: "Proxy baseline", CreatedAt: now.Unix(), UpdatedAt: now.Unix()}
	insertAdminTestUser(t, s, user, "email")
	insertAdminTestSubscription(t, s, "sub_proxy_baseline", user.ID, "", 300, 0, now.Add(time.Hour), now, true, 1, nil)
	if err := s.users.reserveQuota("job_proxy_baseline", user.ID, 60, true, now); err != nil {
		t.Fatalf("reserve proxy: %v", err)
	}

	reset := adminUpdateSubscriptionRequest{ExpectedRevision: 2, RemainingBytes: int64Ptr(200)}
	rec := httptest.NewRecorder()
	s.handleAdminUpdateSubscription(rec, adminUsersRequest(t, http.MethodPatch, "/api/users/usr_proxy_baseline/subscriptions/sub_proxy_baseline", reset, user.ID, "sub_proxy_baseline"))
	if rec.Code != http.StatusOK {
		t.Fatalf("reset baseline status = %d body=%s", rec.Code, rec.Body.String())
	}

	disable := adminUpdateSubscriptionRequest{ExpectedRevision: 3, AllowProxy: boolPtr(false)}
	rec = httptest.NewRecorder()
	s.handleAdminUpdateSubscription(rec, adminUsersRequest(t, http.MethodPatch, "/api/users/usr_proxy_baseline/subscriptions/sub_proxy_baseline", disable, user.ID, "sub_proxy_baseline"))
	if rec.Code != http.StatusOK {
		t.Fatalf("disable proxy status = %d body=%s", rec.Code, rec.Body.String())
	}

	var remaining, revision, reservationGeneration int64
	if err := s.users.db.QueryRow(
		`SELECT remaining_bytes, revision FROM user_subscriptions WHERE id='sub_proxy_baseline'`,
	).Scan(&remaining, &revision); err != nil {
		t.Fatalf("read subscription: %v", err)
	}
	if err := s.users.db.QueryRow(
		`SELECT quota_generation FROM user_quota_reservations WHERE job_id='job_proxy_baseline'`,
	).Scan(&reservationGeneration); err != nil {
		t.Fatalf("read reservation: %v", err)
	}
	if remaining != 200 || revision != 4 || reservationGeneration != invalidQuotaReservationGeneration {
		t.Fatalf("disabled proxy state = remaining:%d revision:%d reservation generation:%d", remaining, revision, reservationGeneration)
	}
	if _, err := s.users.settleQuotaReservation("job_proxy_baseline"); !errors.Is(err, errQuotaReservationInvalidated) {
		t.Fatalf("settle invalidated proxy reservation = %v", err)
	}
	if err := s.users.db.QueryRow(
		`SELECT remaining_bytes, revision FROM user_subscriptions WHERE id='sub_proxy_baseline'`,
	).Scan(&remaining, &revision); err != nil {
		t.Fatalf("read subscription after cleanup: %v", err)
	}
	if remaining != 200 || revision != 4 {
		t.Fatalf("cleanup changed baseline = remaining:%d revision:%d", remaining, revision)
	}
}

func TestAdminTerminateSubscriptionInvalidatesAllReservations(t *testing.T) {
	s, now := newAdminUsersTestServer(t)
	user := User{ID: "usr_terminate", Email: "terminate@example.com", DisplayName: "Terminate", CreatedAt: now.Unix(), UpdatedAt: now.Unix()}
	insertAdminTestUser(t, s, user, "email")
	insertAdminTestSubscription(t, s, "sub_terminate", user.ID, "", 100, 0, now.Add(time.Hour), now, true, 1, nil)
	if err := s.users.reserveQuota("job_terminate", user.ID, 40, false, now); err != nil {
		t.Fatalf("reserve: %v", err)
	}

	rec := httptest.NewRecorder()
	reqBody := adminTerminateSubscriptionRequest{ExpectedRevision: 2}
	s.handleAdminTerminateSubscription(rec, adminUsersRequest(t, http.MethodPost, "/api/users/usr_terminate/subscriptions/sub_terminate/terminate", reqBody, user.ID, "sub_terminate"))
	if rec.Code != http.StatusOK {
		t.Fatalf("terminate status = %d body=%s", rec.Code, rec.Body.String())
	}
	var terminated AdminSubscription
	if err := json.Unmarshal(rec.Body.Bytes(), &terminated); err != nil {
		t.Fatalf("decode terminated: %v", err)
	}
	if terminated.Status != adminSubscriptionTerminated || terminated.RemainingBytes != 0 || terminated.Revision != 3 || terminated.TerminatedAt == "" {
		t.Fatalf("terminated subscription = %+v", terminated)
	}
	if _, err := s.users.settleQuotaReservation("job_terminate"); !errors.Is(err, errQuotaReservationInvalidated) {
		t.Fatalf("settle terminated reservation error = %v", err)
	}
	var reservations int
	if err := s.users.db.QueryRow(`SELECT COUNT(*) FROM user_quota_reservations WHERE job_id='job_terminate'`).Scan(&reservations); err != nil {
		t.Fatalf("count terminated reservations: %v", err)
	}
	if reservations != 0 {
		t.Fatalf("terminated reservations = %d, want 0", reservations)
	}

	rec = httptest.NewRecorder()
	s.handleAdminTerminateSubscription(rec, adminUsersRequest(t, http.MethodPost, "/api/users/usr_terminate/subscriptions/sub_terminate/terminate", adminTerminateSubscriptionRequest{ExpectedRevision: 3}, user.ID, "sub_terminate"))
	if rec.Code != http.StatusConflict {
		t.Fatalf("repeat terminate status = %d body=%s", rec.Code, rec.Body.String())
	}
}

func int64Ptr(value int64) *int64    { return &value }
func stringPtr(value string) *string { return &value }
func boolPtr(value bool) *bool       { return &value }
