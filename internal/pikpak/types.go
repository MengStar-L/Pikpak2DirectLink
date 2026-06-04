package pikpak

import (
	"strings"
	"time"
)

type apiErrorEnvelope struct {
	Error            string `json:"error"`
	ErrorCode        int    `json:"error_code"`
	ErrorDescription string `json:"error_description"`
}

type APIError struct {
	Code    int
	Message string
}

func (e *APIError) Error() string {
	return e.Message
}

type sessionState struct {
	Username     string    `json:"username,omitempty"`
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	UserID       string    `json:"user_id"`
	DeviceID     string    `json:"device_id,omitempty"`
	ExpiresAt    time.Time `json:"expires_at"`
}

type SessionStatus struct {
	Ready     bool
	LoggedIn  bool
	Persisted bool
	Username  string
}

type captchaInitResponse struct {
	CaptchaToken string `json:"captcha_token"`
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	Sub          string `json:"sub"`
	ExpiresIn    int64  `json:"expires_in"`
}

type CreateFileResponse struct {
	Task TaskEntry `json:"task"`
	File FileEntry `json:"file"`
}

type ShareListResponse struct {
	PassCodeToken string      `json:"pass_code_token"`
	Files         []FileEntry `json:"files"`
}

type FileListResponse struct {
	NextPageToken string      `json:"next_page_token"`
	Files         []FileEntry `json:"files"`
}

type TaskListResponse struct {
	NextPageToken string      `json:"next_page_token"`
	Tasks         []TaskEntry `json:"tasks"`
}

type TaskEntry struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Type    string `json:"type"`
	Phase   string `json:"phase"`
	Message string `json:"message"`
	FileID  string `json:"file_id"`
}

type DownloadLink struct {
	URL    string `json:"url"`
	Token  string `json:"token"`
	Expire string `json:"expire"`
}

type MediaEntry struct {
	MediaID   string       `json:"media_id"`
	MediaName string       `json:"media_name"`
	Link      DownloadLink `json:"link"`
}

type FileEntry struct {
	Kind            string `json:"kind"`
	ID              string `json:"id"`
	ParentID        string `json:"parent_id"`
	Name            string `json:"name"`
	Size            string `json:"size"`
	FileExtension   string `json:"file_extension"`
	MimeType        string `json:"mime_type"`
	Hash            string `json:"hash"`
	Phase           string `json:"phase"`
	WebContentLink  string `json:"web_content_link"`
	OriginalURL     string `json:"original_url"`
	FolderType      string `json:"folder_type"`
	CreatedTime     string `json:"created_time"`
	ModifiedTime    string `json:"modified_time"`
	ApplicationLink struct {
		ApplicationOctetStream DownloadLink `json:"application/octet-stream"`
	} `json:"links"`
	Medias []MediaEntry `json:"medias"`
}

func (f FileEntry) IsFolder() bool {
	return strings.Contains(strings.ToLower(f.Kind), "folder")
}

func (f FileEntry) BestDownloadURL() string {
	if f.WebContentLink != "" {
		return f.WebContentLink
	}
	if f.ApplicationLink.ApplicationOctetStream.URL != "" {
		return f.ApplicationLink.ApplicationOctetStream.URL
	}
	for _, media := range f.Medias {
		if media.Link.URL != "" {
			return media.Link.URL
		}
	}
	return ""
}

func (f FileEntry) ExpireAt() time.Time {
	for _, value := range []string{
		f.ApplicationLink.ApplicationOctetStream.Expire,
	} {
		if value == "" {
			continue
		}
		if parsed, err := time.Parse(time.RFC3339, value); err == nil {
			return parsed
		}
	}
	for _, media := range f.Medias {
		if media.Link.Expire == "" {
			continue
		}
		if parsed, err := time.Parse(time.RFC3339, media.Link.Expire); err == nil {
			return parsed
		}
	}
	return time.Time{}
}
