package server

import (
	"net/http"
	"testing"

	"govdoc/internal/docmodel"
)

// A plan round-trips through the browser, so it must not be trusted to carry
// the server's policy. A caller that raises rewrite_limits or max_heading_depth
// would otherwise be able to pass the export gate it is supposed to be stopped
// by. This pins the overwrite on the PUT path, which is where a hand-made
// request would do it.
func TestPutPlanCannotRaiseServerPolicy(t *testing.T) {
	r := newRig(t, true)
	up := r.upload(t, "通知.txt", "一、甲\n\n甲段。\n\n二、乙\n\n乙段。\n")
	sess, _ := r.srv.store.Session(up.DocID)

	var ids []string
	for _, n := range sess.Doc.Nodes {
		if n.Kind == docmodel.KindParagraph {
			ids = append(ids, n.ID)
		}
	}

	// Three levels in a document type that allows four is fine; the point is
	// that the client's own numbers are replaced, not that this plan is bad.
	plan := &docmodel.Plan{
		Sections: []docmodel.Section{
			{ID: "s1", Title: "甲", Level: 1, BodyNodeIDs: ids[:1]},
			{ID: "s2", Title: "乙", Level: 1, BodyNodeIDs: ids[1:]},
		},
		RewriteLimits:   docmodel.RewriteLimits{Enabled: true, FormalMax: 100, MixedMax: 100, ColloquialMax: 100},
		MaxHeadingDepth: 99,
	}
	resp, data := r.do(t, http.MethodPut, "/api/documents/"+up.DocID+"/plan", plan)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT plan: %d %s", resp.StatusCode, data)
	}

	stored, _ := r.srv.store.Session(up.DocID)
	if stored.Plan == nil {
		t.Fatal("方案未保存")
	}
	if stored.Plan.MaxHeadingDepth == 99 {
		t.Error("客户端提交的 max_heading_depth 未被服务端策略覆盖")
	}
	if stored.Plan.RewriteLimits.FormalMax == 100 {
		t.Error("客户端提交的 rewrite_limits 未被服务端策略覆盖")
	}
}

// The same must hold for the no-model path: a baseline plan is produced by the
// server, and it still has to carry the policy so preview and export agree with
// it.
func TestBaselinePlanCarriesServerPolicy(t *testing.T) {
	r := newRig(t, false)
	up := r.upload(t, "通知.txt", "一、甲\n\n甲段。\n\n二、乙\n\n乙段。\n")

	body := map[string]any{"baseline": true}
	resp, data := r.do(t, http.MethodPost, "/api/documents/"+up.DocID+"/plan", body)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("baseline plan: %d %s", resp.StatusCode, data)
	}

	sess, _ := r.srv.store.Session(up.DocID)
	if sess.Plan == nil {
		t.Fatal("基线方案未生成")
	}
	if sess.Plan.MaxHeadingDepth == 0 {
		t.Error("基线方案应带上本题材的最大层级")
	}
	if sess.Plan.RewriteLimits.FormalMax == 0 {
		t.Error("基线方案应带上服务端的改写幅度")
	}
}
