package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"govdoc/internal/api"
	"govdoc/internal/config"
	"govdoc/internal/docmodel"
	"govdoc/internal/gbstandard"
	"govdoc/internal/store"
)

// Formatting profiles — the named rule sets a document is rendered under.
//
// # Why the server owns the editing rules
//
// A profile is a complete gbstandard.Spec, so the client could in principle
// assemble one and send it. Keeping the invariants server-side is what stops a
// malformed profile from reaching the writers: a spec missing a role, or with a
// half-declared character grid, would otherwise fail deep inside a renderer
// with a message about types rather than about formatting rules.
//
// The checks here are therefore about *document intent*, not Go types: a profile
// that would render visibly wrong must be refused at the point the user can
// still fix it.

// toAPIProfiles converts stored profiles to their wire form.
func (s *Server) toAPIProfiles() []api.Profile {
	stored := s.ListProfiles()
	out := make([]api.Profile, 0, len(stored))
	for _, p := range stored {
		item := api.Profile{
			ID:          p.ID,
			Name:        p.Name,
			Description: p.Description,
			BuiltIn:     p.BuiltIn,
		}
		// A profile whose stored rules cannot be decoded is reported with an
		// empty spec rather than omitted: hiding it would make it look deleted,
		// and the user would have no idea what went wrong.
		if spec, err := p.Decode(); err == nil {
			item.Spec = spec
		} else {
			item.Description = strings.TrimSpace(item.Description + "（规则数据损坏，无法读取）")
		}
		out = append(out, item)
	}
	return out
}

func (s *Server) handleListProfiles(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, api.ProfilesResponse{
		Profiles: s.toAPIProfiles(),
		Active:   s.ActiveProfile(),
	})
}

// handleCreateProfile adds a profile.
//
// An empty Spec means "copy CopyFrom", which is how the UI's "复制一份再改"
// flow works: the client never has to round-trip a whole rule table it does not
// understand.
func (s *Server) handleCreateProfile(w http.ResponseWriter, r *http.Request) {
	var req api.ProfileRequest
	if !decodeBody(w, r, &req) {
		return
	}

	name := strings.TrimSpace(req.Name)
	if name == "" {
		writeError(w, http.StatusBadRequest, api.CodeBadRequest, "配置名称不能为空。")
		return
	}

	var spec gbstandard.Spec
	if src := strings.TrimSpace(req.CopyFrom); src != "" {
		spec, _ = s.ProfileSpec(src)
	} else {
		spec = req.Spec
	}
	if problems := validateNewSpec(spec); len(problems) > 0 {
		writeError(w, http.StatusBadRequest, api.CodeBadRequest,
			"这份配置的规则还不完整：\n· "+strings.Join(problems, "\n· "))
		return
	}

	id := config.SanitizeProfileID(name, func(id string) bool {
		_, err := s.ProfileSpec(id)
		return err == nil
	})

	profile, err := config.NewProfile(id, name, strings.TrimSpace(req.Description), spec)
	if err != nil {
		writeError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
		return
	}
	s.mu.Lock()
	s.profiles = append(s.profiles, profile)
	if req.Activate {
		s.activeProfile = id
	}
	s.mu.Unlock()

	s.log.Info("profile created", "id", id, "name", name)
	s.saveProfiles()

	created := s.toAPIProfiles()
	for _, p := range created {
		if p.ID == id {
			writeJSON(w, http.StatusOK, p)
			return
		}
	}
	writeJSON(w, http.StatusOK, created)
}

// handleUpdateProfile replaces a profile's name, description and rules.
//
// Built-in profiles are refused. They are the reference every user can return
// to, and editing one in place would leave no way back to the standard once a
// document has been formatted with the modified values. The UI offers "复制一份"
// instead, which produces an editable copy and leaves the built-in intact.
func (s *Server) handleUpdateProfile(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeError(w, http.StatusBadRequest, api.CodeBadRequest, "缺少配置 id。")
		return
	}

	var req api.ProfileRequest
	if !decodeBody(w, r, &req) {
		return
	}

	stored, ok := s.profileByID(id)
	if !ok {
		writeError(w, http.StatusNotFound, api.CodeNotFound, "找不到该配置。")
		return
	}
	if stored.BuiltIn {
		writeError(w, http.StatusBadRequest, api.CodeBadRequest,
			"内置配置不可直接修改。请先「复制一份」再调整，原始标准会保留不变。")
		return
	}

	if problems := validateNewSpec(req.Spec); len(problems) > 0 {
		writeError(w, http.StatusBadRequest, api.CodeBadRequest,
			"这份配置的规则还不完整：\n· "+strings.Join(problems, "\n· "))
		return
	}

	// An empty name would leave an unlabelled entry in the list, so the old one
	// is kept rather than accepted.
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = stored.Name
	}

	updated, err := config.NewProfile(id, name, strings.TrimSpace(req.Description), req.Spec)
	if err != nil {
		writeError(w, http.StatusInternalServerError, api.CodeInternal, err.Error())
		return
	}
	updated.BuiltIn = false

	s.mu.Lock()
	for i := range s.profiles {
		if s.profiles[i].ID == id {
			s.profiles[i] = updated
			break
		}
	}
	s.mu.Unlock()

	s.log.Info("profile updated", "id", id, "name", name)
	s.saveProfiles()

	for _, p := range s.toAPIProfiles() {
		if p.ID == id {
			writeJSON(w, http.StatusOK, p)
			return
		}
	}
	writeJSON(w, http.StatusOK, updated)
}

// handleDeleteProfile removes a profile.
//
// It refuses when the profile is still the default, because that would leave
// the tool with nothing to format new uploads under — and refuses when any
// in-memory document still points at it, because that document would silently
// fall back to different rules the next time it is exported. Requiring the user
// to switch those documents first is the honest behaviour: the alternative is a
// file that changes shape on export with nothing in the UI saying so.
func (s *Server) handleDeleteProfile(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeError(w, http.StatusBadRequest, api.CodeBadRequest, "缺少配置 id。")
		return
	}

	stored, ok := s.profileByID(id)
	if !ok {
		writeError(w, http.StatusNotFound, api.CodeNotFound, "找不到该配置。")
		return
	}
	if stored.BuiltIn {
		writeError(w, http.StatusBadRequest, api.CodeBadRequest,
			"内置配置不可删除。它是所有文档的兜底规则，也是恢复标准样式的依据。")
		return
	}
	if id == s.ActiveProfile() {
		writeError(w, http.StatusBadRequest, api.CodeBadRequest,
			"这是当前使用的配置，请先在设置里改用其它配置再删除。")
		return
	}
	if users := s.documentsUsingProfile(id); len(users) > 0 {
		writeError(w, http.StatusConflict, api.CodeBadRequest,
			fmt.Sprintf("还有 %d 份文档正在使用这份配置，请先在上传时为它们改选其它配置再删除。", len(users)))
		return
	}

	s.mu.Lock()
	kept := s.profiles[:0]
	for _, p := range s.profiles {
		if p.ID != id {
			kept = append(kept, p)
		}
	}
	s.profiles = kept
	s.mu.Unlock()

	s.log.Info("profile deleted", "id", id)
	s.saveProfiles()
	w.WriteHeader(http.StatusNoContent)
}

// handleActivateProfile chooses the profile for new uploads.
func (s *Server) handleActivateProfile(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID string `json:"id"`
	}
	if !decodeBody(w, r, &req) {
		return
	}
	if err := s.SetActiveProfile(strings.TrimSpace(req.ID)); err != nil {
		writeError(w, http.StatusBadRequest, api.CodeBadRequest, err.Error())
		return
	}
	s.log.Info("active profile changed", "profile", strings.TrimSpace(req.ID))
	s.saveProfiles()
	writeJSON(w, http.StatusOK, api.ProfilesResponse{
		Profiles: s.toAPIProfiles(),
		Active:   s.ActiveProfile(),
	})
}

func (s *Server) profileByID(id string) (config.Profile, bool) {
	for _, p := range s.ListProfiles() {
		if p.ID == id {
			return p, true
		}
	}
	return config.Profile{}, false
}

// documentsUsingProfile counts the in-memory documents bound to a profile.
func (s *Server) documentsUsingProfile(id string) []string {
	var out []string
	s.store.EachSession(func(_ string, sess *store.Session) bool {
		if sess.ProfileID == id {
			out = append(out, sess.ID)
		}
		return true
	})
	return out
}

// validateNewSpec checks a profile before it is stored.
//
// It rejects the failures that would show up as visibly wrong output rather
// than as an error: a missing style (the renderer would fall back to something
// arbitrary), an unknown 字号 (it would render at an unpredictable size), and a
// half-declared character grid (which has no correct interpretation).
func validateNewSpec(spec gbstandard.Spec) []string {
	var problems []string

	for _, role := range docmodel.AllRoles() {
		st, ok := spec.Style(role)
		if !ok {
			problems = append(problems, fmt.Sprintf("缺少「%s」的样式", roleName(role)))
			continue
		}
		if strings.TrimSpace(st.Font) == "" {
			problems = append(problems, fmt.Sprintf("「%s」未设置字体", roleName(role)))
		}
		if !st.Size.Known() {
			problems = append(problems, fmt.Sprintf("「%s」的字号 %q 不是标准字号", roleName(role), st.Size))
		}
	}
	if problems == nil {
		// Only run the full validator once the cheap checks pass; it produces
		// the same missing-role findings in a more verbose form.
		problems = gbstandard.ValidateSpec(spec)
	}
	sort.Strings(problems)
	return problems
}

// roleName prefers the profile's own Chinese label for a role, falling back to
// the constant's name so the message is still readable.
func roleName(role docmodel.Role) string {
	if n := roleLabels[role]; n != "" {
		return n
	}
	return string(role)
}

// roleLabels maps roles to their Chinese names for user-facing messages.
var roleLabels = map[docmodel.Role]string{
	docmodel.RoleCopyNumber:     "份号",
	docmodel.RoleSecurity:       "密级",
	docmodel.RoleUrgency:        "紧急程度",
	docmodel.RoleMasthead:       "发文机关标志",
	docmodel.RoleDocNumber:      "发文字号",
	docmodel.RoleSignatory:      "签发人",
	docmodel.RoleRedLine:        "分隔线",
	docmodel.RoleTitle:          "标题",
	docmodel.RoleRecipient:      "主送机关",
	docmodel.RoleBody:           "正文",
	docmodel.RoleHeading1:       "一级标题",
	docmodel.RoleHeading2:       "二级标题",
	docmodel.RoleHeading3:       "三级标题",
	docmodel.RoleHeading4:       "四级标题",
	docmodel.RoleAttachmentNote: "附件说明",
	docmodel.RoleSignature:      "署名",
	docmodel.RoleDate:           "日期",
	docmodel.RoleNote:           "附注",
	docmodel.RoleCC:             "抄送",
	docmodel.RolePrinter:        "印发机关",
	docmodel.RoleAttachment:     "附件正文",
}

// saveProfiles is defined in server.go, sharing one write path with
// persistSettings so neither can erase the other's fields.

// profilesJSON is used by tests and diagnostics to inspect the stored list.
func (s *Server) profilesJSON() string {
	b, _ := json.Marshal(s.toAPIProfiles())
	return string(b)
}
