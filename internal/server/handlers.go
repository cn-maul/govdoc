package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"mime/multipart"
	"net/http"
	"path"
	"strings"
	"time"

	"govdoc/internal/api"
	"govdoc/internal/assemble"
	"govdoc/internal/baseline"
	"govdoc/internal/config"
	"govdoc/internal/docmodel"
	"govdoc/internal/fonts"
	"govdoc/internal/gbstandard"
	"govdoc/internal/parse"
	"govdoc/internal/render/docx"
	"govdoc/internal/render/preview"
	"govdoc/internal/store"
)

// maxUploadBytes bounds an upload. 公文 are text documents; 32MiB leaves room
// for a heavily illustrated annex while refusing a request that would exhaust
// memory before any validation could run.
const maxUploadBytes = 32 << 20

// ---------------------------------------------------------------- helpers

// writeJSON renders a success response.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if v == nil {
		return
	}
	if err := json.NewEncoder(w).Encode(v); err != nil {
		// The header is already sent, so the only recourse is to log-worthy
		// silence; the client will see a truncated body.
		return
	}
}

// writeError renders the one error shape every failing endpoint uses.
func writeError(w http.ResponseWriter, status int, code, message string, details ...string) {
	writeJSON(w, status, api.ErrorBody{Error: api.ErrorDetail{
		Code: code, Message: message, Details: details,
	}})
}

// writePreviewError renders a failure that the browser will show *inside* the
// preview iframe.
//
// /preview is a document URL, not an API call: the user reaches it from an
// <iframe src> or a "新窗口" link. Answering with the API's JSON error shape
// means the user sees raw escaped JSON instead of a sentence they can act on
// (the reported `{"error":{"code":"not_found",...}}`). The JSON contract is
// unchanged for /api/*, and the status code still carries the machine-readable
// meaning; only the body is made readable for a person.
func writePreviewError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	// Same restrictive policy as a successful preview: the error page contains
	// only escaped user-independent text, but keeping one policy avoids a
	// separate injection surface.
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'")
	w.WriteHeader(status)
	fmt.Fprintf(w, `<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8"><title>预览不可用</title>
<style>
html,body{height:100%%;margin:0}
body{display:flex;align-items:center;justify-content:center;background:#f6f7f9;color:#1f2328;
font:14px/1.7 system-ui,"Segoe UI","Microsoft YaHei",sans-serif;padding:24px;box-sizing:border-box}
.card{max-width:34rem;width:100%%;background:#fff;border:1px solid #e3e6ea;border-radius:16px;
padding:24px 26px;box-shadow:0 1px 2px rgba(0,0,0,.04),0 12px 32px rgba(0,0,0,.06)}
h1{margin:0 0 8px;font-size:16px}
p{margin:0;color:#4a5159;white-space:pre-wrap;word-break:break-word}
.hint{margin-top:12px;color:#6b7280;font-size:13px}
</style></head>
<body><div class="card">
<h1>预览不可用</h1>
<p>%s</p>
<p class="hint">请回到 GovDoc 页面重新分析或重新上传文档。</p>
</div></body></html>`, html.EscapeString(message))
}

// statusFor maps a store error to an HTTP status.
func statusFor(err error) int {
	if errors.Is(err, store.ErrNotFound) {
		return http.StatusNotFound
	}
	return http.StatusInternalServerError
}

func writeStoreError(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, api.CodeNotFound, err.Error())
		return
	}
	writeError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
}

// decodeBody reads a JSON body, rejecting unknown fields so a typo in the
// client is reported instead of silently ignored.
func decodeBody(w http.ResponseWriter, r *http.Request, dst any) bool {
	defer r.Body.Close()
	dec := json.NewDecoder(io.LimitReader(r.Body, 4<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		writeError(w, http.StatusBadRequest, api.CodeBadRequest,
			fmt.Sprintf("请求内容无法解析：%v", err))
		return false
	}
	return true
}

// currentSpec returns the spec of the currently selected profile, with the live
// 行文方向 applied on top.
//
// It is the server's default formatting, used where no document context exists
// (the settings screen's live preview, GET /api/spec). Documents do NOT use
// this: they use specForSession, so that changing the default profile cannot
// reflow a document that was already processed under a different one.
//
// 行文方向 is deliberately an overlay rather than part of a profile's stored
// rules. It describes the document being written (who it is addressed to), not
// the format, and the user changes it per document. Storing it inside each
// profile as well created two sources of truth that drifted apart: the value
// set in the settings never reached the profile, so the settings screen and the
// rendered output disagreed about the same document.
func (s *Server) currentSpec() gbstandard.Spec {
	s.mu.RLock()
	active := s.activeProfile
	profiles := s.profiles
	direction := s.spec.Direction
	fallback := s.spec
	s.mu.RUnlock()

	spec := fallback
	for _, p := range profiles {
		if p.ID != active {
			continue
		}
		if decoded, err := p.Decode(); err == nil {
			spec = decoded
			break
		}
	}
	if direction != "" {
		spec.Direction = direction
	}
	return spec
}

// ListProfiles returns the available profiles in display order.
func (s *Server) ListProfiles() []config.Profile {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]config.Profile, len(s.profiles))
	copy(out, s.profiles)
	return out
}

// ActiveProfile returns the id applied to new uploads.
func (s *Server) ActiveProfile() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.activeProfile == "" {
		return config.ProfileGovDoc
	}
	return s.activeProfile
}

// SetActiveProfile chooses the profile for new uploads.
//
// An unknown id is refused rather than ignored: silently falling back to 公文
// would produce a document formatted under rules the user did not choose, and
// the mistake would not surface until they opened the file.
func (s *Server) SetActiveProfile(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range s.profiles {
		if p.ID == id {
			s.activeProfile = id
			return nil
		}
	}
	return fmt.Errorf("未知配置 %q", id)
}

// ProfileSpec decodes a profile's rules by id.
func (s *Server) ProfileSpec(id string) (gbstandard.Spec, error) {
	s.mu.RLock()
	profiles := s.profiles
	fallback := s.spec
	s.mu.RUnlock()

	for _, p := range profiles {
		if p.ID != id {
			continue
		}
		if spec, err := p.Decode(); err == nil {
			return spec, nil
		}
	}
	if id == "" {
		return fallback, nil
	}
	return gbstandard.Spec{}, fmt.Errorf("未知配置 %q", id)
}

// specForSession returns the spec a document must be rendered with.
//
// It deliberately ignores the currently selected profile: a document remembers
// the one it was uploaded with, so changing the default later cannot reflow an
// already-processed document under different rules. That silent change is
// exactly what a profile system exists to prevent.
//
// The live 行文方向 is re-applied on top, because it is a per-document choice
// the user may change at any time and is not part of the profile's identity.
func (s *Server) specForSession(sess *store.Session) (gbstandard.Spec, error) {
	id := sess.ProfileID
	if id == "" {
		id = s.ActiveProfile()
	}
	spec, err := s.ProfileSpec(id)
	if err != nil {
		// A profile deleted between upload and export must not strand the
		// document; fall back to the default and say so, because the user needs
		// to know the output was formatted under different rules.
		s.log.Warn("文档所用配置已不存在，回退到默认配置", "doc", sess.ID, "profile", id)
		spec = gbstandard.Default()
	}
	if sess.Direction != "" && isKnownDirection(sess.Direction) {
		spec.Direction = sess.Direction
	}
	return spec, nil
}

// currentLLM returns the active model configuration.
func (s *Server) currentLLM() LLMConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.llm
}

// currentAllowEdits reports whether the user permits content corrections.
func (s *Server) currentAllowEdits() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.allowEdits
}

// settingsPath returns the settings file path, or "" when persistence is off.
func (s *Server) settingsPath() string {
	if s.cfg.Settings == nil {
		return ""
	}
	return s.cfg.Settings.Path()
}

// settingsDisabled reports whether settings cannot be persisted.
func (s *Server) settingsDisabled() bool {
	if s.cfg.Settings == nil {
		return true
	}
	return s.cfg.Settings.Disabled()
}

// ---------------------------------------------------------------- health/config

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, api.Health{
		Status:        "ok",
		Version:       Version,
		LLMConfigured: s.currentLLM().configured(),
	})
}

func (s *Server) handleGetConfig(w http.ResponseWriter, r *http.Request) {
	llm := s.currentLLM()
	resp := api.ConfigResponse{
		LLM: api.LLMConfig{
			Endpoint: llm.Endpoint,
			Model:    llm.Model,
			Protocol: llm.Protocol,
			// The key itself is never echoed. A read-only viewer must not be
			// able to recover the credential the user pasted in.
			APIKeySet: strings.TrimSpace(llm.APIKey) != "",
		},
		Direction:  s.currentSpec().Direction,
		Spec:       s.currentSpec(),
		AllowEdits: s.currentAllowEdits(),
		// The settings page needs the profile list to render, and carrying it
		// here means one request populates the whole screen.
		ActiveProfile: s.ActiveProfile(),
		Profiles:      s.toAPIProfiles(),
		Persistent:    !s.settingsDisabled(),
	}
	// saved_to_disk answers "are my settings already stored?" so the UI can
	// word its reassurance accurately: a first run has nothing saved yet, while
	// a configured install does.
	if !s.settingsDisabled() {
		resp.SavedToDisk = s.cfg.Settings.Exists()
	}
	// Telling the user *where* settings live makes "it works after a restart"
	// something they can verify rather than take on faith.
	if p := s.settingsPath(); p != "" {
		resp.SettingsPath = p
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handlePutConfig(w http.ResponseWriter, r *http.Request) {
	var req api.ConfigRequest
	if !decodeBody(w, r, &req) {
		return
	}

	s.mu.Lock()
	// Rebuilding the model client is only meaningful when a connection field
	// changes. Direction and allow_edits are unrelated preferences, and
	// rebuilding for them would fail on an install that has not configured a
	// model yet — making "其他设置" unsavable before the model is set up.
	connectionChanged := req.Endpoint != nil || req.Model != nil ||
		req.Protocol != nil || req.APIKey != nil
	if req.Endpoint != nil {
		s.llm.Endpoint = strings.TrimSpace(*req.Endpoint)
	}
	if req.Model != nil {
		s.llm.Model = strings.TrimSpace(*req.Model)
	}
	if req.Protocol != nil {
		p := strings.TrimSpace(*req.Protocol)
		switch p {
		case "", "openai-chat", "openai-responses", "anthropic":
			s.llm.Protocol = p
		default:
			s.mu.Unlock()
			writeError(w, http.StatusBadRequest, api.CodeBadRequest,
				fmt.Sprintf("不支持的协议 %q，可选：openai-chat、openai-responses、anthropic", p))
			return
		}
	}
	if req.APIKey != nil {
		s.llm.APIKey = strings.TrimSpace(*req.APIKey)
	}
	if req.Direction != nil {
		switch *req.Direction {
		case gbstandard.DirDown, gbstandard.DirUp, gbstandard.DirParallel:
			s.spec.Direction = *req.Direction
		default:
			s.mu.Unlock()
			writeError(w, http.StatusBadRequest, api.CodeBadRequest,
				fmt.Sprintf("不支持的行文方向 %q", *req.Direction))
			return
		}
	}
	if req.AllowEdits != nil {
		s.allowEdits = *req.AllowEdits
	}
	llm := s.llm
	dir := s.spec.Direction
	allow := s.allowEdits
	s.mu.Unlock()

	// Changing the model connection must reach the analyzer, which owns the
	// SDK client; otherwise a saved key would only take effect after a
	// restart, which looks like the save silently failed.
	if a, ok := s.analyzer.(interface{ Reconfigure(LLMConfig) error }); ok && connectionChanged {
		if err := a.Reconfigure(llm); err != nil {
			writeError(w, http.StatusBadRequest, api.CodeBadRequest,
				fmt.Sprintf("模型配置无效：%v", err))
			return
		}
	}

	// Persist after the change is live. A save failure is reported in the
	// response but does not undo the setting: the user asked for it to apply,
	// and it does — only its survival across restarts is in question.
	saveErr := s.persistSettings()

	s.log.Info("config updated",
		"endpoint", llm.Endpoint, "model", llm.Model,
		"protocol", llm.Protocol, "key_set", llm.APIKey != "",
		"direction", dir, "allow_edits", allow,
		"saved", saveErr == nil)

	resp := api.ConfigResponse{
		LLM: api.LLMConfig{
			Endpoint: llm.Endpoint, Model: llm.Model, Protocol: llm.Protocol,
			APIKeySet: llm.APIKey != "",
		},
		Direction:     dir,
		Spec:          s.currentSpec(),
		AllowEdits:    allow,
		ActiveProfile: s.ActiveProfile(),
		Profiles:      s.toAPIProfiles(),
		Persistent:    !s.settingsDisabled(),
		SettingsPath:  s.settingsPath(),
		SavedToDisk:   saveErr == nil,
	}
	// The two failure modes need different words, and conflating them produces
	// a misleading message:
	//
	//   - persistence is unavailable (no config dir): nothing was attempted, so
	//     "写入失败" would blame a write that never happened. The UI should say
	//     "仅本次运行有效" instead.
	//   - persistence was attempted and failed (bad permissions, read-only
	//     disk): the setting IS live for this run, and the user needs to know it
	//     will not survive a restart.
	if s.settingsDisabled() {
		resp.SaveWarning = ""
	} else if saveErr != nil {
		resp.SaveWarning = "设置已在本次运行中生效，但写入配置文件失败：" + saveErr.Error() +
			"（重启后需要重新填写）"
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleListModels probes the model catalog of a connection the user is still
// editing. POST is used rather than GET so a temporary API key never lands in
// a URL, and therefore never in server or proxy access logs.
func (s *Server) handleListModels(w http.ResponseWriter, r *http.Request) {
	var req api.ModelListRequest
	if !decodeBody(w, r, &req) {
		return
	}

	endpoint := strings.TrimSpace(req.Endpoint)
	if endpoint == "" {
		writeError(w, http.StatusBadRequest, api.CodeBadRequest, "请先填写接口地址。")
		return
	}
	protocol := strings.TrimSpace(req.Protocol)
	switch protocol {
	case "", "openai-chat", "openai-responses", "anthropic":
	default:
		writeError(w, http.StatusBadRequest, api.CodeBadRequest,
			fmt.Sprintf("不支持的协议 %q，可选：openai-chat、openai-responses、anthropic", protocol))
		return
	}

	current := s.currentLLM()
	key := strings.TrimSpace(req.APIKey)
	// A saved key belongs to the saved endpoint. Reusing it after the user
	// changes the address would send the credential to a host it was never
	// meant for, so a new address requires the key to be entered again.
	if key == "" && sameEndpoint(current.Endpoint, endpoint) {
		key = strings.TrimSpace(current.APIKey)
	}
	if key == "" {
		writeError(w, http.StatusBadRequest, api.CodeBadRequest,
			"请先填写 API Key；已保存的 Key 只在接口地址未改变时复用。")
		return
	}

	s.mu.RLock()
	lister, ok := s.analyzer.(ModelLister)
	s.mu.RUnlock()
	if !ok {
		writeError(w, http.StatusNotImplemented, api.CodeInternal,
			"当前运行环境不支持获取模型列表。")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	models, err := lister.ListModels(ctx, LLMConfig{
		Endpoint: endpoint,
		APIKey:   key,
		Protocol: protocol,
		Timeout:  current.Timeout,
	})
	if err != nil {
		writeError(w, http.StatusBadGateway, api.CodeUpstream,
			fmt.Sprintf("获取模型列表失败：%v", err))
		return
	}
	if models == nil {
		models = []string{}
	}
	writeJSON(w, http.StatusOK, api.ModelListResponse{Models: models})
}

// sameEndpoint compares connection addresses without trailing slashes, which
// are insignificant here and are a common copy/paste difference.
func sameEndpoint(a, b string) bool {
	return strings.TrimRight(strings.TrimSpace(a), "/") ==
		strings.TrimRight(strings.TrimSpace(b), "/")
}

func (s *Server) handleSpec(w http.ResponseWriter, r *http.Request) {
	spec := s.currentSpec()
	statuses := fonts.Check(spec)

	out := make([]api.FontStatus, 0, len(statuses))
	for _, st := range statuses {
		out = append(out, api.FontStatus{
			Name: st.Name, Available: st.Available,
			Fallbacks: st.Fallbacks, Used: st.Used,
		})
	}
	writeJSON(w, http.StatusOK, api.SpecResponse{
		Spec:     spec,
		Fonts:    out,
		Problems: gbstandard.ValidateSpec(spec),
	})
}

// ---------------------------------------------------------------- upload

func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadBytes)
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		writeError(w, http.StatusBadRequest, api.CodeBadRequest,
			fmt.Sprintf("上传内容无法解析：%v（单个文件上限 %d MiB）", err, maxUploadBytes>>20))
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, api.CodeBadRequest,
			"缺少上传文件：请以 multipart/form-data 提交，字段名为 file。")
		return
	}
	defer file.Close()

	data, err := readAllLimited(file, maxUploadBytes)
	if err != nil {
		writeError(w, http.StatusBadRequest, api.CodeBadRequest, err.Error())
		return
	}
	if len(data) == 0 {
		writeError(w, http.StatusBadRequest, api.CodeBadRequest, "上传的文件为空。")
		return
	}

	name := sanitizeName(header.Filename)
	doc, err := parse.Parse(name, data)
	if err != nil {
		writeError(w, http.StatusBadRequest, api.CodeParseFailed,
			fmt.Sprintf("无法解析该文档：%v", err))
		return
	}

	id := newID()
	s.store.PutSession(id, doc)

	// The profile is chosen here and remembered with the document, so that
	// changing the default later does not reformat this file under different
	// rules. The form field is optional: an absent or unknown value falls back
	// to the current default rather than failing the upload, because refusing to
	// accept a document over a formatting preference would be the wrong
	// trade-off. The applied id is echoed in the response so the UI can show
	// which rules this document will use.
	profileID := strings.TrimSpace(r.FormValue("profile"))
	if profileID == "" {
		profileID = s.ActiveProfile()
	}
	if _, err := s.ProfileSpec(profileID); err != nil {
		s.log.Warn("上传时指定的配置不存在，改用默认配置",
			"doc", id, "profile", profileID)
		profileID = s.ActiveProfile()
	}
	_, _ = s.store.UpdateSession(id, func(sess *store.Session) {
		sess.ProfileID = profileID
		if sess.Direction == "" {
			sess.Direction = s.currentSpec().Direction
		}
	})

	// The response carries a light node view: enough for the source panel to
	// render immediately, without the image payloads the nodes may hold.
	summaries := make([]api.NodeSummary, 0, len(doc.Nodes))
	for _, n := range doc.Nodes {
		summaries = append(summaries, api.NodeSummary{
			ID: n.ID, Kind: n.Kind, Level: n.Level, Text: n.Text,
			Order: n.Order, SourceStyle: n.SourceStyle, SourcePage: n.SourcePage,
			HasImage: n.Image != nil, TableCells: len(n.Rows),
		})
	}

	s.log.Info("document uploaded",
		"id", id, "name", name, "format", doc.Format,
		"nodes", len(doc.Nodes), "content", len(doc.ContentIDs()))

	writeJSON(w, http.StatusOK, api.UploadResponse{
		DocID:        id,
		SourceName:   doc.SourceName,
		Format:       doc.Format,
		NodeCount:    len(doc.Nodes),
		ContentCount: len(doc.ContentIDs()),
		// Echoed so the UI can show which rules this document will use — and so
		// an unknown profile the server fell back from is visible rather than
		// silent.
		ProfileID: profileID,
		Nodes:     summaries,
	})
}

func (s *Server) handleGetDocument(w http.ResponseWriter, r *http.Request) {
	sess, err := s.store.Session(r.PathValue("id"))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, api.DocumentResponse{DocID: sess.ID, Doc: sess.Doc})
}

// handleGetImage serves one embedded picture. Images are fetched by node id
// rather than inlined into the document JSON, so a图-heavy upload does not
// produce a multi-megabyte JSON response the browser must parse.
func (s *Server) handleGetImage(w http.ResponseWriter, r *http.Request) {
	sess, err := s.store.Session(r.PathValue("id"))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	nodeID := r.PathValue("nodeID")
	n, ok := sess.Doc.Node(nodeID)
	if !ok || n.Image == nil || len(n.Image.Data) == 0 {
		writeError(w, http.StatusNotFound, api.CodeNotFound,
			fmt.Sprintf("节点 %s 没有可显示的图片。", nodeID))
		return
	}

	mime := n.Image.MimeType
	if mime == "" {
		mime = http.DetectContentType(n.Image.Data)
	}
	w.Header().Set("Content-Type", mime)
	w.Header().Set("Cache-Control", "private, max-age=3600")
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(n.Image.Data); err != nil {
		s.log.Warn("write image failed", "node", nodeID, "err", err.Error())
	}
}

// ---------------------------------------------------------------- plan

func (s *Server) handleStartPlan(w http.ResponseWriter, r *http.Request) {
	sess, err := s.store.Session(r.PathValue("id"))
	if err != nil {
		writeStoreError(w, err)
		return
	}

	// An empty body is allowed: the client may start analysis with defaults.
	var req api.PlanRequest
	if r.ContentLength > 0 {
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			writeError(w, http.StatusBadRequest, api.CodeBadRequest, "请求内容无法读取。")
			return
		}
		if len(strings.TrimSpace(string(body))) > 0 {
			if err := json.Unmarshal(body, &req); err != nil {
				writeError(w, http.StatusBadRequest, api.CodeBadRequest,
					fmt.Sprintf("请求内容无法解析：%v", err))
				return
			}
		}
	}

	// A structure derived from the document itself needs no model, so it is
	// served before the configured() gate. Otherwise a user with no API key
	// could not produce anything at all, even from a document that already
	// numbers its own chapters.
	if req.Baseline {
		plan := baseline.Build(sess.Doc)
		report := docmodel.VerifyPlan(sess.Doc, plan)
		_, _ = s.store.UpdateSession(sess.ID, func(sess *store.Session) {
			sess.Plan, sess.Report = plan, report
		})
		jobID := newID()
		job := s.store.NewJob(jobID, sess.ID)
		job.Finish(plan, report, nil)
		writeJSON(w, http.StatusAccepted, api.PlanJobResponse{JobID: jobID})
		return
	}

	cfg := s.currentLLM()
	if !cfg.configured() {
		writeError(w, http.StatusBadRequest, api.CodeNoLLMConfig,
			"尚未配置模型连接：请先填写接口地址、模型名称与 API Key。")
		return
	}

	s.mu.RLock()
	analyzer := s.analyzer
	s.mu.RUnlock()
	if analyzer == nil {
		writeError(w, http.StatusServiceUnavailable, api.CodeInternal,
			"分析器未初始化。")
		return
	}

	// 行文方向 may be overridden per document.
	if req.Direction != "" {
		switch req.Direction {
		case gbstandard.DirDown, gbstandard.DirUp, gbstandard.DirParallel:
			_, _ = s.store.UpdateSession(sess.ID, func(sess *store.Session) {
				sess.Direction = req.Direction
			})
		default:
			writeError(w, http.StatusBadRequest, api.CodeBadRequest,
				fmt.Sprintf("不支持的行文方向 %q", req.Direction))
			return
		}
	}

	// Whether the model may correct content. The per-request value wins; a nil
	// means "use the saved preference", which is what the UI sends when the
	// user has not touched the switch.
	allowEdits := s.currentAllowEdits()
	if req.AllowEdits != nil {
		allowEdits = *req.AllowEdits
	}

	jobID := newID()
	job := s.store.NewJob(jobID, sess.ID)

	// The analysis runs detached from the request: the POST returns as soon
	// as the job exists, and the client follows the SSE stream. Binding it to
	// the request context would cancel the work the moment a proxy or a
	// browser gave up on the (short) POST.
	go s.runAnalysis(job, sess, analyzer, AnalysisOptions{
		Hint:       req.Hint,
		AllowEdits: allowEdits,
	})

	writeJSON(w, http.StatusAccepted, api.PlanJobResponse{JobID: jobID})
}

// AnalysisOptions carries the per-request analysis settings.
type AnalysisOptions struct {
	// Hint is the user's note about the document's nature.
	Hint string
	// AllowEdits permits the model to correct content, always through
	// declared edits.
	AllowEdits bool
}

// runAnalysis drives the analyzer and publishes progress. Every exit path
// settles the job, so a client can never be left waiting on a dead stream.
func (s *Server) runAnalysis(job *store.Job, sess *store.Session, analyzer Analyzer, opts AnalysisOptions) {
	defer func() {
		if v := recover(); v != nil {
			s.log.Error("analysis panicked", "doc", sess.ID, "panic", fmt.Sprint(v))
			job.Finish(nil, nil, fmt.Errorf("内部错误：%v", v))
		}
	}()

	progress := func(p Progress) {
		job.Publish(store.StageUpdate{Name: p.Name, Message: p.Message, Done: p.Done})
	}
	partial := func(p PartialProgress) {
		job.Publish(store.StageUpdate{
			Kind: "partial", Plan: p.Plan, Edits: p.Edits, Reset: p.Reset,
		})
	}

	ctx, cancel := analysisContext()
	defer cancel()

	plan, err := analyzer.Analyze(ctx, sess.Doc, opts, progress, partial)
	if err != nil {
		// A partial plan is still useful: the failure may have come from the
		// self-repair loop giving up, in which case showing the user what the
		// model produced (and why it was rejected) is far better than a blank
		// screen.
		if plan != nil {
			report := docmodel.VerifyPlan(sess.Doc, plan)
			_, _ = s.store.UpdateSession(sess.ID, func(sess *store.Session) {
				sess.Plan, sess.Report = plan, report
			})
			s.log.Warn("analysis failed with partial plan",
				"doc", sess.ID, "err", err.Error())
			job.Finish(plan, report, err)
			return
		}
		s.log.Error("analysis failed", "doc", sess.ID, "err", err.Error())
		job.Finish(nil, nil, err)
		return
	}

	// Verification happens here, at the single choke point, so an analyzer
	// cannot declare its own output renderable.
	report := docmodel.VerifyPlan(sess.Doc, plan)
	_, _ = s.store.UpdateSession(sess.ID, func(sess *store.Session) {
		sess.Plan, sess.Report = plan, report
	})
	s.log.Info("analysis complete",
		"doc", sess.ID, "ok", report.OK(),
		"covered", report.CoveredNodes, "total", report.ContentNodes)
	job.Finish(plan, report, nil)
}

func (s *Server) handleGetPlan(w http.ResponseWriter, r *http.Request) {
	sess, err := s.store.Session(r.PathValue("id"))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if sess.Plan == nil {
		writeError(w, http.StatusNotFound, api.CodeNotFound,
			"该文档尚未生成结构方案。")
		return
	}
	writeJSON(w, http.StatusOK, api.PlanResponse{
		Plan:      sess.Plan,
		Report:    sess.Report,
		Structure: baseline.Compare(sess.Doc, baseline.Build(sess.Doc), sess.Plan),
	})
}

// handlePutPlan stores a user-edited plan and returns a fresh verdict.
//
// An invalid plan is stored deliberately. The 校对 workflow depends on the
// user being able to save intermediate states and see which paragraphs are
// still missing; refusing to store them would force a correct edit to be made
// in one shot.
func (s *Server) handlePutPlan(w http.ResponseWriter, r *http.Request) {
	sess, err := s.store.Session(r.PathValue("id"))
	if err != nil {
		writeStoreError(w, err)
		return
	}

	var plan docmodel.Plan
	if !decodeBody(w, r, &plan) {
		return
	}

	report := docmodel.VerifyPlan(sess.Doc, &plan)
	_, _ = s.store.UpdateSession(sess.ID, func(sess *store.Session) {
		sess.Plan, sess.Report = &plan, report
	})

	writeJSON(w, http.StatusOK, api.PlanResponse{
		Plan: &plan, Report: report,
		Structure: baseline.Compare(sess.Doc, baseline.Build(sess.Doc), &plan),
	})
}

// handleRevise turns one chat instruction into declared edits.
//
// It never changes the stored plan. The user accepts or rejects each edit in
// the review list, which is what makes "同意就覆盖、不同意就不覆盖" literally
// true: the preview and the export only change when PUT /plan carries the
// accepted edit.
func (s *Server) handleRevise(w http.ResponseWriter, r *http.Request) {
	sess, err := s.store.Session(r.PathValue("id"))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if sess.Doc == nil {
		writeError(w, http.StatusBadRequest, api.CodeBadRequest, "文档内容不可用。")
		return
	}

	var req api.ReviseRequest
	if !decodeBody(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Instruction) == "" {
		writeError(w, http.StatusBadRequest, api.CodeBadRequest, "请输入修改要求。")
		return
	}
	if req.NodeID != "" {
		if _, ok := sess.Doc.Node(req.NodeID); !ok {
			writeError(w, http.StatusBadRequest, api.CodeBadRequest,
				fmt.Sprintf("选中的段落 %q 不存在。", req.NodeID))
			return
		}
	}

	cfg := s.currentLLM()
	if !cfg.configured() {
		writeError(w, http.StatusBadRequest, api.CodeNoLLMConfig,
			"尚未配置模型连接：请先填写接口地址、模型名称与 API Key。")
		return
	}

	s.mu.RLock()
	reviser, ok := s.analyzer.(Reviser)
	s.mu.RUnlock()
	if !ok {
		writeError(w, http.StatusNotImplemented, api.CodeInternal,
			"当前运行环境不支持对话修改。")
		return
	}

	allowEdits := s.currentAllowEdits()
	if req.AllowEdits != nil {
		allowEdits = *req.AllowEdits
	}
	if !allowEdits {
		writeError(w, http.StatusBadRequest, api.CodeBadRequest,
			"当前设置不允许修改原文。请先在「其他设置」中允许模型修正文字。")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 120*time.Second)
	defer cancel()

	res, err := reviser.Revise(ctx, sess.Doc, RevisionRequest{
		Instruction: strings.TrimSpace(req.Instruction),
		Selection:   req.Selection,
		NodeID:      req.NodeID,
		AllowEdits:  allowEdits,
	})
	if err != nil {
		writeError(w, http.StatusBadGateway, api.CodeUpstream,
			fmt.Sprintf("对话修改失败：%v", err))
		return
	}

	// Guard the invariant the rest of the program depends on: every edit must
	// name a real node and a Before that occurs in it. A paraphrased target
	// would create a review item that can never take effect.
	valid := make([]docmodel.Edit, 0, len(res.Edits))
	for _, e := range res.Edits {
		if strings.TrimSpace(e.NodeID) == "" || strings.TrimSpace(e.Before) == "" {
			continue
		}
		node, ok := sess.Doc.Node(e.NodeID)
		if !ok {
			continue
		}
		if req.NodeID != "" && e.NodeID != req.NodeID {
			continue
		}
		if !strings.Contains(node.Text, e.Before) {
			continue
		}
		valid = append(valid, e)
	}
	if len(res.Edits) > 0 && len(valid) == 0 {
		writeError(w, http.StatusBadGateway, api.CodeUpstream,
			"模型提出的改动无法对应到原文，未添加到校对项。请换一种说法再试。")
		return
	}
	writeJSON(w, http.StatusOK, api.ReviseResponse{Edits: valid, Reply: res.Reply})
}

// ---------------------------------------------------------------- render

func (s *Server) handleRender(w http.ResponseWriter, r *http.Request) {
	sess, err := s.store.Session(r.PathValue("id"))
	if err != nil {
		writeStoreError(w, err)
		return
	}

	var req api.RenderRequest
	if r.ContentLength > 0 {
		if !decodeBody(w, r, &req) {
			return
		}
	}
	if req.Format == "" {
		req.Format = "docx"
	}
	if req.Format != "docx" {
		writeError(w, http.StatusBadRequest, api.CodeBadRequest,
			fmt.Sprintf("暂不支持导出为 %q；当前仅支持 docx。", req.Format))
		return
	}

	plan := sess.Plan
	if req.Plan != nil {
		plan = req.Plan
	}
	if plan == nil {
		writeError(w, http.StatusBadRequest, api.CodeInvalidPlan,
			"尚无结构方案，无法导出。请先执行分析。")
		return
	}

	// The document's own profile, not the currently selected one: switching the
	// default must not reformat a document that was already processed.
	spec, err := s.specForSession(sess)
	if err != nil {
		writeError(w, http.StatusBadRequest, api.CodeBadRequest, err.Error())
		return
	}

	// assemble.Build re-verifies the plan and refuses a lossy one, so a
	// hand-edited plan that dropped a paragraph cannot be exported even if
	// the client skipped its own check.
	res, err := assemble.Build(sess.Doc, plan, spec)
	if err != nil {
		var report *docmodel.Report
		var details []string
		if res != nil && res.Report != nil {
			report = res.Report
			for _, d := range report.Errors() {
				details = append(details, d.Message)
			}
		}
		s.log.Warn("render refused", "doc", sess.ID, "err", err.Error())
		writeJSON(w, http.StatusBadRequest, api.ErrorBody{Error: api.ErrorDetail{
			Code:    api.CodeInvalidPlan,
			Message: err.Error(),
			Details: details,
		}})
		_ = report
		return
	}

	data, err := docx.Render(res.Doc, spec)
	if err != nil {
		s.log.Error("docx render failed", "doc", sess.ID, "err", err.Error())
		writeError(w, http.StatusInternalServerError, api.CodeRenderFailed,
			fmt.Sprintf("生成公文文档失败：%v", err))
		return
	}

	fileID := newID()
	filename := outputName(sess.Doc.SourceName, plan)
	s.store.PutFile(&store.File{
		ID:       fileID,
		Filename: filename,
		MIME:     docxMIME,
		Data:     data,
	})
	_, _ = s.store.UpdateSession(sess.ID, func(sess *store.Session) {
		sess.Plan, sess.Report, sess.SynthText = plan, res.Report, res.SynthText
	})

	s.log.Info("rendered", "doc", sess.ID, "file", fileID,
		"bytes", len(data), "synth", len(res.SynthText))

	writeJSON(w, http.StatusOK, api.RenderResponse{
		FileID:    fileID,
		Filename:  filename,
		Size:      len(data),
		Report:    res.Report,
		Diff:      res.Diff,
		SynthText: res.SynthText,
	})
}

// handleDownload serves a rendered file. Content-Disposition uses the RFC 5987
// UTF-8 form because 公文 file names are Chinese, and a bare filename= would
// reach the browser as mojibake.
func (s *Server) handleDownload(w http.ResponseWriter, r *http.Request) {
	f, err := s.store.File(r.PathValue("fileID"))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	w.Header().Set("Content-Type", f.MIME)
	w.Header().Set("Content-Length", fmt.Sprint(len(f.Data)))
	w.Header().Set("Content-Disposition", contentDisposition(f.Filename))
	w.Header().Set("Cache-Control", "private, no-store")
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(f.Data); err != nil {
		s.log.Warn("download interrupted", "file", f.ID, "err", err.Error())
	}
}

func (s *Server) handlePreview(w http.ResponseWriter, r *http.Request) {
	sess, err := s.store.Session(r.PathValue("id"))
	if err != nil {
		// 这条分支通常就是「服务重启后旧标签页还开着」：文档已不在内存里。
		// 它同样需要一个可读页面，而不是无法操作的 JSON。
		if errors.Is(err, store.ErrNotFound) {
			writePreviewError(w, http.StatusNotFound, err.Error())
			return
		}
		writePreviewError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if sess.Plan == nil {
		// 预览是直接在 iframe 里打开的，返回 JSON 会让用户看到一屏转义字符。
		// 这里给出可读的 HTML，并将状态码保持为 404（语义仍是「还没有方案」）。
		writePreviewError(w, http.StatusNotFound, "该文档尚未生成结构方案。")
		return
	}

	// The document's own profile, not the currently selected one: switching the
	// default must not reformat a document that was already processed.
	spec, err := s.specForSession(sess)
	if err != nil {
		writeError(w, http.StatusBadRequest, api.CodeBadRequest, err.Error())
		return
	}

	res, err := assemble.Build(sess.Doc, sess.Plan, spec)
	if err != nil {
		writePreviewError(w, http.StatusBadRequest,
			fmt.Sprintf("结构方案未通过校验，无法预览：%v", err))
		return
	}
	html, err := preview.Render(res.Doc, spec)
	if err != nil {
		writePreviewError(w, http.StatusInternalServerError,
			fmt.Sprintf("生成预览失败：%v", err))
		return
	}

	// The preview is self-contained HTML served inside a sandboxed iframe.
	// A restrictive CSP means a bug in the preview renderer cannot execute
	// script in the user's session even if the document text contained some.
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy",
		"default-src 'none'; style-src 'unsafe-inline'; img-src data:; font-src 'none'")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(html)
}

const docxMIME = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"

// contentDisposition builds a header that survives a Chinese file name.
func contentDisposition(name string) string {
	ascii := strings.Map(func(r rune) rune {
		if r < 32 || r > 126 || r == '"' || r == '\\' {
			return '_'
		}
		return r
	}, name)
	if ascii == "" || ascii == "." || strings.Trim(ascii, "_") == "" {
		ascii = "document.docx"
	}
	return fmt.Sprintf(`attachment; filename="%s"; filename*=UTF-8''%s`,
		ascii, rfc5987Escape(name))
}

// rfc5987Escape percent-encodes a name for the filename* parameter.
func rfc5987Escape(s string) string {
	var b strings.Builder
	for _, c := range []byte(s) {
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') ||
			(c >= '0' && c <= '9') || strings.IndexByte("-._~", c) >= 0 {
			b.WriteByte(c)
			continue
		}
		fmt.Fprintf(&b, "%%%02X", c)
	}
	return b.String()
}

// outputName derives the download name from the source and the 标题.
func outputName(sourceName string, plan *docmodel.Plan) string {
	// Defense in depth: the upload path already strips directories, but this
	// name also reaches a Content-Disposition header, and a stray separator
	// would produce an invalid one.
	sourceName = path.Base(strings.ReplaceAll(sourceName, "\\", "/"))
	base := strings.TrimSuffix(sourceName, path.Ext(sourceName))
	base = strings.TrimSpace(base)
	if t := strings.TrimSpace(titleOf(plan)); t != "" {
		base = t
	}
	if base == "" {
		base = "公文"
	}
	// Keep the name usable on Windows: forbid its reserved characters and
	// trim a trailing dot or space, which Explorer silently strips.
	base = strings.Map(func(r rune) rune {
		switch r {
		case '<', '>', ':', '"', '/', '\\', '|', '?', '*':
			return '_'
		}
		if r < 32 {
			return -1
		}
		return r
	}, base)
	base = strings.TrimRight(base, ". ")
	if len([]rune(base)) > 80 {
		base = string([]rune(base)[:80])
	}
	return base + ".docx"
}

// titleOf picks the plan's title, preferring an explicitly bound 标题 node so
// the download name uses the document's own wording.
func titleOf(plan *docmodel.Plan) string {
	if plan == nil {
		return ""
	}
	for _, e := range plan.Elements {
		if e.Role == docmodel.RoleTitle {
			return plan.Meta.Title
		}
	}
	return plan.Meta.Title
}

// readAllLimited reads a multipart file up to max bytes.
func readAllLimited(f multipart.File, max int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, fmt.Errorf("读取上传内容失败：%v", err)
	}
	if int64(len(data)) > max {
		return nil, fmt.Errorf("文件超过上限 %d MiB", max>>20)
	}
	return data, nil
}

// sanitizeName strips any directory component from an uploaded file name.
// A browser may send a full path, and honouring it would let a crafted upload
// influence where a file is later written.
func sanitizeName(name string) string {
	name = strings.ReplaceAll(name, "\\", "/")
	name = path.Base(name)
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == "/" {
		return "upload"
	}
	return name
}
