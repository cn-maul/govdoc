// Command govdoc is the GovDoc 公文 processing tool.
//
// # One binary
//
// The Vue front end is embedded into this executable, so a user copies one
// file and runs it. That matters for a 公文 tool: it is often used on an
// office machine where installing a runtime, a web server or a Node toolchain
// is not an option, and where leaving a directory of static assets lying
// around is an invitation for it to drift out of sync with the binary.
//
// The embed directive lives in the root govdoc package, because //go:embed
// paths are resolved relative to the package directory and cannot traverse
// "..". This command consumes that package's fs.FS.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	govdoc "govdoc"
	"govdoc/internal/config"
	"govdoc/internal/docmodel"
	"govdoc/internal/fonts"
	"govdoc/internal/gbstandard"
	"govdoc/internal/llm"
	"govdoc/internal/server"
)

// version is overridable at build time:
//
//	go build -ldflags "-X main.version=1.2.3" ./cmd/govdoc
var version = ""

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "govdoc: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		addr      = flag.String("addr", "127.0.0.1:8765", "监听地址")
		staticDir = flag.String("static", "", "从磁盘而非内嵌资源提供界面（前端开发用）")
		endpoint  = flag.String("endpoint", os.Getenv("GOVDOC_ENDPOINT"), "LLM 接口地址，如 https://api.deepseek.com/v1")
		apiKey    = flag.String("api-key", os.Getenv("GOVDOC_API_KEY"), "LLM API Key（也可用环境变量 GOVDOC_API_KEY）")
		model     = flag.String("model", os.Getenv("GOVDOC_MODEL"), "模型名称，如 deepseek-chat")
		protocol  = flag.String("protocol", os.Getenv("GOVDOC_PROTOCOL"), "协议：openai-chat / openai-responses / anthropic")
		timeout   = flag.Duration("timeout", 120*time.Second, "单次模型调用超时")
		direction = flag.String("direction", "下行文", "行文方向：下行文 / 上行文 / 平行文")
		verbose   = flag.Bool("v", false, "输出调试日志")
		showVer   = flag.Bool("version", false, "显示版本后退出")
		openOnly  = flag.Bool("print-addr", false, "仅打印监听地址后退出（用于脚本探测）")
		noSave    = flag.Bool("no-save", false, "本次运行不读取也不写入已保存的设置")
		showWhere = flag.Bool("settings-path", false, "打印设置文件位置后退出")
	)
	flag.Usage = usage
	flag.Parse()

	if *showVer {
		fmt.Println(displayVersion())
		return nil
	}
	if *showWhere {
		p, err := config.DefaultPath()
		if err != nil {
			return err
		}
		fmt.Println(p)
		return nil
	}
	if *openOnly {
		fmt.Println(*addr)
		return nil
	}

	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	dir, err := parseDirection(*direction)
	if err != nil {
		return err
	}

	spec := gbstandard.Default()
	spec.Direction = dir

	// Settings persistence: the user asked for a connection they configure once
	// and keep. -no-save exists for a locked-down or shared machine where
	// writing a key to disk is not wanted.
	var settings *config.Store
	if !*noSave {
		settings = config.New("")
		if settings.Disabled() {
			logger.Warn("无法确定用户配置目录，本次设置不会被保存")
		}
	}

	cfg := server.Config{
		Settings: settings,
		Addr:     *addr,
		LLM: server.LLMConfig{
			Endpoint: *endpoint,
			APIKey:   *apiKey,
			Model:    *model,
			Protocol: *protocol,
			Timeout:  *timeout,
		},
		Spec:      spec,
		StaticDir: *staticDir,
		Logger:    logger,
	}

	srv := server.New(cfg)

	// Ask the server what is *actually* in force, not what the flags said:
	// server.New has already merged any saved settings over them. Building the
	// client from the raw flags here was a real bug — a key saved through the UI
	// loaded into the server correctly but never reached the model client, so
	// the user still had to re-enter it after every restart, which is exactly
	// what they asked to be rid of.
	effective := srv.EffectiveLLM()

	// The model client is built even when unconfigured, so a user can start
	// the tool first and fill in the connection details in the UI.
	analyzer, err := newAnalyzer(logger, effective)
	if err != nil {
		return err
	}
	srv.SetAnalyzer(analyzer)

	// The banner must show the same thing, or it contradicts the UI.
	cfg.LLM = effective
	cfg.Spec = srv.EffectiveSpec()

	// Resolve the UI source once, so a packaging error is reported at startup
	// rather than as a 404 the user discovers after uploading a document.
	staticFS, err := uiFS(*staticDir)
	if err != nil {
		return err
	}

	handler := srv.Handler(staticFS)

	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		return fmt.Errorf("无法监听 %s：%w", *addr, err)
	}
	// Report the resolved address: with ":0" in a script the caller needs the
	// port the OS actually chose.
	actual := ln.Addr().String()
	url := "http://" + actual

	// cfg.LLM and cfg.Spec were replaced above with the effective values, so the
	// log line and the banner describe what the server will actually do.
	logger.Info("GovDoc 已启动",
		"version", displayVersion(),
		"url", url,
		"llm_configured", cfg.LLM.Endpoint != "" && cfg.LLM.APIKey != "" && cfg.LLM.Model != "",
		"direction", cfg.Spec.Direction,
		"fonts_missing", missingFonts(cfg.Spec))

	printBanner(url, cfg.LLM, cfg.Spec)

	httpSrv := &http.Server{
		Handler: handler,
		// No WriteTimeout: the SSE progress stream is a long-lived response,
		// and a write deadline would cut it off mid-analysis.
		ReadHeaderTimeout: 15 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		if err := httpSrv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	// Wait for a termination signal or a serve failure, then drain.
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	select {
	case err := <-errCh:
		return fmt.Errorf("服务异常退出：%w", err)
	case sig := <-stop:
		logger.Info("正在关闭", "signal", sig.String())
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := httpSrv.Shutdown(ctx); err != nil {
		return fmt.Errorf("关闭超时：%w", err)
	}
	return nil
}

// uiFS selects the UI source: an explicit directory for front-end work, or
// the embedded copy that ships in the binary.
//
// It returns an error when the embedded copy is missing, because that means
// the binary was built without its front end — a packaging mistake that would
// otherwise surface as an empty page.
func uiFS(staticDir string) (fs.FS, error) {
	if staticDir != "" {
		if _, err := os.Stat(staticDir); err != nil {
			return nil, fmt.Errorf("界面目录不可用：%w", err)
		}
		return nil, nil // Handler serves from StaticDir in this case.
	}
	sub, err := govdoc.UI()
	if err != nil {
		return nil, fmt.Errorf("内嵌界面资源不可用：%w", err)
	}
	if _, err := fs.Stat(sub, "index.html"); err != nil {
		return nil, fmt.Errorf(
			"二进制中未包含界面（web/dist/index.html 缺失）。" +
				"请先执行：cd web && pnpm install && pnpm run build，然后重新构建 govdoc")
	}
	return sub, nil
}

// newAnalyzer builds the model client adapter.
func newAnalyzer(logger *slog.Logger, cfg server.LLMConfig) (server.Analyzer, error) {
	a := &analyzerAdapter{logger: logger}
	if err := a.Reconfigure(cfg); err != nil {
		// A bad value from the command line is worth reporting immediately;
		// but an *absent* configuration is not an error, since the user may
		// intend to configure it in the UI.
		if !errors.Is(err, llm.ErrNotConfigured) {
			return nil, err
		}
		logger.Info("尚未配置模型连接，可在界面中填写")
	}
	return a, nil
}

// analyzerAdapter bridges llm.Client to server.Analyzer, translating the
// progress and configuration types so neither package imports the other.
type analyzerAdapter struct {
	logger *slog.Logger
	client *llm.Client
	cfg    server.LLMConfig
}

func (a *analyzerAdapter) Reconfigure(cfg server.LLMConfig) error {
	client, err := llm.New(llm.Config{
		Endpoint: cfg.Endpoint,
		APIKey:   cfg.APIKey,
		Model:    cfg.Model,
		Protocol: cfg.Protocol,
		Timeout:  cfg.Timeout,
	})
	if err != nil {
		return err
	}
	a.client, a.cfg = client, cfg
	return nil
}

func (a *analyzerAdapter) Analyze(
	ctx context.Context,
	doc *docmodel.Document,
	opts server.AnalysisOptions,
	progress func(server.Progress),
) (*docmodel.Plan, error) {
	if a.client == nil {
		return nil, llm.ErrNotConfigured
	}
	var cb func(llm.Stage)
	if progress != nil {
		cb = func(s llm.Stage) {
			progress(server.Progress{Name: s.Name, Message: s.Message, Done: s.Done})
		}
	}
	// Both options must be forwarded: the hint is what the user typed about the
	// document's nature, and AllowEdits is what decides whether the model may
	// propose content corrections at all. Dropping either here would make the
	// corresponding control look like it did nothing, which is worse than not
	// offering it.
	return a.client.PlanWithOptions(ctx, doc, llm.PlanOptions{
		Hint:       opts.Hint,
		AllowEdits: opts.AllowEdits,
	}, cb)
}

func usage() {
	fmt.Fprint(os.Stderr, `GovDoc —— 公文格式规范化工具

用  法:
  govdoc [选项]

说  明:
  上传一份文档，GovDoc 会让模型理解其结构并梳理出正确的章节层次，
  在不改动原文的前提下，按《党政机关公文格式》GB/T 9704-2012
  输出字号、行距、页边距等格式合规的公文 .docx。

  原文逐字不变：模型只能引用段落编号来归组章节，不能改写、增删或
  润色任何正文内容。导出前会逐字比对原文与成品，有丢失或改写即拒绝导出。

选  项:
`)
	flag.PrintDefaults()
	fmt.Fprint(os.Stderr, `
环境变量:
  GOVDOC_ENDPOINT   LLM 接口地址
  GOVDOC_API_KEY    LLM API Key
  GOVDOC_MODEL      模型名称
  GOVDOC_PROTOCOL   协议名

示  例:
  govdoc -endpoint https://api.deepseek.com/v1 -api-key sk-xxx -model deepseek-chat
  govdoc -model deepseek-chat          # 在界面中填写 API Key
`)
}

func displayVersion() string {
	if version != "" {
		return version
	}
	return server.Version
}

func parseDirection(s string) (gbstandard.Direction, error) {
	// The Direction constants are themselves the Chinese names, so the empty
	// string is the only extra spelling to accept (meaning "use the default").
	switch d := gbstandard.Direction(strings.TrimSpace(s)); d {
	case "":
		return gbstandard.DirDown, nil
	case gbstandard.DirDown, gbstandard.DirUp, gbstandard.DirParallel:
		return d, nil
	default:
		return "", fmt.Errorf("行文方向 %q 无效，可选：%s、%s、%s",
			s, gbstandard.DirDown, gbstandard.DirUp, gbstandard.DirParallel)
	}
}

func missingFonts(spec gbstandard.Spec) []string {
	return fonts.Missing(spec)
}

// printBanner tells the user where to go and, importantly, whether anything
// needs configuring before the tool is useful.
func printBanner(url string, cfg server.LLMConfig, spec gbstandard.Spec) {
	fmt.Println()
	fmt.Println("  GovDoc 公文格式规范化工具  ", displayVersion())
	fmt.Println("  ────────────────────────────────────────────")
	fmt.Printf("  请在浏览器中打开:  %s\n", url)
	fmt.Println()
	fmt.Printf("  行文方向: %s（发文机关标志距版心上边缘 %.0fmm）\n",
		spec.Direction, spec.Direction.MastheadTopMM())
	fmt.Printf("  页面设置: A4 %.0f×%.0fmm，页边距 上%.0f 下%.0f 左%.0f 右%.0fmm\n",
		spec.Page.WidthMM, spec.Page.HeightMM,
		spec.Page.MarginTopMM, spec.Page.MarginBottomMM,
		spec.Page.MarginLeftMM, spec.Page.MarginRightMM)
	fmt.Printf("  正文: %s三号（%.0fpt），固定行距 %.2fpt，每面%d行×每行%d字\n",
		spec.Styles[docmodel.RoleBody].Font,
		spec.Styles[docmodel.RoleBody].Size.Points(),
		spec.Page.LineSpacingPT(), spec.Page.LinesPerPage, spec.Page.CharsPerLine)
	fmt.Println()

	if cfg.Endpoint == "" || cfg.APIKey == "" || cfg.Model == "" {
		fmt.Println("  ⚠ 尚未配置模型连接，请在界面中填写接口地址、模型与 API Key。")
	} else {
		fmt.Printf("  模型: %s @ %s\n", cfg.Model, cfg.Endpoint)
	}

	if missing := fonts.Missing(spec); len(missing) > 0 {
		fmt.Printf("  ⚠ 本机缺少字体: %s\n", strings.Join(missing, "、"))
		fmt.Println("    生成的公文在打开时会被替换字体，建议安装后再打印。")
	}
	fmt.Println()
}
