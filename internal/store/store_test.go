package store

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"govdoc/internal/docmodel"
)

func testDoc() *docmodel.Document {
	return &docmodel.Document{
		SourceName: "t.docx", Format: "docx",
		Nodes: []docmodel.Node{{ID: "n0001", Kind: docmodel.KindParagraph, Text: "正文"}},
	}
}

// ---------------------------------------------------------------- sessions

func TestPutAndGetSession(t *testing.T) {
	s := New(0)
	s.PutSession("d1", testDoc())

	sess, err := s.Session("d1")
	if err != nil {
		t.Fatalf("Session: %v", err)
	}
	if sess.Doc.SourceName != "t.docx" {
		t.Errorf("文档未正确保存: %+v", sess.Doc)
	}
	if sess.Created.IsZero() || sess.Updated.IsZero() {
		t.Error("时间戳未设置")
	}
}

// TestUnknownSessionIsNotFound: after a restart every id is gone, so the
// handler must be able to distinguish "never existed" from "server error"
// and answer 404 with a message telling the user to re-upload.
func TestUnknownSessionIsNotFound(t *testing.T) {
	s := New(0)
	_, err := s.Session("nope")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("期望 ErrNotFound, 得到 %v", err)
	}
}

func TestUpdateSession(t *testing.T) {
	s := New(0)
	s.PutSession("d1", testDoc())

	plan := &docmodel.Plan{Model: "test-model"}
	report := &docmodel.Report{ContentNodes: 1, CoveredNodes: 1}
	got, err := s.UpdateSession("d1", func(sess *Session) {
		sess.Plan = plan
		sess.Report = report
	})
	if err != nil {
		t.Fatalf("UpdateSession: %v", err)
	}
	if got.Plan.Model != "test-model" {
		t.Errorf("方案未保存: %+v", got.Plan)
	}
	if got.Report.CoveredNodes != 1 {
		t.Errorf("校验结果未保存: %+v", got.Report)
	}
}

// TestUpdateSessionUnknownIDTouchesNothing: the only failure mode is an
// unknown id, and it must not partially apply.
func TestUpdateSessionUnknownIDTouchesNothing(t *testing.T) {
	s := New(0)
	s.PutSession("d1", testDoc())
	_, _ = s.UpdateSession("d1", func(sess *Session) {
		sess.Plan = &docmodel.Plan{Model: "original"}
	})

	called := false
	_, err := s.UpdateSession("nope", func(*Session) { called = true })
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("期望 ErrNotFound, 得到 %v", err)
	}
	if called {
		t.Error("未知 id 不应执行回调")
	}
	sess, _ := s.Session("d1")
	if sess.Plan.Model != "original" {
		t.Errorf("其他会话不应被改动, 实际 %q", sess.Plan.Model)
	}
}

// TestUpdateSessionStoresInvalidPlanOnPurpose: the UI must be able to keep an
// unrenderable plan so the user can see the diagnostics and fix it. Refusing
// to store it would make the 校对 workflow impossible.
func TestUpdateSessionStoresInvalidPlanOnPurpose(t *testing.T) {
	s := New(0)
	s.PutSession("d1", testDoc())
	// An empty plan is unrenderable (content uncovered).
	bad := &docmodel.Plan{}
	report := docmodel.VerifyPlan(testDoc(), bad)
	if report.OK() {
		t.Fatal("测试前提错误：空方案本应不可渲染")
	}
	if _, err := s.UpdateSession("d1", func(sess *Session) {
		sess.Plan = bad
		sess.Report = report
	}); err != nil {
		t.Fatalf("UpdateSession: %v", err)
	}
	sess, _ := s.Session("d1")
	if sess.Report.OK() {
		t.Error("不可渲染的方案与结论都应被保存，以便前端展示原因")
	}
}

// TestSessionsPreserveInsertionOrder: map iteration in Go is randomised, so
// a UI listing built straight from the map would reshuffle on every refresh.
func TestSessionsPreserveInsertionOrder(t *testing.T) {
	s := New(0)
	for _, id := range []string{"a", "b", "c", "d", "e"} {
		s.PutSession(id, testDoc())
	}
	got := s.Sessions()
	want := []string{"a", "b", "c", "d", "e"}
	if len(got) != len(want) {
		t.Fatalf("Sessions = %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("顺序错误: %v, 期望 %v", got, want)
		}
	}
}

// TestSessionsReturnsACopy: a caller mutating the returned slice must not
// corrupt the store's own ordering.
func TestSessionsReturnsACopy(t *testing.T) {
	s := New(0)
	s.PutSession("a", testDoc())
	got := s.Sessions()
	got[0] = "mutated"
	if again := s.Sessions(); again[0] != "a" {
		t.Errorf("Sessions 未返回副本: %v", again)
	}
}

// ---------------------------------------------------------------- files

func TestPutAndGetFile(t *testing.T) {
	s := New(0)
	s.PutFile(&File{ID: "f1", Filename: "公文.docx", MIME: "application/vnd.openxmlformats-officedocument.wordprocessingml.document", Data: []byte("PK")})

	f, err := s.File("f1")
	if err != nil {
		t.Fatalf("File: %v", err)
	}
	if f.Filename != "公文.docx" || string(f.Data) != "PK" {
		t.Errorf("文件内容错误: %+v", f)
	}
	if f.Created.IsZero() {
		t.Error("未设置创建时间")
	}
}

func TestUnknownFileIsNotFound(t *testing.T) {
	s := New(0)
	_, err := s.File("nope")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("期望 ErrNotFound, 得到 %v", err)
	}
	// The message must tell the user what to do, since eviction is a normal
	// occurrence rather than a bug.
	if msg := err.Error(); !contains(msg, "重新导出") {
		t.Errorf("错误信息应提示重新导出, 实际 %q", msg)
	}
}

// TestFileEvictionIsBounded: a user tuning a document may export repeatedly,
// and each output is a whole .docx. Without a bound the process would grow
// without limit.
func TestFileEvictionIsBounded(t *testing.T) {
	const cap = 4
	s := New(cap)
	for i := 0; i < 20; i++ {
		s.PutFile(&File{
			ID:      fmt.Sprintf("f%02d", i),
			Data:    []byte("x"),
			Created: time.Now().Add(time.Duration(i) * time.Millisecond),
		})
	}
	if got := s.FileCount(); got != cap {
		t.Fatalf("文件数 = %d, 期望上限 %d", got, cap)
	}
	// The newest must survive and the oldest must be gone.
	if _, err := s.File("f19"); err != nil {
		t.Errorf("最新的文件应保留: %v", err)
	}
	if _, err := s.File("f00"); !errors.Is(err, ErrNotFound) {
		t.Errorf("最旧的文件应被淘汰, 得到 %v", err)
	}
}

// TestDefaultFileCap: maxFiles<=0 must not mean "unbounded".
func TestDefaultFileCap(t *testing.T) {
	s := New(0)
	if s.maxFiles != 32 {
		t.Errorf("默认上限 = %d, 期望 32", s.maxFiles)
	}
}

// ---------------------------------------------------------------- jobs

// TestJobPublishesHistoryToLateSubscriber is the reason jobs exist: a browser
// that reloads mid-analysis must still see the stages it missed.
func TestJobPublishesHistoryToLateSubscriber(t *testing.T) {
	s := New(0)
	j := s.NewJob("j1", "d1")

	j.Publish(StageUpdate{Name: "connect", Message: "正在连接模型…"})
	j.Publish(StageUpdate{Name: "analyze", Message: "正在分析文档结构…"})

	history, ch, done := j.Subscribe()
	if done {
		t.Fatal("任务尚未结束, done 应为 false")
	}
	defer j.Unsubscribe(ch)

	if len(history) != 2 {
		t.Fatalf("历史长度 = %d, 期望 2", len(history))
	}
	if history[0].Name != "connect" || history[1].Name != "analyze" {
		t.Errorf("历史顺序错误: %+v", history)
	}
}

func TestJobLiveUpdates(t *testing.T) {
	s := New(0)
	j := s.NewJob("j1", "d1")
	_, ch, _ := j.Subscribe()
	defer j.Unsubscribe(ch)

	go func() {
		time.Sleep(10 * time.Millisecond)
		j.Publish(StageUpdate{Name: "analyze", Message: "分析中"})
	}()

	select {
	case u := <-ch:
		if u.Name != "analyze" {
			t.Errorf("收到 %+v", u)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("未收到推送")
	}
}

// TestJobFinishAttachesReport: the client needs the verdict together with the
// plan, so it can never display a plan without knowing whether it renders.
func TestJobFinishAttachesReport(t *testing.T) {
	s := New(0)
	j := s.NewJob("j1", "d1")
	// Subscribe while running so the terminal frame is delivered live.
	_, ch, running := j.Subscribe()
	if running {
		t.Fatal("任务尚未结束")
	}
	defer j.Unsubscribe(ch)

	plan := &docmodel.Plan{Model: "m"}
	report := &docmodel.Report{ContentNodes: 2, CoveredNodes: 2}
	j.Finish(plan, report, nil)

	if !j.Done() {
		t.Fatal("任务应已结束")
	}
	gotPlan, gotReport, err, done := j.Result()
	if !done || err != nil {
		t.Fatalf("Result: done=%v err=%v", done, err)
	}
	if gotPlan == nil || gotPlan.Model != "m" {
		t.Errorf("结果方案错误: %+v", gotPlan)
	}
	if gotReport == nil || gotReport.CoveredNodes != 2 {
		t.Errorf("结果校验错误: %+v", gotReport)
	}

	// The live subscriber must receive a terminal frame carrying the report.
	select {
	case u := <-ch:
		if !u.Done {
			t.Errorf("末帧应标记 Done: %+v", u)
		}
		if u.Report == nil || u.Report.CoveredNodes != 2 {
			t.Errorf("末帧应携带校验结果: %+v", u.Report)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("未收到完成帧")
	}

	// A subscriber arriving after completion gets the full history including
	// the terminal frame, and no channel.
	hist2, ch2, done2 := j.Subscribe()
	if !done2 {
		t.Error("已完成的任务 Subscribe 应报告 done")
	}
	if ch2 != nil {
		t.Error("已完成的任务不应返回通道")
	}
	if len(hist2) == 0 {
		t.Fatal("已完成的任务应保留历史")
	}
	tail := hist2[len(hist2)-1]
	if !tail.Done {
		t.Errorf("末帧应为完成帧: %+v", tail)
	}
	if tail.Report == nil || tail.Report.CoveredNodes != 2 {
		t.Errorf("末帧应携带校验结果: %+v", tail.Report)
	}
}

func TestJobFinishWithError(t *testing.T) {
	s := New(0)
	j := s.NewJob("j1", "d1")
	j.Finish(nil, nil, errors.New("上游 401"))

	plan, report, err, done := j.Result()
	if !done {
		t.Fatal("任务应已结束")
	}
	if plan != nil || report != nil {
		t.Error("失败时不应有方案或校验结果")
	}
	if err == nil || !contains(err.Error(), "401") {
		t.Errorf("错误应被保留: %v", err)
	}
	hist, _, _ := j.Subscribe()
	if tail := hist[len(hist)-1]; tail.Error == "" {
		t.Error("末帧应携带错误信息")
	}
}

// TestJobFinishIsIdempotent: a racing timeout and a late result must not
// double-close the done channel (which would panic).
func TestJobFinishIsIdempotent(t *testing.T) {
	s := New(0)
	j := s.NewJob("j1", "d1")
	j.Finish(&docmodel.Plan{Model: "first"}, nil, nil)
	j.Finish(&docmodel.Plan{Model: "second"}, nil, nil) // must not panic

	plan, _, _, _ := j.Result()
	if plan.Model != "first" {
		t.Errorf("重复 Finish 不应覆盖结果, 实际 %q", plan.Model)
	}
}

// TestPublishAfterFinishIsIgnored: a stray stage from a goroutine that lost
// the race must not appear after the terminal frame.
func TestPublishAfterFinishIsIgnored(t *testing.T) {
	s := New(0)
	j := s.NewJob("j1", "d1")
	j.Finish(nil, nil, nil)
	before, _, _ := j.Subscribe()
	j.Publish(StageUpdate{Name: "late", Message: "不应出现"})
	after, _, _ := j.Subscribe()
	if len(after) != len(before) {
		t.Errorf("完成后不应再接受推送: %d → %d", len(before), len(after))
	}
}

// TestJobWait covers the blocking accessor used by tests and the CLI.
func TestJobWait(t *testing.T) {
	s := New(0)
	j := s.NewJob("j1", "d1")
	go func() {
		time.Sleep(20 * time.Millisecond)
		j.Finish(nil, nil, nil)
	}()
	if !j.Wait(2 * time.Second) {
		t.Fatal("Wait 应在任务结束时返回 true")
	}
	if j.Wait(10*time.Millisecond) != true {
		t.Error("已完成的任务 Wait 应立即返回 true")
	}

	s2 := New(0)
	j2 := s2.NewJob("j2", "d2")
	if j2.Wait(20 * time.Millisecond) {
		t.Error("未完成的任务 Wait 应超时返回 false")
	}
}

func TestUnknownJob(t *testing.T) {
	s := New(0)
	if _, err := s.Job("nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("期望 ErrNotFound, 得到 %v", err)
	}
}

// TestSlowSubscriberDoesNotStallAnalysis: a stalled SSE client must not block
// the analysis goroutine. The publish path drops frames for a full buffer
// instead of blocking, and the client can recover from History.
func TestSlowSubscriberDoesNotStallAnalysis(t *testing.T) {
	s := New(0)
	j := s.NewJob("j1", "d1")

	stalledHistory, stalledCh, _ := j.Subscribe() // never drained
	defer j.Unsubscribe(stalledCh)
	_ = stalledHistory

	done := make(chan struct{})
	go func() {
		for i := 0; i < 200; i++ { // far more than the 32-slot buffer
			j.Publish(StageUpdate{Name: "analyze", Message: fmt.Sprintf("第%d步", i)})
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("慢速订阅者阻塞了发布者")
	}
}

// TestUnsubscribeIsSafe: the SSE handler unsubscribes on client disconnect,
// which may race with a publish.
func TestUnsubscribeIsSafe(t *testing.T) {
	s := New(0)
	j := s.NewJob("j1", "d1")
	_, ch, _ := j.Subscribe()
	j.Unsubscribe(ch)
	j.Unsubscribe(ch) // double unsubscribe must not panic
	j.Unsubscribe(nil)

	j.Publish(StageUpdate{Name: "x"}) // must not panic with no waiters
}

// TestCleanupJobsRemovesSettledJobs: a long-lived server must not accumulate
// job records for every analysis ever run.
func TestCleanupJobsRemovesSettledJobs(t *testing.T) {
	s := New(0)
	old := s.NewJob("old", "d1")
	old.Finish(nil, nil, nil)
	fresh := s.NewJob("fresh", "d2")
	fresh.Finish(nil, nil, nil)
	// A job that never settled must survive the sweep regardless of age.
	s.NewJob("running", "d3")

	// Backdate the settled jobs past the TTL.
	old.mu.Lock()
	old.finished = time.Now().Add(-2 * time.Hour)
	old.mu.Unlock()
	fresh.mu.Lock()
	fresh.finished = time.Now()
	fresh.mu.Unlock()

	n := s.CleanupJobs(time.Hour)
	if n != 1 {
		t.Errorf("清理数 = %d, 期望 1", n)
	}
	if _, err := s.Job("old"); !errors.Is(err, ErrNotFound) {
		t.Error("过期的已结束任务应被清理")
	}
	if _, err := s.Job("fresh"); err != nil {
		t.Error("未过期的任务不应被清理")
	}
	if _, err := s.Job("running"); err != nil {
		t.Error("未结束的任务不应被清理")
	}
}

// TestCleanupKeepsJobsWithSubscribers: dropping a job a live SSE client is
// reading would break that client's stream.
func TestCleanupKeepsJobsWithSubscribers(t *testing.T) {
	s := New(0)
	j := s.NewJob("j1", "d1")
	_, ch, _ := j.Subscribe()
	defer j.Unsubscribe(ch)
	j.Finish(nil, nil, nil)
	j.mu.Lock()
	j.finished = time.Now().Add(-2 * time.Hour)
	j.mu.Unlock()

	if n := s.CleanupJobs(time.Hour); n != 0 {
		t.Errorf("仍有订阅者时不应清理, 清理了 %d 个", n)
	}
}

// ---------------------------------------------------------------- concurrency

// TestStoreConcurrentAccess exercises the lock discipline under -race, which
// is how the parallel HTTP handlers will use it.
func TestStoreConcurrentAccess(t *testing.T) {
	s := New(8)
	const workers = 16
	done := make(chan struct{})

	for w := 0; w < workers; w++ {
		go func(w int) {
			defer func() { done <- struct{}{} }()
			id := fmt.Sprintf("d%d", w)
			s.PutSession(id, testDoc())
			_, _ = s.UpdateSession(id, func(sess *Session) {
				sess.Plan = &docmodel.Plan{Model: id}
			})
			_, _ = s.Session(id)
			s.Sessions()
			s.PutFile(&File{ID: fmt.Sprintf("f%d", w), Data: []byte("x")})
			_, _ = s.File(fmt.Sprintf("f%d", w))
			j := s.NewJob(fmt.Sprintf("j%d", w), id)
			j.Publish(StageUpdate{Name: "analyze"})
			_, ch, _ := j.Subscribe()
			j.Unsubscribe(ch)
			j.Finish(nil, nil, nil)
			s.CleanupJobs(time.Nanosecond)
		}(w)
	}
	for w := 0; w < workers; w++ {
		<-done
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 ||
		indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
