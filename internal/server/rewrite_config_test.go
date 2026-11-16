package server

import (
	"encoding/json"
	"net/http"
	"testing"

	"govdoc/internal/api"
	"govdoc/internal/style"
)

// The settings page renders its inputs from the PUT response, so a missing
// rewrite block there would show every budget as 0 and tempt the user to "fix"
// working defaults. The response must echo what is actually in effect.
func TestPutConfigReturnsRewriteLimits(t *testing.T) {
	r := newRig(t, true)
	limits := style.Limits{Enabled: true, FormalMax: 12, MixedMax: 30, ColloquialMax: 90}

	resp, data := r.do(t, http.MethodPut, "/api/config", api.ConfigRequest{Rewrite: &limits})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT /api/config: %d %s", resp.StatusCode, data)
	}
	var out api.ConfigResponse
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Rewrite != limits {
		t.Errorf("响应中的改写幅度 = %+v, 期望 %+v", out.Rewrite, limits)
	}
}

// A negative budget must be rejected rather than clamped. Silently turning -1
// into the default would save a number the user never chose, and the next time
// they opened the page it would look like their edit had been ignored.
func TestPutConfigRejectsOutOfRangeBudget(t *testing.T) {
	r := newRig(t, true)
	bad := style.Limits{Enabled: true, FormalMax: -1, MixedMax: 30, ColloquialMax: 90}

	resp, data := r.do(t, http.MethodPut, "/api/config", api.ConfigRequest{Rewrite: &bad})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("越界值应被拒绝, 实际 %d: %s", resp.StatusCode, data)
	}
}
