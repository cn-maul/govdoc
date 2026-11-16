// Package server implements the GovDoc HTTP API and serves the embedded web UI.
//
// # Shape of the program
//
// GovDoc ships as one binary: the Vue build is embedded with //go:embed, and
// the same process serves both the API and the UI. That is a deliberate
// choice for a 公文 tool — the alternative (a separate static host) means a
// user must run two things and keep a port open, and 公文 handling often
// happens on machines where that is not acceptable.
package server

import (
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"govdoc/internal/config"
	"govdoc/internal/docmodel"
	"govdoc/internal/gbstandard"
	"govdoc/internal/store"
	"govdoc/internal/style"
)

// Version is the application version reported by /api/health.
const Version = "1.0.0"

// Config is the process configuration.
type Config struct {
	// Addr is the listen address, e.g. "127.0.0.1:8765".
	Addr string
	// LLM holds the initial model connection, usually from flags/env.
	LLM LLMConfig
	// Spec is the formatting standard. A zero value is replaced by
	// gbstandard.Default().
	Spec gbstandard.Spec
	// MaxFiles bounds rendered outputs held in memory.
	MaxFiles int
	// StaticDir, when non-empty, serves the UI from disk instead of the
	// embedded filesystem. It exists for front-end development
	// (`pnpm run dev` builds into web/dist); the shipped binary always uses
	// the embedded copy.
	StaticDir string
	// Settings persists the model connection and preferences between runs.
	// Nil disables persistence, which is what the tests want and what a
	// read-only install gets.
	Settings *config.Store
	// AllowEdits overrides the default of permitting content corrections.
	// nil keeps the default (on); tests and embedders use it to start in the
	// strict mode.
	AllowEdits *bool
	Logger     *slog.Logger
}

// LLMConfig is the mutable model connection held by the server.
type LLMConfig struct {
	Endpoint string
	APIKey   string
	Model    string
	Protocol string
	Timeout  time.Duration
}

// configured reports whether a plan can be requested at all.
func (c LLMConfig) configured() bool {
	return strings.TrimSpace(c.Endpoint) != "" &&
		strings.TrimSpace(c.APIKey) != "" &&
		strings.TrimSpace(c.Model) != ""
}

// Server holds the mutable state of one running instance.
type Server struct {
	cfg   Config
	store *store.Store
	log   *slog.Logger

	// mu guards cfg.LLM, cfg.Spec and cfg.StaticDir, all of which the
	// settings endpoints may change while requests are in flight.
	mu       sync.RWMutex
	llm      LLMConfig
	spec     gbstandard.Spec
	analyzer Analyzer
	// allowEdits is whether the user permits the model to correct content.
	// It defaults to true: the 校对 pane is the workbench's main output, and a
	// default of false silently produced an empty review list on every fresh
	// install — indistinguishable from the feature being broken.
	allowEdits bool
	// rewrite is how far the model may move the text of each register. It is
	// stored beside allowEdits because both describe how much the user trusts
	// the model, not what the document type looks like.
	rewrite style.Limits
	// profiles are the available formatting rule sets and activeProfile is the
	// one applied to new uploads. They live under the same mutex as spec because
	// the settings endpoints change both, and a profile *is* a spec.
	profiles      []config.Profile
	activeProfile string
	// specOverridden records that the caller supplied its own spec, in which
	// case the built-in 公文 profile must not overwrite it on startup.
	specOverridden bool
}

// Progress is one analysis stage notification. It mirrors llm.Stage so
// cmd/govdoc can adapt the LLM client to the Analyzer interface with a
// closure, keeping this package free of any dependency on the model SDK.
type Progress struct {
	Name    string `json:"name"`
	Message string `json:"message"`
	Done    bool   `json:"done"`
}

// PartialProgress is one provisional plan snapshot during a model call.
//
// Reset asks the client to discard the previous attempt's provisional rows.
// A snapshot is never treated as a verdict; only the Analyzer's final return
// value goes through VerifyPlan and becomes renderable.
type PartialProgress struct {
	Plan  *docmodel.Plan
	Edits []docmodel.Edit
	Reset bool
}

// Analyzer produces a structural plan for a document.
//
// It is an interface for two reasons: internal/server then has no dependency
// on rosetta (the model SDK stays behind one boundary), and the whole HTTP
// pipeline can be tested end to end with a deterministic stub instead of a
// network call.
//
// opts carries the per-request settings: the user's hint about the document's
// nature, and whether content corrections are permitted. Both are passed
// separately from the document — a hint is intent, not content, and enabling
// corrections never means the model may rewrite silently; it must declare each
// change as a docmodel.Edit.
//
// Note that Analyze does NOT return a verdict. Verification is applied by the
// server itself via docmodel.VerifyPlan, so there is exactly one place in the
// program that decides whether a plan is renderable — an analyzer cannot
// bypass it by reporting success.
type Analyzer interface {
	Analyze(
		ctx context.Context,
		doc *docmodel.Document,
		opts AnalysisOptions,
		progress func(Progress),
		partial func(PartialProgress),
	) (*docmodel.Plan, error)
}

// Reviser proposes declared content edits in response to a user request.
//
// It is deliberately separate from Analyzer, and deliberately returns edits
// rather than a rewritten document. The chat panel lets the user ask for any
// change ("把这句话改短一点"), and the answer must remain something the user
// can accept or reject one item at a time. Returning a plan, or rewritten
// text, would either re-run the whole restructuring or let prose bypass the
// before/after review the rest of the program is built around.
//
// Every edit it returns is still verified server-side: Before must occur in
// the named node, so a model cannot invent a target.
type Reviser interface {
	Revise(ctx context.Context, doc *docmodel.Document, req RevisionRequest) (RevisionResult, error)
}

// RevisionResult is one chat turn's answer.
type RevisionResult struct {
	// Edits are the proposed changes. None of them is applied here: the user
	// accepts or rejects each one in the review list.
	Edits []docmodel.Edit
	// Reply is a short explanation shown in the chat, including when the model
	// declines to change anything.
	Reply string
}

// RevisionRequest carries one chat turn.
type RevisionRequest struct {
	// Instruction is what the user wants changed, in their own words.
	Instruction string
	// Selection is the text the user highlighted for the request. Empty means
	// the instruction applies to the document as a whole.
	Selection string
	// NodeID names the node the selection came from, when known. It scopes the
	// model's answer and lets the server check Before against that node.
	NodeID string
	// AllowEdits permits the model to propose content changes at all. The chat
	// panel is useless when it is false, but the override still exists so a
	// saved "no corrections" preference is not silently ignored.
	AllowEdits bool
}

// Reconfigurable is implemented by an analyzer that can adopt a changed model
// connection without a restart. Applying a saved key immediately matters:
// otherwise the settings save looks like it silently failed.
type Reconfigurable interface {
	Reconfigure(LLMConfig) error
}

// ModelLister lists the models available at a connection that may not be saved
// yet. The settings UI uses it to turn the model-name field into a picker.
type ModelLister interface {
	ListModels(ctx context.Context, cfg LLMConfig) ([]string, error)
}

// New builds a server.
func New(cfg Config) *Server {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	// A zero or partial spec would make every handler's lookups fall back
	// silently, so an unusable spec is replaced by the standard table.
	specOverridden := len(cfg.Spec.Styles) > 0
	if !specOverridden {
		cfg.Spec = gbstandard.Default()
	}
	if cfg.MaxFiles == 0 {
		cfg.MaxFiles = 32
	}
	s := &Server{
		cfg:            cfg,
		store:          store.New(cfg.MaxFiles),
		log:            cfg.Logger,
		llm:            cfg.LLM,
		spec:           cfg.Spec,
		// Corrections are proposed by default. The user still screens every
		// edit before it reaches the document, so this widens what may be
		// *offered*, never what may happen silently.
		allowEdits:     cfg.AllowEdits == nil || *cfg.AllowEdits,
		specOverridden: specOverridden,
		// Seed the in-memory profile list even without a settings file, so the
		// profile endpoints work on a first run with -no-save.
		activeProfile: config.ProfileGovDoc,
	}
	s.profiles = config.BuiltinProfiles()
	s.syncBuiltinProfiles()
	// Adopt saved settings over the flag/env values, but only where the file
	// actually has a value. A user who fixes a typo in the model name on the
	// command line should not be overridden by a stale file.
	s.loadSettings()
	return s
}

// loadSettings merges the persisted settings into the running configuration.
//
// Precedence is: saved value when present, else the flag/env value already in
// memory. That order is deliberate — the file is what the user last confirmed
// through the UI, while flags are usually a one-off for this run. Where the two
// disagree and the file wins, the log says so, because a silently ignored
// command-line flag is a confusing thing to debug.
func (s *Server) loadSettings() {
	st := s.cfg.Settings
	if st == nil || st.Disabled() {
		return
	}
	f, err := st.Load()
	if err != nil {
		// Not fatal: a corrupt or unreadable settings file must not stop the
		// program, and the message tells the user why their settings were
		// ignored.
		s.log.Warn("读取已保存的设置失败，将使用启动参数", "err", err.Error(), "path", st.Path())
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if v := strings.TrimSpace(f.Endpoint); v != "" {
		if s.llm.Endpoint != "" && s.llm.Endpoint != v {
			s.log.Info("已保存的设置覆盖了启动参数", "field", "endpoint", "saved", v, "flag", s.llm.Endpoint)
		}
		s.llm.Endpoint = v
	}
	if v := strings.TrimSpace(f.Model); v != "" {
		if s.llm.Model != "" && s.llm.Model != v {
			s.log.Info("已保存的设置覆盖了启动参数", "field", "model", "saved", v, "flag", s.llm.Model)
		}
		s.llm.Model = v
	}
	if v := strings.TrimSpace(f.Protocol); v != "" {
		s.llm.Protocol = v
	}
	if v := strings.TrimSpace(f.APIKey); v != "" {
		s.llm.APIKey = v
	}
	if v := strings.TrimSpace(f.Direction); v != "" {
		if d := gbstandard.Direction(v); isKnownDirection(d) {
			s.spec.Direction = d
		}
	}
	// A file written before the field became a pointer has no allow_edits key,
	// which means "never chosen" — keep the default (on). Only an explicit
	// false in the file turns corrections off.
	if f.AllowEdits != nil {
		s.allowEdits = *f.AllowEdits
	}
	if f.Rewrite.Enabled || f.Rewrite.FormalMax > 0 || f.Rewrite.MixedMax > 0 ||
		f.Rewrite.ColloquialMax > 0 {
		s.rewrite = f.Rewrite.Normalized()
	}

	// Profiles replace the single spec as the unit of formatting. The spec the
	// server was constructed with becomes the 公文 profile's rules when no
	// stored profile carries them, so a command-line tuned spec is not lost.
	s.profiles = f.Profiles
	s.activeProfile = f.ActiveProfile
	s.syncBuiltinProfiles()

	s.log.Info("已载入保存的设置",
		"path", st.Path(),
		"endpoint", s.llm.Endpoint,
		"model", s.llm.Model,
		"key_set", s.llm.APIKey != "",
		"direction", s.spec.Direction,
		"allow_edits", s.allowEdits,
		"rewrite", s.rewrite.Describe(),
		"profiles", len(s.profiles),
		"active_profile", s.activeProfile)
}

// syncBuiltinProfiles makes sure the built-in profiles are present and the
// active id names one that exists.
//
// A build can introduce a new built-in, so the list is topped up on every start
// — but a stored profile with the same id is never replaced, because it may be
// the user's tuned copy of it.
func (s *Server) syncBuiltinProfiles() {
	f := config.File{Profiles: s.profiles, ActiveProfile: s.activeProfile}
	f = config.EnsureProfiles(f)

	// The 公文 profile is special: if the server was started with a spec that is
	// not the stock standard (a -direction flag, or a caller-supplied table),
	// that is the user's intent for 公文 and must not be overwritten by the
	// built-in copy.
	//
	// The live 行文方向 survives the round trip: it is an overlay on the spec,
	// not part of a profile's rules, and letting the profile's stored copy win
	// here is what made a direction chosen in the settings silently revert.
	if p, ok := f.ProfileByID(config.ProfileGovDoc); ok && p.BuiltIn && !s.specOverridden {
		if spec, err := p.Decode(); err == nil {
			spec.Direction = s.spec.Direction
			s.spec = spec
			s.profiles = f.Profiles
			s.reencodeProfile(config.ProfileGovDoc, spec)
		}
	}

	s.profiles = f.Profiles
	s.activeProfile = f.ActiveProfile
	if s.activeProfile == "" {
		s.activeProfile = config.ProfileGovDoc
	}
}

// reencodeProfile writes a decoded spec back into the profile list, so a change
// made at runtime (行文方向) survives into the next save rather than being
// re-derived from the file each start.
func (s *Server) reencodeProfile(id string, spec gbstandard.Spec) {
	p, err := config.NewProfile(id, "", "", spec)
	if err != nil {
		return
	}
	for i := range s.profiles {
		if s.profiles[i].ID != id {
			continue
		}
		// Keep the human-facing metadata; only the rules are replaced.
		s.profiles[i].Spec = p.Spec
		return
	}
}

// isKnownDirection reports whether a direction value is one the standard
// defines. An unknown value from a hand-edited file is ignored rather than
// applied, since a wrong 行文方向 moves the 发文机关标志.
func isKnownDirection(d gbstandard.Direction) bool {
	switch d {
	case gbstandard.DirDown, gbstandard.DirUp, gbstandard.DirParallel:
		return true
	}
	return false
}

// persistSettings writes the current settings to disk. Failure is logged and
// reported to the caller, but never rolls back the in-memory change: the user
// asked for the setting to apply, and it does; not surviving a restart is a
// lesser problem than the save appearing to do nothing.
func (s *Server) persistSettings() error {
	return s.persist(func(f *config.File) {
		s.mu.RLock()
		defer s.mu.RUnlock()
		f.Endpoint = s.llm.Endpoint
		f.Model = s.llm.Model
		f.Protocol = s.llm.Protocol
		f.APIKey = s.llm.APIKey
		f.Direction = string(s.spec.Direction)
		f.AllowEdits = &s.allowEdits
		f.Rewrite = s.rewrite
	})
}

// saveProfiles writes the profile list back to disk.
//
// It shares one write path with persistSettings on purpose. They used to be
// separate, and because both rebuilt the file from only the fields they cared
// about, saving the model connection silently erased every profile — a data-loss
// bug that no single save could surface, only the next restart.
func (s *Server) saveProfiles() {
	if err := s.persist(func(f *config.File) {
		s.mu.RLock()
		defer s.mu.RUnlock()
		f.Profiles = append([]config.Profile(nil), s.profiles...)
		f.ActiveProfile = s.activeProfile
	}); err != nil {
		s.log.Error("保存配置列表失败", "err", err.Error(), "path", s.settingsPath())
	}
}

// persist loads the stored file, applies mutate to it, and saves it back.
//
// Loading first is what makes concurrent partial updates safe: a handler that
// only knows about the model connection does not overwrite profiles it never
// read, and neither does the profile editor clobber the key. The read-modify-
// write is not atomic against a concurrent Save, which is why both callers are
// invoked from handlers that already serialize on the server mutex.
func (s *Server) persist(mutate func(*config.File)) error {
	st := s.cfg.Settings
	if st == nil || st.Disabled() {
		return nil
	}

	f, err := st.Load()
	if err != nil {
		// A corrupt or missing file is not a reason to refuse the update; the
		// caller's values are the newer truth.
		f = config.File{}
	}
	mutate(&f)

	if err := st.Save(f); err != nil {
		s.log.Error("保存设置失败", "err", err.Error(), "path", st.Path())
		return err
	}
	s.log.Info("设置已保存", "path", st.Path(), "profiles", len(f.Profiles))
	return nil
}

// SetAnalyzer installs the plan producer. It is set by cmd/govdoc from the
// llm package; keeping it injectable means internal/server has no dependency
// on the model SDK, and tests can drive the whole pipeline deterministically.
func (s *Server) SetAnalyzer(a Analyzer) {
	s.mu.Lock()
	s.analyzer = a
	s.mu.Unlock()
}

// EffectiveLLM returns the model connection actually in force, after saved
// settings have been merged over the values passed at startup.
//
// cmd/govdoc needs this: the caller that builds the SDK client and prints the
// startup banner must use the *effective* configuration, not the raw flags.
// Using the flags there was a real bug — a saved key loaded correctly into the
// server but never reached the model client, so a user who configured the tool
// once still had to re-enter everything after each restart.
func (s *Server) EffectiveLLM() LLMConfig {
	return s.currentLLM()
}

// EffectiveSpec returns the formatting standard actually in force, including a
// 行文方向 restored from saved settings.
func (s *Server) EffectiveSpec() gbstandard.Spec {
	return s.currentSpec()
}

// Handler returns the HTTP handler, with the API mounted under /api and
// everything else served from the static filesystem.
func (s *Server) Handler(staticFS fs.FS) http.Handler {
	mux := http.NewServeMux()
	s.routes(mux)

	var ui http.Handler
	switch {
	case s.cfg.StaticDir != "":
		ui = spaHandler(http.Dir(s.cfg.StaticDir))
	case staticFS != nil:
		ui = spaHandler(http.FS(staticFS))
	default:
		ui = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			writeError(w, http.StatusServiceUnavailable, "ui_unavailable",
				"界面资源未打包。请先执行 web 构建（pnpm run build），或使用 --static 指定目录。")
		})
	}

	// API routes win over the SPA fallback; the SPA catches everything else.
	root := http.NewServeMux()
	root.Handle("/api/", mux)
	root.Handle("/", ui)
	return withRecovery(s.log, withLogging(s.log, root))
}

// routes registers the API endpoints. See docs/CONTRACTS.md §3.
func (s *Server) routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/health", s.handleHealth)
	mux.HandleFunc("GET /api/config", s.handleGetConfig)
	mux.HandleFunc("PUT /api/config", s.handlePutConfig)
	mux.HandleFunc("POST /api/llm/models", s.handleListModels)
	mux.HandleFunc("GET /api/spec", s.handleSpec)

	// Formatting profiles. Registration order matters here: the literal
	// "activate" path must be matched before the "{id}" pattern, or Go's mux
	// would treat "activate" as an id.
	mux.HandleFunc("GET /api/profiles", s.handleListProfiles)
	mux.HandleFunc("POST /api/profiles", s.handleCreateProfile)
	mux.HandleFunc("POST /api/profiles/activate", s.handleActivateProfile)
	mux.HandleFunc("PUT /api/profiles/{id}", s.handleUpdateProfile)
	mux.HandleFunc("DELETE /api/profiles/{id}", s.handleDeleteProfile)

	mux.HandleFunc("POST /api/documents", s.handleUpload)
	mux.HandleFunc("GET /api/documents/{id}", s.handleGetDocument)
	mux.HandleFunc("GET /api/documents/{id}/images/{nodeID}", s.handleGetImage)
	mux.HandleFunc("POST /api/documents/{id}/plan", s.handleStartPlan)
	mux.HandleFunc("GET /api/documents/{id}/plan", s.handleGetPlan)
	mux.HandleFunc("PUT /api/documents/{id}/plan", s.handlePutPlan)
	mux.HandleFunc("POST /api/documents/{id}/revise", s.handleRevise)
	mux.HandleFunc("POST /api/documents/{id}/render", s.handleRender)
	mux.HandleFunc("GET /api/documents/{id}/preview", s.handlePreview)

	mux.HandleFunc("GET /api/jobs/{jobID}/events", s.handleJobEvents)
	mux.HandleFunc("GET /api/files/{fileID}", s.handleDownload)

	// Anything else under /api is a client bug; answer in the standard error
	// shape rather than the SPA's HTML, so a mistyped endpoint does not look
	// like a successful page load.
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "not_found",
			fmt.Sprintf("接口不存在：%s %s", r.Method, r.URL.Path))
	})
}

// spaHandler serves a single-page app: files that exist are served directly,
// and anything else falls back to index.html so client-side routing works on
// a deep link or a reload.
func spaHandler(root http.FileSystem) http.Handler {
	fileServer := http.FileServer(root)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/")
		if p == "" {
			p = "index.html"
		}
		f, err := root.Open(p)
		if err != nil {
			// Not a real file: hand the SPA its entry point.
			serveIndex(w, r, root)
			return
		}
		_ = f.Close()
		// Hashed asset names are content-addressed, so they can be cached
		// hard; index.html must not be, or a rebuild would not be picked up.
		if strings.HasPrefix(p, "assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		fileServer.ServeHTTP(w, r)
	})
}

func serveIndex(w http.ResponseWriter, r *http.Request, root http.FileSystem) {
	f, err := root.Open("index.html")
	if err != nil {
		writeError(w, http.StatusNotFound, "ui_unavailable",
			"未找到界面入口 index.html。请先执行 web 构建（pnpm run build）。")
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "无法读取界面入口文件。")
		return
	}
	// ServeContent handles range requests and sets a correct Content-Length.
	rs, ok := f.(interface {
		Read([]byte) (int, error)
		Seek(int64, int) (int64, error)
	})
	if !ok {
		writeError(w, http.StatusInternalServerError, "internal_error", "界面入口文件不可读。")
		return
	}
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeContent(w, r, "index.html", st.ModTime(), rs)
}

// withRecovery turns a handler panic into a 500 in the standard error shape.
// Without it a bug in one endpoint would kill the connection with no
// diagnosis, which is the worst outcome for a user mid-edit.
func withRecovery(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				log.Error("http: 处理请求时发生 panic",
					"method", r.Method, "path", r.URL.Path, "panic", fmt.Sprint(v))
				writeError(w, http.StatusInternalServerError, "internal_error",
					"服务器内部错误，请重试；若持续出现请查看日志。")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// withLogging records slow or failing requests without drowning the log in
// routine 200s.
func withLogging(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		dur := time.Since(start)
		if rec.status >= 400 || dur > 2*time.Second {
			log.Warn("http", "method", r.Method, "path", r.URL.Path,
				"status", rec.status, "ms", dur.Milliseconds())
		}
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (r *statusRecorder) WriteHeader(code int) {
	if !r.wrote {
		r.status = code
		r.wrote = true
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	r.wrote = true
	return r.ResponseWriter.Write(b)
}

// Flush exposes the underlying flusher so the SSE endpoint can push frames.
func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
