// plugin: rolling-memory v0.0.58
package rollingmemory

import "testing"

// The engine CLI writes its failure envelope to stdout under --json; the
// console must read that text instead of degrading to the bare exit status.
func TestEngineEnvelopeErrorReadsJSONFailure(t *testing.T) {
	raw := []byte("{\"ok\": false, \"error\": \"daemon /v1/graph failed with status 503: localvault: empty corpus snapshot\"}\n")
	got := engineEnvelopeError(raw)
	want := "daemon /v1/graph failed with status 503: localvault: empty corpus snapshot"
	if got != want {
		t.Fatalf("engineEnvelopeError = %q, want %q", got, want)
	}
}

func TestEngineEnvelopeErrorIgnoresSuccessAndGarbage(t *testing.T) {
	for _, raw := range [][]byte{
		nil,
		[]byte("   "),
		[]byte(`{"ok": true}`),
		[]byte("not json"),
		[]byte(`{"ok": false, "error": "   "}`),
	} {
		if got := engineEnvelopeError(raw); got != "" {
			t.Fatalf("engineEnvelopeError(%q) = %q, want empty", raw, got)
		}
	}
}
