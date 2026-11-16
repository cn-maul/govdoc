// Package llm is the only place in the program that talks to a model.
//
// # What this package is, and is not, responsible for
//
// It builds a prompt that asks the model to *group* the source paragraphs into
// a chapter tree, sends it through github.com/cn-maul/rosetta, parses the
// answer, and — critically — checks its own work against
// docmodel.VerifyPlan, feeding the failures back for another attempt.
//
// It is not responsible for deciding whether a plan is renderable. That
// decision belongs to docmodel.VerifyPlan, and the server applies it once at
// the choke point before anything is stored. This package verifying too is a
// quality measure (a plan that already satisfies the checker is far more
// likely to survive the server's independent check), not an authority.
//
// # Why the prompt is built from ids
//
// The model never sees a request to retype the document. It is given a list of
// `id | kind | text` lines and must answer with a plan that references those
// ids. Content cannot be silently rewritten because content is never asked
// for: the assembler pulls every character from the parsed Document, and
// VerifyPlan rejects any plan that drops or duplicates a paragraph. This is
// the mechanism that makes 原文逐字不变 a property of the design rather than a
// hope about model behaviour.
package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/cn-maul/rosetta"

	"govdoc/internal/baseline"
	"govdoc/internal/docmodel"
)

// ErrNotConfigured is returned when endpoint, key or model is missing.
var ErrNotConfigured = errors.New("llm: 未配置模型连接")

// ErrTruncated is returned when the model's answer was cut off by the output
// cap. A truncated plan would look complete but silently lose chapters, so it
// is treated as a hard failure rather than parsed optimistically.
var ErrTruncated = errors.New("llm: 模型回答被截断（输出长度上限），未能得到完整结构方案")

// maxRepairRounds bounds the self-repair loop. Two rounds is enough for a
// model to fix a missed node or a wrong id; more usually means the prompt is
// unclear and the loop is burning the user's tokens.
const maxRepairRounds = 2

// defaultTimeout bounds one model call when the caller does not set one.
const defaultTimeout = 120 * time.Second

// defaultMaxOutput is the output cap for a small document, and the floor for
// a larger one. A plan is verbose JSON — one object per section plus up to
// four node ids per body — so a fixed cap is right for a 通知 and too small
// for a 100-page 规划. The cap therefore scales with the document (see
// maxOutput); the value is also what makes ErrTruncated actionable rather
// than mysterious.
const defaultMaxOutput = 8192

// maxOutputCeiling bounds the computed cap. Beyond this a single answer is
// impractically slow and the model is more likely to wander than to finish.
const maxOutputCeiling = 32768

// tokensPerNode covers the plan JSON each body node costs when it is merely
// referenced: its id in body_node_ids plus quotes and a comma, plus its share
// of a section object.
const tokensPerNode = 40

// tokensPerRune covers documents that must be *recomposed*. A structureless
// document can be one paragraph holding thousands of characters, and a
// recomposition writes every output paragraph back into the answer — roughly
// one token per character for Chinese. Sizing the cap by node count alone
// gives exactly that document the smallest budget, which is why a one-node
// test document overflowed an 8k cap while producing a valid plan.
//
// The estimate is deliberately generous and then capped: a Chinese character
// is about 1–1.5 tokens in most tokenizers, and the plan also contains ids and
// structure, so 2 tokens per rune bounds the recomposition payload without
// making every large document pay for a huge cap.
const tokensPerRune = 2

// defaultTemperature is applied when the caller sets none.
//
// This is a classification task: the same document should produce the same
// tree every time. Leaving it nil lets each provider pick its own default,
// and several use 1.0 — which is why two runs over one document could differ
// in where a borderline paragraph landed. rosetta drops this field
// automatically for reasoning-capable requests, so setting it costs nothing
// on models that reject sampling parameters.
//
// 0 (greedy decoding) rather than a small positive value: with a positive
// temperature the same prompt has a distribution of plausible answers, and the
// borderline decisions this task is full of (is this line a heading? does this
// chapter start here?) are exactly the ones that flip between samples.
const defaultTemperature = 0

// defaultSeed is sent with every planning request so providers that support
// seeding (OpenAI, and compatible gateways) return the same answer for the same
// input. Providers without the field ignore it.
//
// It is not a guarantee: some gateways drop it silently, and even OpenAI only
// calls it "best effort". It is worth sending anyway, because when it works the
// user's "run it again and see" reproduces the same tree instead of a different
// one — which makes a complaint about the result traceable to the prompt rather
// than to sampling noise.
const defaultSeed = 20240501

// Config is the model connection.
type Config struct {
	Endpoint string
	APIKey   string
	Model    string
	// Protocol is "" (defaults to openai-chat), "openai-chat",
	// "openai-responses" or "anthropic".
	Protocol string
	// Timeout bounds one call. Zero applies a 120s default.
	Timeout time.Duration
	// MaxOutputTokens overrides the output cap. Zero applies the default.
	MaxOutputTokens int
	// Temperature is applied when non-nil. Planning is a structuring task, so
	// the caller normally wants a low value. Nil applies
	// defaultTemperature rather than the provider's own default, because a
	// provider default of 1.0 makes the same document produce different
	// trees on different runs.
	Temperature *float64
}

// Stage is one progress notification shown while a plan is produced.
type Stage struct {
	Name    string `json:"name"`
	Message string `json:"message"`
	Done    bool   `json:"done"`
}

// Stage names, stable so the UI can key on them.
const (
	StageConnect  = "connect"
	StageAnalyze  = "analyze"
	StageRepair   = "repair"
	StageValidate = "validate"
	StageComplete = "complete"
)

// PlanError carries a plan together with the verdict that rejected it.
//
// Returning both matters: when the repair loop gives up, the user is far
// better served by seeing what the model produced and which paragraphs it
// mishandled than by a bare "failed". The server stores the partial plan so
// the 校对 screen opens with something to fix.
type PlanError struct {
	Plan   *docmodel.Plan
	Report *docmodel.Report
	// Cause is the underlying failure, when there was one (an API error, a
	// timeout). It is nil when the loop simply exhausted its rounds.
	Cause error
	// Rounds is how many model calls were made.
	Rounds int
}

func (e *PlanError) Error() string {
	var b strings.Builder
	b.WriteString("llm: 未能生成通过校验的结构方案")
	if e.Report != nil {
		fmt.Fprintf(&b, "（%s）", e.Report.Summary())
	}
	fmt.Fprintf(&b, "，已尝试 %d 次", e.Rounds)
	if e.Cause != nil {
		fmt.Fprintf(&b, "：%v", e.Cause)
	}
	if e.Report != nil {
		if errs := e.Report.Errors(); len(errs) > 0 {
			b.WriteString("。主要问题：")
			for i, d := range errs {
				if i >= 3 {
					fmt.Fprintf(&b, " 等 %d 项", len(errs))
					break
				}
				if i > 0 {
					b.WriteString("；")
				}
				b.WriteString(d.Message)
			}
		}
	}
	return b.String()
}

func (e *PlanError) Unwrap() error { return e.Cause }

// Client produces plans for one configuration. It is safe for concurrent use:
// rosetta.Client is, and the only mutable state is the capability flag below,
// which is atomic.
type Client struct {
	cfg    Config
	client *rosetta.Client
	// noJSONMode records that this endpoint rejected response_format. Some
	// OpenAI-compatible gateways (and a few proxies in front of them) answer
	// 400 "unknown parameter" to a perfectly valid response_format. Rather
	// than failing the analysis, the client drops the field for the rest of
	// its lifetime and relies on the prompt, which already demands bare JSON.
	// Sticky by design: a second probe per request would cost a round trip
	// and fail the same way every time.
	noJSONMode atomic.Bool
}

// New validates the configuration and builds a client.
func New(cfg Config) (*Client, error) {
	if strings.TrimSpace(cfg.Endpoint) == "" ||
		strings.TrimSpace(cfg.APIKey) == "" ||
		strings.TrimSpace(cfg.Model) == "" {
		return nil, ErrNotConfigured
	}

	client, err := newSDKClient(cfg)
	if err != nil {
		return nil, err
	}
	return &Client{cfg: cfg, client: client}, nil
}

// ListModelsFor builds a client from a connection that is not saved yet and
// fetches its model catalog. The settings UI needs this before the
// configuration is complete, so Model is intentionally not required here: the
// whole point of the request is choosing one.
func ListModelsFor(ctx context.Context, cfg Config) ([]string, error) {
	client, err := newSDKClient(cfg)
	if err != nil {
		return nil, err
	}
	return (&Client{cfg: cfg, client: client}).ListModels(ctx)
}

// newSDKClient validates the connection and builds the SDK client.
func newSDKClient(cfg Config) (*rosetta.Client, error) {
	if strings.TrimSpace(cfg.Endpoint) == "" ||
		strings.TrimSpace(cfg.APIKey) == "" {
		return nil, ErrNotConfigured
	}

	opts := []rosetta.Option{
		rosetta.WithEndpoint(strings.TrimSpace(cfg.Endpoint)),
		rosetta.WithAPIKey(strings.TrimSpace(cfg.APIKey)),
		// The output-cap field name is pinned rather than probed: the probe
		// costs an extra round trip on the first call, and every endpoint this
		// tool targets accepts max_tokens.
		rosetta.WithMaxTokensField("max_tokens"),
		// Thinking-capable models are common now, but planning does not need
		// reasoning traces and they consume the output budget. Leaving
		// Thinking nil disables it.
	}
	switch strings.TrimSpace(cfg.Protocol) {
	case "", "openai-chat":
		opts = append(opts, rosetta.WithProtocol(rosetta.ProtoOpenAIChat))
	case "openai-responses":
		opts = append(opts, rosetta.WithProtocol(rosetta.ProtoOpenAIResponses))
	case "anthropic":
		opts = append(opts, rosetta.WithProtocol(rosetta.ProtoAnthropic))
	default:
		return nil, fmt.Errorf("llm: 不支持的协议 %q（可选 openai-chat、openai-responses、anthropic）", cfg.Protocol)
	}

	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	opts = append(opts, rosetta.WithTimeout(timeout))
	// A 401/403 must not be retried pointlessly; the retry budget is kept
	// small so a misconfigured key fails fast and visibly.
	opts = append(opts, rosetta.WithMaxRetries(1))

	client, err := rosetta.NewClient(opts...)
	if err != nil {
		return nil, fmt.Errorf("llm: 创建客户端失败：%w", err)
	}
	return client, nil
}

// Model returns the configured model id.
func (c *Client) Model() string { return c.cfg.Model }

// maxOutput resolves the output cap for one document.
//
// An explicit configuration always wins. Otherwise the cap grows with the
// node count, because a plan's size is driven by how many paragraphs it must
// reference: a 40-paragraph 通知 fits easily in defaultMaxOutput, while a
// 300-paragraph 规划 does not, and truncation is a hard failure (ErrTruncated)
// rather than something the repair loop can recover from.
func (c *Client) maxOutput(doc *docmodel.Document) int {
	if c.cfg.MaxOutputTokens > 0 {
		return c.cfg.MaxOutputTokens
	}
	n, chars := 0, 0
	if doc != nil {
		n = len(doc.ContentNodes())
		for _, node := range doc.ContentNodes() {
			chars += len([]rune(node.Text))
		}
	}
	want := defaultMaxOutput + tokensPerNode*n + tokensPerRune*chars
	if want > maxOutputCeiling {
		want = maxOutputCeiling
	}
	return want
}

// temperature resolves the sampling temperature.
func (c *Client) temperature() *float64 {
	if c.cfg.Temperature != nil {
		return c.cfg.Temperature
	}
	t := float64(defaultTemperature)
	return &t
}

// seed pins sampling for providers that support it. See defaultSeed.
func (c *Client) seed() *int {
	s := defaultSeed
	return &s
}

// Plan asks the model to restructure doc. progress may be nil.
//
// This is the documented entry point; PlanWithOptions is the same call with
// the optional settings.
func (c *Client) Plan(ctx context.Context, doc *docmodel.Document, progress func(Stage)) (*docmodel.Plan, error) {
	return c.PlanWithOptions(ctx, doc, PlanOptions{}, progress)
}

// PlanOptions carries the per-request analysis settings.
type PlanOptions struct {
	// Hint is the user's note about the document's nature ("这是一份通报").
	// It is guidance about intent, never content, and is never written into a
	// node, so it cannot reach the output document.
	Hint string
	// AllowEdits permits the model to propose content corrections —
	// 错别字, 语病, 用词, 标点, 断句 — in addition to restructuring.
	//
	// When true the model is told it MUST express every correction as a
	// declared edit with a before/after and a reason. When false it is told, as
	// before, that it may not change a character. Either way an undeclared
	// change fails verification, so this flag widens what may be *proposed*
	// without ever widening what may happen silently.
	AllowEdits bool
	// TrustedSource tells the model the text is already clean, so it should not
	// spend effort hunting for errors. It is the default for a document the
	// user has not flagged.
	TrustedSource bool
	// Partial receives snapshots of the plan while the model is still writing.
	// A snapshot is not a verdict: the arrays can be incomplete, and only the
	// final answer is normalized and verified. It exists so the workbench can
	// show chapters and review items as they arrive instead of waiting for the
	// entire JSON document.
	Partial func(PartialPlan)
}

// PartialPlan is an incremental view of the model's answer.
//
// Reset marks the start of a new attempt (a repair round or a retry without
// response_format). The client should clear any provisional rows when it sees
// Reset, then apply the next snapshot.
type PartialPlan struct {
	Plan  *docmodel.Plan
	Edits []docmodel.Edit
	Reset bool
}

// PlanWithHint is retained for callers that only need a hint.
func (c *Client) PlanWithHint(
	ctx context.Context,
	doc *docmodel.Document,
	hint string,
	progress func(Stage),
) (*docmodel.Plan, error) {
	return c.PlanWithOptions(ctx, doc, PlanOptions{Hint: hint}, progress)
}

// PlanWithOptions asks the model to restructure doc.
func (c *Client) PlanWithOptions(
	ctx context.Context,
	doc *docmodel.Document,
	opts PlanOptions,
	progress func(Stage),
) (*docmodel.Plan, error) {
	if c == nil || c.client == nil {
		return nil, ErrNotConfigured
	}
	if doc == nil {
		return nil, errors.New("llm: 缺少文档")
	}
	if len(doc.ContentNodes()) == 0 {
		return nil, errors.New("llm: 文档中没有可梳理的正文内容")
	}

	notify := func(name, msg string) {
		if progress != nil {
			progress(Stage{Name: name, Message: msg})
		}
	}

	partial := &partialEmitter{doc: doc, publish: opts.Partial}
	mustRecompose := needsRecompose(doc, opts.AllowEdits)

	notify(StageConnect, fmt.Sprintf("正在连接模型 %s…", c.cfg.Model))

	// The conversation is built up across repair rounds, so the model can see
	// its own previous answer and the specific objections to it. Replaying the
	// exchange is what makes the repair round effective: a fresh prompt would
	// often reproduce the same mistake.
	messages := []rosetta.Message{
		rosetta.User(buildUserPrompt(doc, opts.Hint, opts.AllowEdits)),
	}

	var (
		lastPlan   *docmodel.Plan
		lastReport *docmodel.Report
		rounds     int
	)

	for attempt := 0; attempt <= maxRepairRounds; attempt++ {
		if err := ctx.Err(); err != nil {
			return lastPlan, &PlanError{Plan: lastPlan, Report: lastReport, Cause: err, Rounds: rounds}
		}

		if attempt == 0 {
			notify(StageAnalyze, "正在分析文档结构…")
		} else {
			notify(StageRepair, fmt.Sprintf("正在修正结构方案（第 %d 次）…", attempt))
		}

		resp, calls, err := c.chat(ctx, doc, opts.AllowEdits, messages, partial.chunk, partial.reset)
		partial.flush()
		rounds += calls
		if err != nil {
			return lastPlan, &PlanError{Plan: lastPlan, Report: lastReport, Cause: classify(err), Rounds: rounds}
		}
		if resp.StopReason == rosetta.StopLength {
			return lastPlan, &PlanError{
				Plan: lastPlan, Report: lastReport, Rounds: rounds,
				Cause: fmt.Errorf("%w（可增大 MaxOutputTokens，当前 %d）", ErrTruncated, c.maxOutput(doc)),
			}
		}

		text := resp.Text()
		if strings.TrimSpace(text) == "" {
			return lastPlan, &PlanError{
				Plan: lastPlan, Report: lastReport, Rounds: rounds,
				Cause: errors.New("模型返回了空回答"),
			}
		}

		plan, perr := ParsePlan(text, doc)
		if perr != nil {
			// A malformed answer is worth one more attempt with the parse
			// error spelled out: models frequently recover from a JSON slip
			// when told exactly what was wrong.
			messages = append(messages,
				rosetta.Assistant(text),
				rosetta.User(fmt.Sprintf(
					"你上面的回答无法解析为所需的 JSON：%v\n\n"+
						"请重新输出**只包含一个 JSON 对象**的回答，不要包含解释文字或 Markdown 代码围栏。", perr)),
			)
			continue
		}

		// Structure the model may have omitted: a plan with no sections is a
		// parse success but a useless answer, so it goes through the same
		// repair path as a failed verification rather than being returned.
		notify(StageValidate, "正在校验结构方案…")
		report := docmodel.VerifyPlan(doc, plan)
		lastPlan, lastReport = plan, report

		// Mechanical problems — a paragraph not listed, an id listed twice, a
		// heading title paraphrased, a child level not deepened — are decidable
		// from the document alone, so they are fixed locally instead of spending
		// a model round that may return the same mistake.
		//
		// This runs on *valid* plans too, not just failing ones. A plan can pass
		// verification with warnings, and two of those warnings are defects the
		// user reported: a model-authored section title sitting above the
		// source's own heading paragraph prints the heading twice, and a
		// rewritten heading never appears in the review list. Both are
		// repairable exactly, so returning the plan untouched would leave the
		// defect for the user to discover in the exported file.
		//
		// The fixes are attached to the plan as notes, because a repair the user
		// cannot see would be a silent change.
		fixed, fixes := baseline.Repair(doc, plan)
		candidate, verdict := plan, report
		if len(fixes) > 0 {
			frep := docmodel.VerifyPlan(doc, fixed)
			// Prefer the locally repaired plan when it is valid. When the
			// original was already invalid, the repaired version is also the
			// better starting point for the next model round even if it still
			// fails, because it has fewer mechanical defects.
			if frep.OK() || !report.OK() {
				candidate, verdict = fixed, frep
			}
		}
		if len(fixes) > 0 {
			candidate.Notes = append(candidate.Notes, fixes...)
		}

		// A run-on document is not acceptable just because every node is
		// referenced. Without a recomposition the model has preserved the
		// one-block shape the user asked to fix, so this goes through a
		// targeted repair round rather than being returned as success.
		missingRecompose := mustRecompose && len(candidate.Recompose) == 0
		if verdict.OK() && !missingRecompose {
			candidate.Edits = dropRecomposeConflicts(
				candidate.Recompose, mergeEdits(doc, candidate.Edits))
			candidate.Model = c.cfg.Model
			msg := "分析完成"
			if len(fixes) > 0 && !report.OK() {
				msg = fmt.Sprintf("分析完成（本地修正了 %d 处结构问题）", len(fixes))
			}
			notify(StageComplete, msg)
			return candidate, nil
		}

		plan, report = candidate, verdict
		lastPlan, lastReport = candidate, verdict

		if attempt == maxRepairRounds {
			break
		}
		next := repairPrompt(report, doc, opts.AllowEdits)
		if missingRecompose {
			next = recomposeRepairPrompt(doc)
		}
		messages = append(messages, rosetta.Assistant(text), rosetta.User(next))
	}

	return lastPlan, &PlanError{Plan: lastPlan, Report: lastReport, Rounds: rounds}
}

// chat sends one planning request, retrying once without response_format if
// the endpoint says it does not know that field.
//
// It returns the number of model calls actually made, so the caller's repair
// budget counts what it spent rather than how many loop iterations ran.
func (c *Client) chat(
	ctx context.Context,
	doc *docmodel.Document,
	allowEdits bool,
	messages []rosetta.Message,
	onText func(string),
	onReset func(),
) (*rosetta.ChatResponse, int, error) {
	req := &rosetta.ChatRequest{
		Model:           c.cfg.Model,
		System:          systemPrompt(doc, allowEdits),
		Messages:        messages,
		MaxOutputTokens: c.maxOutput(doc),
		Temperature:     c.temperature(),
		Seed:            c.seed(),
	}
	if !c.noJSONMode.Load() {
		req.ResponseFormat = planResponseFormat()
	}

	calls := 1
	streamOnce := func() (*rosetta.ChatResponse, error) {
		// rosetta's WithTimeout bounds unary calls only; a stream otherwise
		// inherits just the caller's context. The configured timeout must still
		// bound a silent endpoint, or a request that accepts the connection and
		// never answers would hang until the process is restarted.
		streamCtx := ctx
		timeout := c.cfg.Timeout
		if timeout <= 0 {
			timeout = defaultTimeout
		}
		if timeout > 0 {
			var cancel context.CancelFunc
			streamCtx, cancel = context.WithTimeout(ctx, timeout)
			defer cancel()
		}
		if onReset != nil {
			onReset()
		}
		stream, err := c.client.ChatStream(streamCtx, req)
		if err != nil {
			return nil, err
		}
		defer stream.Close()
		for stream.Next() {
			ev := stream.Event()
			if ev != nil && ev.Type == rosetta.EventTextDelta && ev.Text != "" && onText != nil {
				onText(ev.Text)
			}
		}
		if err := stream.Err(); err != nil {
			return nil, err
		}
		return stream.Partial(), nil
	}

	resp, err := streamOnce()
	if err == nil {
		return resp, calls, nil
	}
	// Some compatible gateways accept `stream:true` and answer with one whole
	// JSON body. rosetta normally converts that into a one-shot stream, but a
	// gateway can also reject streaming outright. Only that case falls back to
	// unary; every other failure (auth, network, malformed answer) keeps its
	// error so the retry below and the caller see the real cause.
	if isStreamRejection(err) {
		resp, uerr := c.client.Chat(ctx, req)
		if uerr == nil {
			return resp, calls, nil
		}
		err = uerr
	}
	// A provider that rejects response_format is not broken — it simply does
	// not implement the field. Dropping it for the rest of this client's life
	// makes the endpoint usable, where surfacing the 400 would make analysis
	// impossible on that gateway.
	if req.ResponseFormat == nil || !rejectsResponseFormat(err) || c.noJSONMode.Swap(true) {
		return nil, calls, err
	}
	calls++
	req.ResponseFormat = nil
	resp, err = streamOnce()
	return resp, calls, err
}

// isStreamRejection reports whether err is the endpoint saying it cannot
// stream. A network or auth failure is not a streaming problem and must not
// be turned into a second request that fails the same way.
func isStreamRejection(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, rosetta.ErrNotSupported) {
		return true
	}
	var apiErr *rosetta.APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	if apiErr.StatusCode != 400 && apiErr.StatusCode != 404 && apiErr.StatusCode != 422 {
		return false
	}
	s := strings.ToLower(apiErr.Message + " " + apiErr.Code + " " + apiErr.Type + " " + apiErr.Param)
	for _, hint := range []string{"stream", "sse", "event-stream"} {
		if strings.Contains(s, hint) {
			return true
		}
	}
	return false
}

// partialEmitter turns streamed text into throttled, complete-object
// snapshots. Parsing on every token would spend more time in the scanner than
// the model spends producing tokens, so snapshots are emitted at a small
// interval; the final flush catches the remainder.
type partialEmitter struct {
	doc     *docmodel.Document
	publish func(PartialPlan)
	raw     strings.Builder
	last    time.Time
	pending bool
	lastSig string
}

func (p *partialEmitter) reset() {
	p.raw.Reset()
	p.last = time.Time{}
	p.pending = false
	p.lastSig = ""
	if p.publish != nil {
		p.publish(PartialPlan{Reset: true})
	}
}

func (p *partialEmitter) chunk(text string) {
	p.raw.WriteString(text)
	p.pending = true
	now := time.Now()
	if !p.last.IsZero() && now.Sub(p.last) < 120*time.Millisecond {
		return
	}
	p.emit()
	p.last = now
}

func (p *partialEmitter) flush() {
	if p.pending {
		p.emit()
	}
}

func (p *partialEmitter) emit() {
	p.pending = false
	if p.publish == nil || p.raw.Len() == 0 {
		return
	}
	plan, edits := ParsePartialPlan(p.raw.String(), p.doc)
	if plan == nil {
		return
	}
	// The stream emits at a fixed interval, so a long run of text inside one
	// object produces the same complete-object snapshot repeatedly. Sending
	// those duplicates makes the browser re-render the whole review list for
	// no visible change; compare the serialized snapshot instead.
	sig, err := json.Marshal(struct {
		Plan  *docmodel.Plan  `json:"plan"`
		Edits []docmodel.Edit `json:"edits"`
	}{Plan: plan, Edits: edits})
	if err == nil {
		next := string(sig)
		if next == p.lastSig {
			return
		}
		p.lastSig = next
	}
	p.publish(PartialPlan{Plan: plan, Edits: edits})
}

// ListModels fetches the endpoint's model catalog, so the UI can offer a list
// instead of asking the user to remember an exact model id.
func (c *Client) ListModels(ctx context.Context) ([]string, error) {
	if c == nil || c.client == nil {
		return nil, ErrNotConfigured
	}
	infos, err := c.client.ListModels(ctx)
	if err != nil {
		return nil, classify(err)
	}
	out := make([]string, 0, len(infos))
	for _, m := range infos {
		if m.ID != "" {
			out = append(out, m.ID)
		}
	}
	return out, nil
}

// classify turns an SDK error into a message that tells the user what to do.
//
// The distinction that matters most is *whose fault* it is: a bad key needs the
// user to change a setting, while an upstream fault needs a retry. Telling
// them apart in the message is what stops a user from re-entering a correct
// key because the provider happened to be down.
func classify(err error) error {
	if err == nil {
		return nil
	}

	var apiErr *rosetta.APIError
	if errors.As(err, &apiErr) {
		switch {
		case apiErr.StatusCode == 401 || apiErr.StatusCode == 403:
			return fmt.Errorf("API Key 无效或无权限（HTTP %d）：请在设置中检查密钥。原始信息：%v",
				apiErr.StatusCode, err)
		case apiErr.StatusCode == 404:
			return fmt.Errorf("接口或模型不存在（HTTP 404）：请检查接口地址与模型名称。原始信息：%v", err)
		case apiErr.StatusCode == 429:
			return fmt.Errorf("请求过于频繁（HTTP 429）：请稍后重试。原始信息：%v", err)
		case apiErr.StatusCode >= 500:
			return fmt.Errorf("模型服务端错误（HTTP %d）：请稍后重试。原始信息：%v",
				apiErr.StatusCode, err)
		default:
			return fmt.Errorf("模型接口返回错误（HTTP %d）：%v", apiErr.StatusCode, err)
		}
	}

	switch {
	case errors.Is(err, rosetta.ErrNoAPIKey):
		return fmt.Errorf("%w：请填写 API Key", ErrNotConfigured)
	case errors.Is(err, rosetta.ErrContextTooLong):
		return fmt.Errorf("文档过长，超出模型上下文窗口：请拆分文档后重试（%v）", err)
	case errors.Is(err, rosetta.ErrUpstreamMalformed):
		return fmt.Errorf("模型返回了无法解析的内容（可能被网关截断）：%v", err)
	case errors.Is(err, rosetta.ErrStreamTruncated):
		return fmt.Errorf("连接被中断：%v", err)
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return fmt.Errorf("请求已取消或超时：%v", err)
	case errors.Is(err, rosetta.ErrInvalidRequest):
		return fmt.Errorf("请求不合法：%v", err)
	}
	return err
}
