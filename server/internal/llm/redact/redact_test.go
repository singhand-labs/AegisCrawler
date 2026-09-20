package redact

import (
	"strings"
	"testing"
	"time"
)

func TestRecordingRedactsPasswordValue(t *testing.T) {
	rec := map[string]any{
		"events": []any{
			map[string]any{"type": "input", "selector": "#pwd", "value": "password: my-secret-password"},
		},
	}
	out := Recording(rec)
	ev := out["events"].([]any)[0].(map[string]any)
	if ev["selector"] != "#pwd" {
		t.Fatalf("selector should be preserved, got %v", ev["selector"])
	}
	got := ev["value"].(string)
	if !strings.Contains(got, "[REDACTED]") {
		t.Fatalf("expected redacted password, got %q", got)
	}
}

func TestRecordingRedactsEmailAndCard(t *testing.T) {
	rec := map[string]any{
		"domSnapshots": []any{
			map[string]any{
				"tag":       "div",
				"outerHTML": "<div>user@example.com bought with 1234567890123456</div>",
			},
		},
	}
	out := Recording(rec)
	snap := out["domSnapshots"].([]any)[0].(map[string]any)
	if snap["tag"] != "div" {
		t.Fatalf("tag name should be preserved, got %v", snap["tag"])
	}
	html := snap["outerHTML"].(string)
	if strings.Contains(html, "user@example.com") {
		t.Fatalf("email should be redacted, got %q", html)
	}
	if strings.Contains(html, "1234567890123456") {
		t.Fatalf("card number should be redacted, got %q", html)
	}
	if !strings.Contains(html, "[REDACTED]") {
		t.Fatalf("expected placeholder in redacted html, got %q", html)
	}
}

func TestRecordingPreservesSelectors(t *testing.T) {
	rec := map[string]any{
		"domSnapshots": []any{
			map[string]any{
				"attributes": map[string]any{
					"id":    "login-form",
					"class": "form",
				},
				"selector": "#login-form",
			},
		},
	}
	out := Recording(rec)
	snap := out["domSnapshots"].([]any)[0].(map[string]any)
	attrs := snap["attributes"].(map[string]any)
	if attrs["id"] != "login-form" || attrs["class"] != "form" {
		t.Fatalf("attribute names/values should be preserved, got %v", attrs)
	}
	if snap["selector"] != "#login-form" {
		t.Fatalf("selector should be preserved, got %v", snap["selector"])
	}
}

func TestRecordingDoesNotMutateInput(t *testing.T) {
	rec := map[string]any{
		"events": []any{
			map[string]any{"type": "input", "value": "secret"},
		},
	}
	_ = Recording(rec)
	ev := rec["events"].([]any)[0].(map[string]any)
	if ev["value"] != "secret" {
		t.Fatal("input recording was mutated")
	}
}

func TestRecordingRedactsNestedSensitiveValue(t *testing.T) {
	rec := map[string]any{
		"domSnapshots": []any{
			map[string]any{
				"text": map[string]any{
					"header": "password: hunter2",
				},
			},
		},
	}
	out := Recording(rec)
	snap := out["domSnapshots"].([]any)[0].(map[string]any)
	text := snap["text"].(map[string]any)
	if !strings.Contains(text["header"].(string), "[REDACTED]") {
		t.Fatalf("expected nested text redaction, got %q", text["header"])
	}
}

func TestRedactStringHandlesMultiplePatterns(t *testing.T) {
	s := "email: alice@example.com, card: 4111111111111111, token: abc123"
	got := redactString(s)
	wantCount := strings.Count(got, "[REDACTED]")
	if wantCount != 3 {
		t.Fatalf("expected 3 redactions, got %d in %q", wantCount, got)
	}
}

func TestAnyPreservesTokenAccountingButRedactsTokenCredentials(t *testing.T) {
	out := Any(map[string]any{
		"inputTokens":  123,
		"outputTokens": 45,
		"apiToken":     "provider-secret",
	}).(map[string]any)
	if out["inputTokens"] != float64(123) || out["outputTokens"] != float64(45) {
		t.Fatalf("token accounting was redacted: %#v", out)
	}
	if out["apiToken"] != "[REDACTED]" {
		t.Fatalf("credential token was not redacted: %#v", out)
	}
}

func TestStringRedactsCredentialFieldsInsideJSONString(t *testing.T) {
	got := String(`{"nested":{"apiToken":"provider-secret"},"safe":"visible"}`)
	if strings.Contains(got, "provider-secret") || !strings.Contains(got, `"[REDACTED]"`) {
		t.Fatalf("JSON-in-string credential bypassed redaction: %s", got)
	}
	if !strings.Contains(got, `"safe":"visible"`) {
		t.Fatalf("JSON-in-string redaction destroyed safe content: %s", got)
	}
}

func TestStringRedactsCredentialFieldsInsidePrefixedAndMalformedJSON(t *testing.T) {
	for _, test := range []struct {
		value  string
		prefix string
	}{
		{value: `provider failed: {"nested":{"apiToken":"provider-secret"},"safe":"visible"} after`, prefix: "provider failed:"},
		{value: `provider failed: {"apiToken":"provider-secret"`, prefix: "provider failed:"},
		{value: `provider failed: {"apiToken":"provider-secret`, prefix: "provider failed:"},
		{value: `error: "apiToken":"provider-secret"`, prefix: "error:"},
		{value: `error: 'apiToken':'provider-secret'`, prefix: "error:"},
		{value: `provider failed: {"api\u0054oken":"provider-secret"}`, prefix: "provider failed:"},
		{value: `provider failed: {"api\u0054oken":"provider-secret`, prefix: "provider failed:"},
	} {
		got := String(test.value)
		if strings.Contains(got, "provider-secret") || !strings.Contains(got, "[REDACTED]") {
			t.Fatalf("embedded credential bypassed redaction: %q", got)
		}
		if !strings.Contains(got, test.prefix) {
			t.Fatalf("embedded redaction destroyed diagnostic prefix: %q", got)
		}
	}
}

func TestStringHandlesLargeMalformedJSONInLinearTime(t *testing.T) {
	done := make(chan struct{})
	go func() {
		_ = String(strings.Repeat("{", 256<<10))
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("malformed provider content caused pathological redaction time")
	}
}

func TestStringPreservesSafeJSONExactly(t *testing.T) {
	value := "{\n  \"result\": true,\n  \"outputTokens\": 12\n}"
	if got := String(value); got != value {
		t.Fatalf("safe JSON formatting changed: %q", got)
	}
}

func TestStringRedactsCompleteAuthorizationAndCookieHeaderValues(t *testing.T) {
	value := "Authorization: Bearer bearer-secret\n" +
		"Cookie: session=cookie-secret; theme=dark\n" +
		"Set-Cookie: refresh=set-cookie-secret; HttpOnly\n" +
		"safe: visible"
	got := String(value)
	for _, secret := range []string{"bearer-secret", "cookie-secret", "set-cookie-secret"} {
		if strings.Contains(got, secret) {
			t.Fatalf("credential header leaked %q: %q", secret, got)
		}
	}
	if strings.Count(got, "[REDACTED]") != 3 || !strings.Contains(got, "safe: visible") {
		t.Fatalf("credential header redaction damaged safe lines: %q", got)
	}
}

func TestRecordingRedactsInlineSecret(t *testing.T) {
	rec := map[string]any{
		"events": []any{
			map[string]any{
				"type":  "input",
				"value": "api-key: sk-live-12345",
			},
		},
	}
	out := Recording(rec)
	ev := out["events"].([]any)[0].(map[string]any)
	val := ev["value"].(string)
	if strings.Contains(val, "sk-live-12345") {
		t.Fatalf("inline api key should be redacted, got %q", val)
	}
}

func TestRecordingPreservesNonStringSensitiveValues(t *testing.T) {
	rec := map[string]any{
		"domSnapshots": []any{
			map[string]any{
				"text": map[string]any{
					"count":  42,
					"active": true,
					"ratio":  3.14,
					"nested": []any{1, "password: secret"},
				},
			},
		},
	}
	out := Recording(rec)
	snap := out["domSnapshots"].([]any)[0].(map[string]any)
	text := snap["text"].(map[string]any)
	if text["count"] != float64(42) {
		t.Fatalf("expected count 42, got %v", text["count"])
	}
	if text["active"] != true {
		t.Fatalf("expected active true, got %v", text["active"])
	}
	if text["ratio"] != 3.14 {
		t.Fatalf("expected ratio 3.14, got %v", text["ratio"])
	}
	nested := text["nested"].([]any)
	if strings.Contains(nested[1].(string), "secret") {
		t.Fatalf("nested secret should be redacted, got %v", nested[1])
	}
}

func TestRecordingRedactsSensitiveFieldsRecursively(t *testing.T) {
	rec := map[string]any{
		"events": []any{
			map[string]any{
				"type": "input",
				"value": map[string]any{
					"deep": map[string]any{
						"text": "password: deep-secret",
					},
				},
			},
		},
	}
	out := Recording(rec)
	ev := out["events"].([]any)[0].(map[string]any)
	val := ev["value"].(map[string]any)
	deep := val["deep"].(map[string]any)
	if !strings.Contains(deep["text"].(string), "[REDACTED]") {
		t.Fatalf("expected deeply nested secret redacted, got %q", deep["text"])
	}
}

func TestRecordingReturnsEmptyMapWhenDeepCopyUnmarshalFails(t *testing.T) {
	// json.Marshal of a map[string]any always succeeds, so exercise deepCopy
	// with a value that produces an unmarshal error by temporarily overriding.
	// Instead, we verify the fallback behavior of deepCopy when json.Unmarshal fails
	// by constructing an input that is valid for Marshal but not for Unmarshal into
	// map[string]any. The only way is an array at the top level, which this function
	// does not accept. We therefore test the shallow-copy fallback by passing a map
	// with an unmarshalable value (channel), confirming Recording does not panic.
	rec := map[string]any{
		"events": make(chan int),
	}
	out := Recording(rec)
	if out == nil {
		t.Fatal("expected non-nil output")
	}
}

func TestAnyRedactsCredentialKeysAndInlineSecretsWithoutMutation(t *testing.T) {
	input := map[string]any{
		"authorization": "Bearer private-token",
		"nested": map[string]any{
			"session_token": "private-token",
			"message":       "password=hunter2",
		},
	}
	output, ok := Any(input).(map[string]any)
	if !ok {
		t.Fatalf("unexpected generic redaction result: %#v", output)
	}
	serialized := output["authorization"].(string) + output["nested"].(map[string]any)["session_token"].(string) + output["nested"].(map[string]any)["message"].(string)
	if strings.Contains(serialized, "private-token") || strings.Contains(serialized, "hunter2") || strings.Count(serialized, "[REDACTED]") != 3 {
		t.Fatalf("generic diagnostic was not redacted: %#v", output)
	}
	if input["authorization"] != "Bearer private-token" {
		t.Fatal("generic redaction mutated its input")
	}
	if got := Any("token=inline-secret"); got != "[REDACTED]" {
		t.Fatalf("top-level string was not redacted: %q", got)
	}
}
