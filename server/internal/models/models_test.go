package models

import (
	"database/sql/driver"
	"encoding/json"
	"testing"
)

func TestJSONScan(t *testing.T) {
	tests := []struct {
		name    string
		value   any
		want    JSON
		wantErr bool
	}{
		{"nil", nil, JSON("{}"), false},
		{"bytes", []byte(`{"a":1}`), JSON(`{"a":1}`), false},
		{"string", `{"b":2}`, JSON(`{"b":2}`), false},
		{"unsupported", 123, nil, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var j JSON
			err := j.Scan(tt.value)
			if (err != nil) != tt.wantErr {
				t.Fatalf("Scan() error = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr && string(j) != string(tt.want) {
				t.Fatalf("Scan() got %q, want %q", j, tt.want)
			}
		})
	}
}

func TestJSONValue(t *testing.T) {
	j := JSON(`{"x":1}`)
	v, err := j.Value()
	if err != nil {
		t.Fatal(err)
	}
	if v != `{"x":1}` {
		t.Fatalf("Value() got %q, want %q", v, `{"x":1}`)
	}

	empty := JSON("")
	v, err = empty.Value()
	if err != nil {
		t.Fatal(err)
	}
	if v != "{}" {
		t.Fatalf("Value() empty got %q, want %q", v, "{}")
	}
}

func TestJSONValueImplementsDriverValuer(t *testing.T) {
	var _ driver.Valuer = JSON("")
}

func TestJSONMarshalJSON(t *testing.T) {
	j := JSON(`{"y":2}`)
	b, err := j.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `{"y":2}` {
		t.Fatalf("MarshalJSON() got %q", b)
	}

	empty := JSON("")
	b, err = empty.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "{}" {
		t.Fatalf("MarshalJSON() empty got %q", b)
	}
}

func TestJSONUnmarshalJSON(t *testing.T) {
	var j JSON
	if err := j.UnmarshalJSON([]byte(`{"z":3}`)); err != nil {
		t.Fatal(err)
	}
	if string(j) != `{"z":3}` {
		t.Fatalf("UnmarshalJSON() got %q", j)
	}
}

func TestJSONRoundTrip(t *testing.T) {
	var j JSON
	if err := j.UnmarshalJSON([]byte(`{"a":1}`)); err != nil {
		t.Fatal(err)
	}

	b, err := j.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}

	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if out["a"] != float64(1) {
		t.Fatalf("round trip got %v", out)
	}
}

func TestTaskStatusConstants(t *testing.T) {
	// Constants exist and have expected values.
	if TaskStatusPending != "pending" {
		t.Fatalf("unexpected pending status: %s", TaskStatusPending)
	}
	if TaskStatusDone != "done" {
		t.Fatalf("unexpected done status: %s", TaskStatusDone)
	}
}

func TestPriorityConstants(t *testing.T) {
	if PriorityHigh != "high" {
		t.Fatalf("unexpected high priority: %s", PriorityHigh)
	}
}

func TestRuleApprovalStatusConstants(t *testing.T) {
	if RuleApprovalApproved != "approved" {
		t.Fatalf("unexpected approved status: %s", RuleApprovalApproved)
	}
}

func TestEnhancementStatusConstants(t *testing.T) {
	if EnhancementStatusRejected != "rejected" {
		t.Fatalf("unexpected rejected status: %s", EnhancementStatusRejected)
	}
}

func TestScheduleTypeConstants(t *testing.T) {
	if ScheduleTypeCron != "cron" {
		t.Fatalf("unexpected cron schedule type: %s", ScheduleTypeCron)
	}
}

func TestCatchupModeConstants(t *testing.T) {
	if CatchupSkip != "skip" {
		t.Fatalf("unexpected catchup mode: %s", CatchupSkip)
	}
}
