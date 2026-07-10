package pikpak

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	userHost         = "https://user.mypikpak.com"
	driveHost        = "https://api-drive.mypikpak.com"
	clientID         = "YNxT9w7GMdWvEOKa"
	clientSecret     = "dbw2OtmVEeuUvIptb1Coyg"
	clientVersion    = "1.47.1"
	packageName      = "com.pikcloud.pikpak"
	sdkVersion       = "2.0.4.204000"
	browserUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36"
)

var (
	emailPattern = regexp.MustCompile(`\w+([-.+]\w+)*@\w+([-.]\w+)*\.\w+([-.]\w+)*`)
	phonePattern = regexp.MustCompile(`^\d{11,18}$`)
	captchaSalts = []string{
		"Gez0T9ijiI9WCeTsKSg3SMlx",
		"zQdbalsolyb1R/",
		"ftOjr52zt51JD68C3s",
		"yeOBMH0JkbQdEFNNwQ0RI9T3wU/v",
		"BRJrQZiTQ65WtMvwO",
		"je8fqxKPdQVJiy1DM6Bc9Nb1",
		"niV",
		"9hFCW2R1",
		"sHKHpe2i96",
		"p7c5E6AcXQ/IJUuAEC9W6",
		"",
		"aRv9hjc9P+Pbn+u3krN6",
		"BzStcgE8qVdqjEH16l4",
		"SqgeZvL5j9zoHP95xWHt",
		"zVof5yaJkPe3VFpadPof",
	}
)

type Config struct {
	Username       string
	Password       string
	SessionFile    string
	SessionStore   SessionStore
	RootFolderName string
	RequestTimeout time.Duration
}

// SessionStore persists the serialized PikPak session.
type SessionStore interface {
	Load() ([]byte, error)
	Save(data []byte) error
	Delete() error
}

type Client struct {
	config   Config
	http     *http.Client
	deviceID string

	authMu        sync.Mutex
	sessionLoaded bool
	accessToken   string
	refreshToken  string
	userID        string
	expiresAt     time.Time

	rootMu       sync.Mutex
	rootFolderID string
}

func NewClient(cfg Config) *Client {
	cfg.Username = strings.TrimSpace(cfg.Username)
	cfg.RootFolderName = strings.TrimSpace(cfg.RootFolderName)

	deviceID := ""
	if cfg.Username != "" || cfg.Password != "" {
		sum := md5.Sum([]byte(cfg.Username + cfg.Password))
		deviceID = hex.EncodeToString(sum[:])
	}

	return &Client{
		config: cfg,
		http:   &http.Client{
			// 不设置全局 Timeout，每个请求通过 context 控制超时
			// 避免分享链接等长超时请求被全局 timeout 打断
		},
		deviceID: deviceID,
	}
}

func (c *Client) requestTimeout() time.Duration {
	timeout := c.config.RequestTimeout
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	return timeout
}

func (c *Client) requestContext(ctx context.Context) (context.Context, context.CancelFunc) {
	timeout := c.requestTimeout()
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			requestCtx, cancel := context.WithCancel(ctx)
			cancel()
			return requestCtx, func() {}
		}
		if remaining < timeout {
			timeout = remaining
		}
	}
	return context.WithTimeout(ctx, timeout)
}

func (c *Client) DeviceID() string {
	if c.deviceID != "" {
		return c.deviceID
	}
	if c.config.Username != "" || c.config.Password != "" {
		return md5Hex(c.config.Username + c.config.Password)
	}
	return ""
}

func (c *Client) Status() SessionStatus {
	c.authMu.Lock()
	defer c.authMu.Unlock()

	if !c.sessionLoaded {
		_ = c.loadSessionLocked()
		c.sessionLoaded = true
	}

	loggedIn := c.refreshToken != "" || c.accessToken != ""
	hasCredentials := strings.TrimSpace(c.config.Username) != "" && c.config.Password != ""

	return SessionStatus{
		Ready:     loggedIn || hasCredentials,
		LoggedIn:  loggedIn,
		Persisted: loggedIn && (c.config.SessionStore != nil || c.config.SessionFile != ""),
		Username:  strings.TrimSpace(c.config.Username),
	}
}

func (c *Client) Login(ctx context.Context, username, password string) error {
	username = strings.TrimSpace(username)
	if username == "" || strings.TrimSpace(password) == "" {
		return fmt.Errorf("username and password are required")
	}

	c.authMu.Lock()
	defer c.authMu.Unlock()

	c.config.Username = username
	c.config.Password = password
	c.deviceID = md5Hex(username + password)
	c.accessToken = ""
	c.refreshToken = ""
	c.userID = ""
	c.expiresAt = time.Time{}
	c.sessionLoaded = true

	if err := c.loginLocked(ctx); err != nil {
		return err
	}
	if err := c.saveSessionLocked(); err != nil {
		return err
	}

	c.resetRootFolder()
	return nil
}

func (c *Client) Logout() error {
	c.authMu.Lock()
	c.config.Username = ""
	c.config.Password = ""
	c.accessToken = ""
	c.refreshToken = ""
	c.userID = ""
	c.expiresAt = time.Time{}
	c.deviceID = ""
	c.sessionLoaded = true
	sessionStore := c.config.SessionStore
	sessionFile := c.config.SessionFile
	c.authMu.Unlock()

	c.resetRootFolder()

	if sessionStore != nil {
		if err := sessionStore.Delete(); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	if sessionFile == "" {
		return nil
	}
	if err := os.Remove(sessionFile); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func (c *Client) EnsureRootFolder(ctx context.Context) (string, error) {
	c.rootMu.Lock()
	defer c.rootMu.Unlock()

	if c.rootFolderID != "" {
		return c.rootFolderID, nil
	}

	rootID, err := c.ensurePath(ctx, []string{c.config.RootFolderName})
	if err != nil {
		return "", err
	}
	c.rootFolderID = rootID
	return rootID, nil
}

func (c *Client) CreateFolder(ctx context.Context, name, parentID string) (*FileEntry, error) {
	payload := map[string]any{
		"kind":      "drive#folder",
		"name":      name,
		"parent_id": parentID,
	}

	var resp CreateFileResponse
	if err := c.doJSON(ctx, http.MethodPost, driveHost, "/drive/v1/files", nil, payload, true, &resp); err != nil {
		return nil, err
	}
	return &resp.File, nil
}

func (c *Client) CreateOfflineTask(ctx context.Context, sourceURL, parentID, name string) (*TaskEntry, error) {
	payload := map[string]any{
		"kind":        "drive#file",
		"name":        name,
		"upload_type": "UPLOAD_TYPE_URL",
		"url": map[string]string{
			"url": sourceURL,
		},
		"folder_type": "",
		"parent_id":   parentID,
	}

	if parentID == "" {
		payload["folder_type"] = "DOWNLOAD"
	}

	var resp CreateFileResponse
	if err := c.doJSON(ctx, http.MethodPost, driveHost, "/drive/v1/files", nil, payload, true, &resp); err != nil {
		return nil, err
	}
	return &resp.Task, nil
}

func (c *Client) ListOfflineTasks(ctx context.Context, phases []string) ([]TaskEntry, error) {
	if len(phases) == 0 {
		phases = []string{
			"PHASE_TYPE_PENDING",
			"PHASE_TYPE_RUNNING",
			"PHASE_TYPE_COMPLETE",
			"PHASE_TYPE_ERROR",
		}
	}

	var tasks []TaskEntry
	pageToken := ""
	for {
		filters, err := json.Marshal(map[string]any{
			"phase": map[string]string{
				"in": strings.Join(phases, ","),
			},
		})
		if err != nil {
			return nil, err
		}

		query := url.Values{}
		query.Set("type", "offline")
		query.Set("thumbnail_size", "SIZE_SMALL")
		query.Set("limit", "200")
		query.Set("with", "reference_resource")
		query.Set("filters", string(filters))
		if pageToken != "" {
			query.Set("page_token", pageToken)
		}

		var resp TaskListResponse
		if err := c.doJSON(ctx, http.MethodGet, driveHost, "/drive/v1/tasks", query, nil, true, &resp); err != nil {
			return nil, err
		}

		tasks = append(tasks, resp.Tasks...)
		if resp.NextPageToken == "" || resp.NextPageToken == pageToken {
			break
		}
		pageToken = resp.NextPageToken
	}

	return tasks, nil
}

func (c *Client) ListFiles(ctx context.Context, parentID string) ([]FileEntry, error) {
	baseFilters := map[string]any{
		"trashed": map[string]bool{
			"eq": false,
		},
		"phase": map[string]string{
			"eq": "PHASE_TYPE_COMPLETE",
		},
	}
	return c.listFilesWithFilters(ctx, parentID, baseFilters)
}

func (c *Client) GetFile(ctx context.Context, fileID string) (*FileEntry, error) {
	query := url.Values{}
	query.Set("thumbnail_size", "SIZE_LARGE")

	var resp FileEntry
	if err := c.doJSON(ctx, http.MethodGet, driveHost, "/drive/v1/files/"+fileID, query, nil, true, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *Client) GetVIPInfo(ctx context.Context) (*VIPInfo, error) {
	var resp VIPInfo
	if err := c.doJSON(ctx, http.MethodGet, driveHost, "/drive/v1/privilege/vip", nil, nil, true, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *Client) GetShareInfo(ctx context.Context, shareID, passCode, parentID string) (*ShareListResponse, error) {
	return c.getShareList(ctx, "/drive/v1/share", shareID, "pass_code", passCode, parentID, "3")
}

func (c *Client) GetShareFolder(ctx context.Context, shareID, passCodeToken, parentID string) (*ShareListResponse, error) {
	return c.getShareList(ctx, "/drive/v1/share/detail", shareID, "pass_code_token", passCodeToken, parentID, "6")
}

func (c *Client) getShareList(ctx context.Context, pathValue, shareID, passKey, passValue, parentID, order string) (*ShareListResponse, error) {
	query := url.Values{}
	query.Set("limit", "100")
	query.Set("thumbnail_size", "SIZE_LARGE")
	query.Set("order", order)
	query.Set("share_id", shareID)
	if parentID != "" {
		query.Set("parent_id", parentID)
	}
	if passValue != "" || passKey == "pass_code_token" {
		query.Set(passKey, passValue)
	}

	merged := &ShareListResponse{}
	pageToken := ""
	for {
		pageQuery := cloneValues(query)
		if pageToken != "" {
			pageQuery.Set("page_token", pageToken)
		}

		var resp ShareListResponse
		if err := c.doJSON(ctx, http.MethodGet, driveHost, pathValue, pageQuery, nil, true, &resp); err != nil {
			return nil, err
		}
		if resp.PassCodeToken != "" {
			merged.PassCodeToken = resp.PassCodeToken
		}
		merged.Files = append(merged.Files, resp.Files...)
		if resp.NextPageToken == "" || resp.NextPageToken == pageToken {
			break
		}
		pageToken = resp.NextPageToken
	}
	return merged, nil
}

func (c *Client) RestoreShare(ctx context.Context, shareID, passCodeToken string, fileIDs []string) (*RestoreShareResponse, error) {
	payload := map[string]any{
		"share_id":        shareID,
		"pass_code_token": passCodeToken,
		"file_ids":        fileIDs,
	}
	var resp RestoreShareResponse
	if err := c.doJSON(ctx, http.MethodPost, driveHost, "/drive/v1/share/restore", nil, payload, true, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// WaitForFileDownloadURL 等待文件直链就绪，带轮询和超时控制
// 参考 TelDriveManager 的 wait_for_download_urls 实现
// 分享文件转存后需要等待 PikPak 后台准备直链，可能需要几秒到几十秒
func (c *Client) WaitForFileDownloadURL(ctx context.Context, fileID string, timeout, pollInterval time.Duration) (*FileEntry, error) {
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	if pollInterval <= 0 {
		pollInterval = 3 * time.Second
	}

	opCtx, cancelOp := context.WithTimeout(ctx, timeout)
	defer cancelOp()

	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	var lastErr error
	attempt := 0

	// 立即尝试一次
	attempt++
	requestCtx, cancel := c.requestContext(opCtx)
	file, err := c.GetFile(requestCtx, fileID)
	cancel()
	if err == nil && file.BestDownloadURL() != "" {
		return file, nil
	}
	if err != nil {
		lastErr = err
	}

	// 轮询等待直链就绪
	for {
		select {
		case <-opCtx.Done():
			return nil, opCtx.Err()
		case <-ticker.C:
			if time.Now().After(deadline) {
				errMsg := fmt.Sprintf("等待文件直链超时（%v），尝试次数：%d", timeout, attempt)
				if lastErr != nil {
					errMsg += fmt.Sprintf("，最后错误：%v", lastErr)
				}
				return nil, fmt.Errorf("%s，可能触发 PikPak 风控", errMsg)
			}

			attempt++
			requestCtx, cancel := c.requestContext(opCtx)
			file, err := c.GetFile(requestCtx, fileID)
			cancel()

			if err != nil {
				lastErr = err
				// 继续重试，不立即失败
				continue
			}

			if file.BestDownloadURL() != "" {
				return file, nil
			}
		}
	}
}

func (c *Client) DeleteFiles(ctx context.Context, fileIDs []string) error {
	ids := make([]string, 0, len(fileIDs))
	seen := make(map[string]struct{}, len(fileIDs))
	for _, id := range fileIDs {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return nil
	}

	payload := map[string]any{"ids": ids}
	if err := c.doJSON(ctx, http.MethodPost, driveHost, "/drive/v1/files:batchTrash", nil, payload, true, nil); err != nil {
		return err
	}
	return c.doJSON(ctx, http.MethodPost, driveHost, "/drive/v1/files:batchDelete", nil, payload, true, nil)
}

func (c *Client) ensurePath(ctx context.Context, parts []string) (string, error) {
	parentID := ""
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}

		files, err := c.ListFiles(ctx, parentID)
		if err != nil {
			return "", err
		}

		foundID := ""
		for _, file := range files {
			if file.IsFolder() && file.Name == part {
				foundID = file.ID
				break
			}
		}
		if foundID == "" {
			created, err := c.CreateFolder(ctx, part, parentID)
			if err != nil {
				return "", err
			}
			foundID = created.ID
		}
		parentID = foundID
	}

	return parentID, nil
}

func (c *Client) listFilesWithFilters(ctx context.Context, parentID string, filters map[string]any) ([]FileEntry, error) {
	filterJSON, err := json.Marshal(filters)
	if err != nil {
		return nil, err
	}

	var files []FileEntry
	pageToken := ""
	for {
		query := url.Values{}
		query.Set("thumbnail_size", "SIZE_MEDIUM")
		query.Set("limit", "200")
		query.Set("with_audit", "true")
		query.Set("filters", string(filterJSON))
		if parentID != "" {
			query.Set("parent_id", parentID)
		}
		if pageToken != "" {
			query.Set("page_token", pageToken)
		}

		var resp FileListResponse
		if err := c.doJSON(ctx, http.MethodGet, driveHost, "/drive/v1/files", query, nil, true, &resp); err != nil {
			return nil, err
		}

		files = append(files, resp.Files...)
		if resp.NextPageToken == "" || resp.NextPageToken == pageToken {
			break
		}
		pageToken = resp.NextPageToken
	}

	return files, nil
}

func (c *Client) doJSON(ctx context.Context, method, baseURL, path string, query url.Values, payload any, withAuth bool, out any) error {
	var captchaToken string
	for attempt := 0; attempt < 4; attempt++ {
		if withAuth {
			if err := c.ensureAccessToken(ctx); err != nil {
				return err
			}
		}

		body, err := encodeJSON(payload)
		if err != nil {
			return err
		}

		requestCtx, cancelRequest := c.requestContext(ctx)

		req, err := http.NewRequestWithContext(requestCtx, method, buildURL(baseURL, path, query), body)
		if err != nil {
			cancelRequest()
			return err
		}

		accessToken, userID := "", ""
		if withAuth {
			accessToken, userID = c.authSnapshot()
		}

		headers := c.defaultHeaders(accessToken, captchaToken, userID)
		for key, value := range headers {
			req.Header.Set(key, value)
		}

		respBytes, statusCode, err := c.send(req)
		cancelRequest()
		if err != nil {
			return err
		}

		apiErr, parseErr := decodeAPIError(respBytes, statusCode)
		if parseErr != nil {
			return parseErr
		}
		if apiErr == nil {
			if out == nil || len(bytes.TrimSpace(respBytes)) == 0 {
				return nil
			}
			return json.Unmarshal(respBytes, out)
		}

		if apiErr.Code == 16 && withAuth {
			if err := c.refreshOrLogin(ctx); err != nil {
				return err
			}
			captchaToken = ""
			continue
		}

		if apiErr.Code == 9 {
			token, err := c.initCaptchaForAction(ctx, method, path)
			if err != nil {
				return err
			}
			captchaToken = token
			continue
		}

		return apiErr
	}

	return fmt.Errorf("request retry limit exceeded for %s %s", method, path)
}

func (c *Client) ensureAccessToken(ctx context.Context) error {
	c.authMu.Lock()
	defer c.authMu.Unlock()

	if !c.sessionLoaded {
		_ = c.loadSessionLocked()
		c.sessionLoaded = true
	}

	if c.accessToken != "" && time.Now().Before(c.expiresAt.Add(-90*time.Second)) {
		return nil
	}

	if c.refreshToken != "" {
		if err := c.refreshTokenLocked(ctx); err == nil {
			return c.saveSessionLocked()
		}
	}

	if strings.TrimSpace(c.config.Username) == "" || c.config.Password == "" {
		return fmt.Errorf("login required")
	}

	if err := c.loginLocked(ctx); err != nil {
		return err
	}
	return c.saveSessionLocked()
}

func (c *Client) refreshOrLogin(ctx context.Context) error {
	c.authMu.Lock()
	defer c.authMu.Unlock()

	if c.refreshToken != "" {
		if err := c.refreshTokenLocked(ctx); err == nil {
			return c.saveSessionLocked()
		}
	}

	if strings.TrimSpace(c.config.Username) == "" || c.config.Password == "" {
		return fmt.Errorf("login required")
	}

	if err := c.loginLocked(ctx); err != nil {
		return err
	}
	return c.saveSessionLocked()
}

func (c *Client) authSnapshot() (accessToken, userID string) {
	c.authMu.Lock()
	defer c.authMu.Unlock()
	return c.accessToken, c.userID
}

func (c *Client) loginLocked(ctx context.Context) error {
	if strings.TrimSpace(c.config.Username) == "" || c.config.Password == "" {
		return fmt.Errorf("username or password is empty")
	}

	metaKey := "username"
	if emailPattern.MatchString(c.config.Username) {
		metaKey = "email"
	} else if phonePattern.MatchString(c.config.Username) {
		metaKey = "phone_number"
	}

	loginURL := userHost + "/v1/auth/signin"
	captchaToken, err := c.initCaptcha(ctx, "POST:"+loginURL, map[string]any{
		metaKey: c.config.Username,
	})
	if err != nil {
		return err
	}

	payload := map[string]string{
		"client_id":     clientID,
		"client_secret": clientSecret,
		"username":      c.config.Username,
		"password":      c.config.Password,
		"captcha_token": captchaToken,
	}

	body, err := encodeJSON(payload)
	if err != nil {
		return err
	}

	requestCtx, cancelRequest := c.requestContext(ctx)
	req, err := http.NewRequestWithContext(requestCtx, http.MethodPost, loginURL, body)
	if err != nil {
		cancelRequest()
		return err
	}

	for key, value := range c.defaultHeaders("", "", "") {
		req.Header.Set(key, value)
	}

	respBytes, statusCode, err := c.send(req)
	cancelRequest()
	if err != nil {
		return err
	}

	apiErr, parseErr := decodeAPIError(respBytes, statusCode)
	if parseErr != nil {
		return parseErr
	}
	if apiErr != nil {
		return apiErr
	}

	var tokenResp tokenResponse
	if err := json.Unmarshal(respBytes, &tokenResp); err != nil {
		return err
	}

	c.accessToken = tokenResp.AccessToken
	c.refreshToken = tokenResp.RefreshToken
	c.userID = tokenResp.Sub
	c.expiresAt = time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second)
	return nil
}

func (c *Client) refreshTokenLocked(ctx context.Context) error {
	payload := map[string]string{
		"client_id":     clientID,
		"refresh_token": c.refreshToken,
		"grant_type":    "refresh_token",
	}

	body, err := encodeJSON(payload)
	if err != nil {
		return err
	}

	requestCtx, cancelRequest := c.requestContext(ctx)
	req, err := http.NewRequestWithContext(requestCtx, http.MethodPost, userHost+"/v1/auth/token", body)
	if err != nil {
		cancelRequest()
		return err
	}

	for key, value := range c.defaultHeaders("", "", "") {
		req.Header.Set(key, value)
	}

	respBytes, statusCode, err := c.send(req)
	cancelRequest()
	if err != nil {
		return err
	}

	apiErr, parseErr := decodeAPIError(respBytes, statusCode)
	if parseErr != nil {
		return parseErr
	}
	if apiErr != nil {
		return apiErr
	}

	var tokenResp tokenResponse
	if err := json.Unmarshal(respBytes, &tokenResp); err != nil {
		return err
	}

	c.accessToken = tokenResp.AccessToken
	c.refreshToken = tokenResp.RefreshToken
	c.userID = tokenResp.Sub
	c.expiresAt = time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second)
	return nil
}

func (c *Client) initCaptchaForAction(ctx context.Context, method, path string) (string, error) {
	return c.initCaptcha(ctx, strings.ToUpper(method)+":"+path, nil)
}

func (c *Client) initCaptcha(ctx context.Context, action string, meta map[string]any) (string, error) {
	if meta == nil {
		timestamp := fmt.Sprintf("%d", time.Now().UnixMilli())
		meta = map[string]any{
			"captcha_sign":   captchaSign(c.DeviceID(), timestamp),
			"client_version": clientVersion,
			"package_name":   packageName,
			"user_id":        c.currentUserID(),
			"timestamp":      timestamp,
		}
	}

	payload := map[string]any{
		"client_id": clientID,
		"action":    action,
		"device_id": c.DeviceID(),
		"meta":      meta,
	}

	body, err := encodeJSON(payload)
	if err != nil {
		return "", err
	}

	requestCtx, cancelRequest := c.requestContext(ctx)
	req, err := http.NewRequestWithContext(requestCtx, http.MethodPost, userHost+"/v1/shield/captcha/init", body)
	if err != nil {
		cancelRequest()
		return "", err
	}

	for key, value := range c.defaultHeaders("", "", "") {
		req.Header.Set(key, value)
	}

	respBytes, statusCode, err := c.send(req)
	cancelRequest()
	if err != nil {
		return "", err
	}

	apiErr, parseErr := decodeAPIError(respBytes, statusCode)
	if parseErr != nil {
		return "", parseErr
	}
	if apiErr != nil {
		return "", apiErr
	}

	var resp captchaInitResponse
	if err := json.Unmarshal(respBytes, &resp); err != nil {
		return "", err
	}
	if resp.CaptchaToken == "" {
		return "", fmt.Errorf("empty captcha token")
	}
	return resp.CaptchaToken, nil
}

func (c *Client) send(req *http.Request) ([]byte, int, error) {
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return body, resp.StatusCode, nil
}

func (c *Client) defaultHeaders(accessToken, captchaToken, userID string) map[string]string {
	userAgent := browserUserAgent
	if captchaToken != "" {
		userAgent = buildCustomUserAgent(c.DeviceID(), userID)
	}

	headers := map[string]string{
		"Content-Type": "application/json; charset=utf-8",
		"User-Agent":   userAgent,
		"X-Device-Id":  c.DeviceID(),
	}
	if accessToken != "" {
		headers["Authorization"] = "Bearer " + accessToken
	}
	if captchaToken != "" {
		headers["X-Captcha-Token"] = captchaToken
	}
	return headers
}

func (c *Client) currentUserID() string {
	c.authMu.Lock()
	defer c.authMu.Unlock()
	return c.userID
}

func (c *Client) loadSessionLocked() error {
	var (
		data []byte
		err  error
	)
	if c.config.SessionStore != nil {
		data, err = c.config.SessionStore.Load()
	} else {
		if c.config.SessionFile == "" {
			return os.ErrNotExist
		}
		data, err = os.ReadFile(c.config.SessionFile)
	}
	if err != nil {
		return err
	}

	var session sessionState
	if err := json.Unmarshal(data, &session); err != nil {
		return err
	}

	c.accessToken = session.AccessToken
	c.refreshToken = session.RefreshToken
	c.userID = session.UserID
	c.expiresAt = session.ExpiresAt
	if username := strings.TrimSpace(session.Username); username != "" {
		c.config.Username = username
	}
	if deviceID := strings.TrimSpace(session.DeviceID); deviceID != "" {
		c.deviceID = deviceID
	}
	return nil
}

func (c *Client) saveSessionLocked() error {
	if c.config.SessionStore == nil && c.config.SessionFile == "" {
		return nil
	}

	session := sessionState{
		Username:     strings.TrimSpace(c.config.Username),
		AccessToken:  c.accessToken,
		RefreshToken: c.refreshToken,
		UserID:       c.userID,
		DeviceID:     c.DeviceID(),
		ExpiresAt:    c.expiresAt,
	}

	data, err := json.MarshalIndent(session, "", "  ")
	if err != nil {
		return err
	}
	if c.config.SessionStore != nil {
		return c.config.SessionStore.Save(data)
	}

	dir := filepath.Dir(c.config.SessionFile)
	if dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}

	return os.WriteFile(c.config.SessionFile, data, 0o600)
}

func (c *Client) resetRootFolder() {
	c.rootMu.Lock()
	defer c.rootMu.Unlock()
	c.rootFolderID = ""
}

func buildURL(baseURL, path string, query url.Values) string {
	if len(query) == 0 {
		return baseURL + path
	}
	return baseURL + path + "?" + query.Encode()
}

func cloneValues(values url.Values) url.Values {
	if len(values) == 0 {
		return nil
	}
	cloned := make(url.Values, len(values))
	for key, list := range values {
		cloned[key] = append([]string(nil), list...)
	}
	return cloned
}

func encodeJSON(payload any) (io.Reader, error) {
	if payload == nil {
		return nil, nil
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return bytes.NewReader(data), nil
}

func decodeAPIError(body []byte, statusCode int) (*APIError, error) {
	body = bytes.TrimSpace(body)
	if len(body) == 0 {
		if statusCode >= 200 && statusCode < 300 {
			return nil, nil
		}
		return nil, fmt.Errorf("empty response with status %d", statusCode)
	}

	var envelope apiErrorEnvelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		if statusCode >= 200 && statusCode < 300 {
			return nil, nil
		}
		return nil, fmt.Errorf("unexpected response (%d): %s", statusCode, string(body))
	}

	if envelope.Error == "" && statusCode >= 200 && statusCode < 300 {
		return nil, nil
	}

	message := envelope.ErrorDescription
	if message == "" {
		message = envelope.Error
	}
	if message == "" {
		message = string(body)
	}

	return &APIError{
		Code:    envelope.ErrorCode,
		Message: message,
	}, nil
}

func md5Hex(value string) string {
	sum := md5.Sum([]byte(value))
	return hex.EncodeToString(sum[:])
}

func captchaSign(deviceID, timestamp string) string {
	value := clientID + clientVersion + packageName + deviceID + timestamp
	for _, salt := range captchaSalts {
		value = md5Hex(value + salt)
	}
	return "1." + value
}

func buildCustomUserAgent(deviceID, userID string) string {
	deviceSign := generateDeviceSign(deviceID)
	parts := []string{
		"ANDROID-" + packageName + "/" + clientVersion,
		"protocolVersion/200",
		"accesstype/",
		"clientid/" + clientID,
		"clientversion/" + clientVersion,
		"action_type/",
		"networktype/WIFI",
		"sessionid/",
		"deviceid/" + deviceID,
		"providername/NONE",
		"devicesign/" + deviceSign,
		"refresh_token/",
		"sdkversion/" + sdkVersion,
		fmt.Sprintf("datetime/%d", time.Now().UnixMilli()),
		"usrno/" + userID,
		"appname/" + packageName,
		"session_origin/",
		"grant_type/",
		"appid/",
		"clientip/",
		"devicename/Xiaomi_M2004J7AC",
		"osversion/13",
		"platformversion/10",
		"accessmode/",
		"devicemodel/M2004J7AC",
	}
	return strings.Join(parts, " ")
}

func generateDeviceSign(deviceID string) string {
	base := deviceID + packageName + "1appkey"
	sha := sha1.Sum([]byte(base))
	shaHex := hex.EncodeToString(sha[:])
	return "div101." + deviceID + md5Hex(shaHex)
}
