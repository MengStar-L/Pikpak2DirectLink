package app

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
)

func (s *Server) registerLibraryRoutes() {
	s.mux.HandleFunc("GET /api/library", s.handleLibraryList)
	s.mux.HandleFunc("POST /api/library", s.handleLibraryCreate)
	s.mux.HandleFunc("PATCH /api/library/{id}", s.handleLibraryUpdate)
	s.mux.HandleFunc("DELETE /api/library/{id}", s.handleLibraryDelete)
	s.mux.HandleFunc("POST /api/library/{id}/retry", s.handleLibraryRetry)
	s.mux.HandleFunc("POST /api/library/{id}/files/{fileID}/link", s.handleLibraryLink)
	s.mux.HandleFunc("DELETE /api/library/{id}/files/{fileID}/link", s.handleLibraryRevoke)
}

func (s *Server) libraryEntryView(r *http.Request, entry *LibraryEntry) {
	for i := range entry.Files {
		file := &entry.Files[i]
		if file.Token != "" {
			file.PublicURL = strings.TrimRight(s.baseURL(r), "/") + "/d/" + file.Token
		}
	}
}

func writeLibraryError(w http.ResponseWriter, err error) {
	var invalid libraryValidationError
	switch {
	case errors.Is(err, errLibraryNotFound):
		writeError(w, http.StatusNotFound, errLibraryNotFound.Error())
	case errors.Is(err, errLibraryConflict):
		writeError(w, http.StatusConflict, errLibraryConflict.Error())
	case errors.As(err, &invalid):
		writeError(w, http.StatusBadRequest, invalid.Error())
	default:
		writeError(w, http.StatusInternalServerError, "文件库保存或读取失败")
	}
}

func (s *Server) handleLibraryList(w http.ResponseWriter, r *http.Request) {
	entries, err := s.library.list()
	if err != nil {
		writeLibraryError(w, err)
		return
	}
	for i := range entries {
		s.libraryEntryView(r, &entries[i])
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"entries": entries})
}

func (s *Server) handleLibraryCreate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Kind     string `json:"kind"`
		Name     string `json:"name"`
		ParentID string `json:"parent_id"`
		Input    string `json:"input"`
		PassCode string `json:"pass_code"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	entry := &LibraryEntry{Kind: req.Kind, Name: strings.TrimSpace(req.Name), ParentID: strings.TrimSpace(req.ParentID), Input: strings.TrimSpace(req.Input), PassCode: strings.TrimSpace(req.PassCode)}
	if entry.Kind == "" {
		entry.Kind = "resource"
	}
	if entry.Kind != "resource" && entry.Kind != "folder" {
		writeError(w, http.StatusBadRequest, "类型必须为 folder 或 resource")
		return
	}
	if entry.Kind == "resource" {
		lines := splitResourceLineSpecs(entry.Input)
		if len(lines) != 1 {
			writeError(w, http.StatusBadRequest, "每个资源请输入一个磁力链接或 PikPak 分享链接")
			return
		}
		kind, err := detectResourceKind(lines[0].clean)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		entry.Input, entry.ResourceKind = lines[0].raw, kind
		if kind == ResourceShare {
			_, passCode, err := shareStateAndPassCode(entry.Input, entry.PassCode)
			if err != nil {
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}
			entry.PassCode = passCode
		}
		if entry.Name == "" {
			entry.Name = "分享资源"
			if kind == ResourceMagnet {
				entry.Name = "磁力资源"
				if parsed, err := url.Parse(lines[0].clean); err == nil {
					if name := strings.TrimSpace(parsed.Query().Get("dn")); name != "" && validLibraryName(name) {
						entry.Name = name
					}
				}
			}
		}
	} else {
		entry.Input, entry.PassCode = "", ""
	}
	if err := s.library.create(entry); err != nil {
		writeLibraryError(w, err)
		return
	}
	if entry.Kind == "resource" {
		s.startLibraryDiscovery(entry.ID)
	}
	writeJSON(w, http.StatusCreated, entry)
}

func (s *Server) handleLibraryUpdate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name     *string `json:"name"`
		ParentID *string `json:"parent_id"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Name == nil && req.ParentID == nil {
		writeError(w, http.StatusBadRequest, "请提供名称或目标文件夹")
		return
	}
	entry, err := s.library.update(r.PathValue("id"), req.Name, req.ParentID)
	if err != nil {
		writeLibraryError(w, err)
		return
	}
	s.libraryEntryView(r, entry)
	writeJSON(w, http.StatusOK, entry)
}

func (s *Server) handleLibraryDelete(w http.ResponseWriter, r *http.Request) {
	if err := s.library.remove(r.PathValue("id")); err != nil {
		writeLibraryError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleLibraryRetry(w http.ResponseWriter, r *http.Request) {
	entry, err := s.library.markPending(r.PathValue("id"))
	if err != nil {
		writeLibraryError(w, err)
		return
	}
	s.startLibraryDiscovery(entry.ID)
	writeJSON(w, http.StatusAccepted, entry)
}

func (s *Server) handleLibraryLink(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Mode  string `json:"mode"`
		Reset bool   `json:"reset"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Mode == "" {
		req.Mode = "direct"
	}
	token, err := s.library.link(r.PathValue("id"), r.PathValue("fileID"), req.Mode, req.Reset)
	if err != nil {
		writeLibraryError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]string{"url": strings.TrimRight(s.baseURL(r), "/") + "/d/" + token, "mode": req.Mode})
}

func (s *Server) handleLibraryRevoke(w http.ResponseWriter, r *http.Request) {
	if err := s.library.revokeLink(r.PathValue("id"), r.PathValue("fileID")); err != nil {
		writeLibraryError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
