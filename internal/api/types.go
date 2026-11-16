// Package api holds the HTTP wire types shared by the server and mirrored by
// the TypeScript client. Field names here are the contract: changing one is a
// breaking change for web/src/api/types.ts, so the two are pinned together by
// a test that compares this package's JSON keys against the TS declarations.
package api

import (
	"govdoc/internal/baseline"
	"govdoc/internal/docmodel"
	"govdoc/internal/gbstandard"
	"govdoc/internal/style"
)

// ErrorBody is the single error shape every failing endpoint returns. A
// machine-readable Code lets the UI branch on cause (bad_key, upstream,
// invalid_plan) instead of matching Chinese prose.
type ErrorBody struct {
	Error ErrorDetail `json:"error"`
}

// ErrorDetail carries one failure.
type ErrorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	// Details optionally names the offending nodes.
	Details []string `json:"details,omitempty"`
}

// Error codes, stable across releases.
const (
	CodeBadRequest   = "bad_request"
	CodeNotFound     = "not_found"
	CodeBadKey       = "bad_key"
	CodeUpstream     = "upstream_error"
	CodeNoLLMConfig  = "llm_not_configured"
	CodeInvalidPlan  = "invalid_plan"
	CodeParseFailed  = "parse_failed"
	CodeRenderFailed = "render_failed"
	CodeInternal     = "internal_error"
)

// Health is the liveness probe.
type Health struct {
	Status        string `json:"status"`
	Version       string `json:"version"`
	LLMConfigured bool   `json:"llm_configured"`
}

// LLMConfig is the model connection. APIKey is write-only: reads never echo
// it back, only whether one is stored.
type LLMConfig struct {
	Endpoint  string `json:"endpoint"`
	Model     string `json:"model"`
	Protocol  string `json:"protocol"`
	APIKeySet bool   `json:"api_key_set"`
	// APIKey is accepted on write and ignored on read.
	APIKey string `json:"api_key,omitempty"`
}

// ModelListRequest asks the server to probe a model connection and return its
// catalog. Endpoint is required; APIKey may be omitted when the saved key
// belongs to the same endpoint.
type ModelListRequest struct {
	Endpoint string `json:"endpoint"`
	Protocol string `json:"protocol,omitempty"`
	APIKey   string `json:"api_key,omitempty"`
}

// ModelListResponse is the model ids discovered at the endpoint.
type ModelListResponse struct {
	Models []string `json:"models"`
}

// ConfigResponse is the full application configuration.
type ConfigResponse struct {
	LLM       LLMConfig            `json:"llm"`
	Direction gbstandard.Direction `json:"direction"`
	Spec      gbstandard.Spec      `json:"spec"`
	// AllowEdits reports whether the model may correct 错别字/语病/用词.
	AllowEdits bool `json:"allow_edits"`
	// Rewrite is how far the model may move the text of each register. It is
	// the budget the export gate enforces, so the UI shows the same numbers
	// the verifier will apply.
	Rewrite style.Limits `json:"rewrite"`
	// ActiveProfile is the profile applied to new uploads.
	ActiveProfile string `json:"active_profile,omitempty"`
	// Profiles is every available profile, so the settings page can list them
	// without a second request.
	Profiles []Profile `json:"profiles,omitempty"`
	// Persistent reports whether settings survive a restart. When false the
	// UI must not promise the user that they will.
	Persistent bool `json:"persistent"`
	// SettingsPath is where the settings are stored, so "it works after a
	// restart" is something the user can verify rather than take on faith.
	SettingsPath string `json:"settings_path,omitempty"`
	// SavedToDisk reports whether the last save reached disk.
	SavedToDisk bool `json:"saved_to_disk"`
	// SaveWarning explains a persistence failure in the user's terms. The
	// setting is still in effect for this run, so it is a warning, not an
	// error.
	SaveWarning string `json:"save_warning,omitempty"`
}

// Profile is one named formatting rule set.
//
// The Spec is the whole rule table — page geometry plus one style per role — so
// a profile is precisely "what this document type looks like". The UI edits it
// directly rather than through a narrower set of knobs, which keeps this
// contract stable if a formatting property is added later.
type Profile struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// BuiltIn marks a seeded profile: it can be copied and tuned, but not
	// edited or deleted, so a known-good reference always remains.
	BuiltIn bool            `json:"built_in,omitempty"`
	Spec    gbstandard.Spec `json:"spec"`
}

// ProfilesResponse lists the available profiles.
type ProfilesResponse struct {
	Profiles []Profile `json:"profiles"`
	Active   string    `json:"active"`
}

// ProfileRequest creates or updates a profile.
//
// ID is required for an update and must be empty for a create, because a create
// mints a new key: reusing an existing one would silently overwrite somebody
// else's profile.
type ProfileRequest struct {
	ID          string          `json:"id,omitempty"`
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Spec        gbstandard.Spec `json:"spec"`
	// CopyFrom, when set, asks the server to seed the new profile's rules from
	// an existing one. The client sends an empty Spec in that case. Keeping the
	// copy server-side means the new profile starts from known-good rules
	// instead of whatever half-filled form the user had open.
	CopyFrom string `json:"copy_from,omitempty"`
	// Activate also makes the profile the default for new uploads.
	Activate bool `json:"activate,omitempty"`
}

// ConfigRequest updates configuration. Empty fields are left unchanged, so
// the UI can save the model without resending the key.
type ConfigRequest struct {
	Endpoint  *string               `json:"endpoint,omitempty"`
	Model     *string               `json:"model,omitempty"`
	Protocol  *string               `json:"protocol,omitempty"`
	APIKey    *string               `json:"api_key,omitempty"`
	Direction *gbstandard.Direction `json:"direction,omitempty"`
	// AllowEdits toggles whether the model may correct content.
	AllowEdits *bool `json:"allow_edits,omitempty"`
	// Rewrite updates the rewrite budget. Absent leaves it unchanged.
	Rewrite *style.Limits `json:"rewrite,omitempty"`
}

// FontStatus reports whether one 公文 typeface is installed on this machine.
// A missing font is not an error — the renderer falls back — but the user
// deserves to know before they print.
type FontStatus struct {
	Name      string   `json:"name"`
	Available bool     `json:"available"`
	Fallbacks []string `json:"fallbacks"`
	// Used helps the UI say *which* 公文 element needs it.
	Used []string `json:"used"`
}

// SpecResponse is the active formatting standard plus font diagnostics.
type SpecResponse struct {
	Spec  gbstandard.Spec `json:"spec"`
	Fonts []FontStatus    `json:"fonts"`
	// Problems lists ValidateSpec findings; non-empty means the spec is
	// not trustworthy and the UI should refuse to render.
	Problems []string `json:"problems,omitempty"`
}

// NodeSummary is a light node view for the upload response and the source
// panel. It omits Runs/Rows/Image payloads, which the source view fetches
// only for the nodes it actually displays.
type NodeSummary struct {
	ID          string            `json:"id"`
	Kind        docmodel.NodeKind `json:"kind"`
	Level       int               `json:"level,omitempty"`
	Text        string            `json:"text"`
	Order       int               `json:"order"`
	SourceStyle string            `json:"source_style,omitempty"`
	SourcePage  int               `json:"source_page,omitempty"`
	HasImage    bool              `json:"has_image,omitempty"`
	TableCells  int               `json:"table_cells,omitempty"`
}

// UploadResponse is returned by POST /api/documents.
type UploadResponse struct {
	DocID        string `json:"doc_id"`
	SourceName   string `json:"source_name"`
	Format       string `json:"format"`
	NodeCount    int    `json:"node_count"`
	ContentCount int    `json:"content_count"`
	// ProfileID is the formatting profile this document will be rendered with.
	// It is echoed so the UI can show which rules apply, and so an unknown
	// value the server fell back from is visible rather than silent.
	ProfileID string        `json:"profile_id,omitempty"`
	Nodes     []NodeSummary `json:"nodes"`
}

// DocumentResponse wraps the full parsed document.
type DocumentResponse struct {
	DocID     string             `json:"doc_id"`
	Doc       *docmodel.Document `json:"doc"`
	ProfileID string             `json:"profile_id,omitempty"`
}

// PlanRequest asks the server to analyse a document. Hint is an optional
// user-supplied nudge ("这是通知" / "按通报格式") passed to the model.
type PlanRequest struct {
	Hint string `json:"hint,omitempty"`
	// Direction lets one document override the session's 行文方向.
	Direction gbstandard.Direction `json:"direction,omitempty"`
	// AllowEdits permits the model to correct 错别字/语病/用词/标点 in addition
	// to restructuring. When nil the server's saved preference applies.
	//
	// Every correction it makes must arrive as a declared docmodel.Edit, so
	// enabling this never allows a silent rewrite — it only allows a *visible*
	// one.
	AllowEdits *bool `json:"allow_edits,omitempty"`
	// Baseline asks for a structure derived from the document's own numbering
	// instead of from a model.
	//
	// It exists because "no model configured" currently means "no output at
	// all", even for a document that already numbers its chapters — where the
	// structure is not a judgement call but a reading. The result is a
	// starting point the user can edit, not a substitute for analysis: no
	// chapter is invented, no title is guessed beyond the opening line.
	Baseline bool `json:"baseline,omitempty"`
}

// PlanJobResponse is returned when analysis starts; the client then follows
// the SSE stream at /api/jobs/{job_id}/events.
type PlanJobResponse struct {
	JobID string `json:"job_id"`
}

// StageEvent is one SSE progress frame.
type StageEvent struct {
	Name    string `json:"name"`
	Message string `json:"message"`
	Done    bool   `json:"done"`
}

// PartialEvent streams a provisional snapshot while the model is still
// writing its answer.
//
// The plan is not complete and has not passed VerifyPlan. The client uses it
// only to append structure rows and review cards; the terminal DoneEvent
// remains the sole authority for what may be rendered.
type PartialEvent struct {
	Plan  *docmodel.Plan  `json:"plan,omitempty"`
	Edits []docmodel.Edit `json:"edits,omitempty"`
	Reset bool            `json:"reset,omitempty"`
}

// PlanResponse carries a plan together with its verification verdict. The
// pair always travels together so the UI can never display a plan without
// knowing whether it is renderable.
type PlanResponse struct {
	Plan   *docmodel.Plan   `json:"plan"`
	Report *docmodel.Report `json:"report"`
	// Structure compares the plan against the structure the source document
	// itself declares. VerifyPlan proves nothing was lost or duplicated;
	// this is what shows whether anything was *moved* — the structural
	// counterpart of the text-level change log.
	Structure *baseline.StructureDiff `json:"structure,omitempty"`
	JobID     string                  `json:"job_id,omitempty"`
}

// ReviseRequest asks the model for declared content edits in response to a
// chat instruction. Nothing in the request is written into the document; the
// model answers with Edits that the user reviews one by one.
type ReviseRequest struct {
	Instruction string `json:"instruction"`
	// Selection is the text the user highlighted in the preview. Empty means
	// the instruction applies to the whole document.
	Selection string `json:"selection,omitempty"`
	// NodeID scopes the request to one source block when the selection came
	// from it.
	NodeID string `json:"node_id,omitempty"`
	// AllowEdits overrides the saved preference for this turn.
	AllowEdits *bool `json:"allow_edits,omitempty"`
}

// ReviseResponse carries proposed edits plus the chat reply. The edits are
// not applied: each one still has to be accepted by the user.
type ReviseResponse struct {
	Edits []docmodel.Edit `json:"edits"`
	Reply string          `json:"reply,omitempty"`
}

// DoneEvent is the terminal SSE frame.
type DoneEvent struct {
	Plan      *docmodel.Plan          `json:"plan"`
	Report    *docmodel.Report        `json:"report"`
	Structure *baseline.StructureDiff `json:"structure,omitempty"`
	Error     string                  `json:"error,omitempty"`
}

// RenderRequest asks for the formatted output.
type RenderRequest struct {
	// Format is "docx" (the only supported output today; PDF is produced by
	// the user's word processor, which keeps the file editable as a 公文
	// must be).
	Format string `json:"format"`
	// Plan, when non-nil, is the user's latest edit and is verified before
	// rendering. When nil the stored plan is used.
	Plan *docmodel.Plan `json:"plan,omitempty"`
}

// RenderResponse reports a finished render. Report and Diff are included
// because a download the user cannot audit is not trustworthy.
type RenderResponse struct {
	FileID   string                 `json:"file_id"`
	Filename string                 `json:"filename"`
	Size     int                    `json:"size"`
	Report   *docmodel.Report       `json:"report"`
	Diff     *docmodel.VerbatimDiff `json:"diff"`
	// SynthText lists the scaffolding strings the assembler generated
	// (numbering, masthead). Listing them is what lets a reviewer confirm
	// that nothing *else* was invented.
	SynthText []string `json:"synth_text,omitempty"`
}
