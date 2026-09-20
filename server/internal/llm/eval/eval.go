//go:build eval

package eval

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/singhand-labs/AegisCrawler/internal/llm"
)

// Case is a single offline evaluation example.
type Case struct {
	Recording              map[string]any `json:"recording"`
	Baseline               map[string]any `json:"baseline"`
	ExpectedFields         []string       `json:"expectedFields"`
	ExpectedStableSelector bool           `json:"expectedStableSelector"`
}

// CaseResult holds the outcome for a single case.
type CaseResult struct {
	Index   int
	Passed  bool
	Missing []string
	Error   string
	Stable  bool
	TP      int
	FP      int
	FN      int
}

// Result aggregates outcomes over the whole dataset.
type Result struct {
	Total     int
	Passed    int
	Failures  []string
	Accuracy  float64
	Precision float64
	Recall    float64
	PerCase   []CaseResult
}

// LoadCases reads evaluation cases from a JSONL stream.
func LoadCases(r io.Reader) ([]Case, error) {
	var cases []Case
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 1024), 1024*1024)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var c Case
		if err := json.Unmarshal([]byte(line), &c); err != nil {
			return nil, fmt.Errorf("line %d: %w", lineNo, err)
		}
		cases = append(cases, c)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return cases, nil
}

// LoadCasesFromFile reads evaluation cases from a JSONL file.
func LoadCasesFromFile(path string) ([]Case, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return LoadCases(f)
}

// Run evaluates the enhancer against the provided cases and returns accuracy,
// precision, and recall metrics.
func Run(ctx context.Context, enhancer *llm.Enhancer, cases []Case) (*Result, error) {
	result := &Result{
		Total:   len(cases),
		PerCase: make([]CaseResult, 0, len(cases)),
	}

	tpTotal, fpTotal, fnTotal := 0, 0, 0

	for i, c := range cases {
		cr := CaseResult{Index: i}

		res, err := enhancer.Enhance(ctx, llm.EnhanceRequest{
			Recording:    c.Recording,
			BaselineRule: c.Baseline,
			UserHint:     "", // hints can be added to the fixture later if needed
		})
		if err != nil {
			cr.Error = err.Error()
			cr.FN = len(c.ExpectedFields)
			fnTotal += cr.FN
			result.PerCase = append(result.PerCase, cr)
			result.Failures = append(result.Failures, fmt.Sprintf("case %d: enhance error: %v", i, err))
			continue
		}

		rule := res.Rule
		predicted := collectPredictedFields(rule)
		missing := checkExpectedFields(predicted, c.ExpectedFields)
		cr.Missing = missing
		cr.Stable = hasStableSelector(rule)
		cr.TP = len(c.ExpectedFields) - len(missing)
		cr.FN = len(missing)
		cr.FP = countFalsePositives(predicted, c.ExpectedFields)

		cr.Passed = len(missing) == 0 && (!c.ExpectedStableSelector || cr.Stable)
		if cr.Passed {
			result.Passed++
		} else {
			var parts []string
			if len(missing) > 0 {
				parts = append(parts, fmt.Sprintf("missing=%v", missing))
			}
			if c.ExpectedStableSelector && !cr.Stable {
				parts = append(parts, "stableSelector=false")
			}
			if cr.Error != "" {
				parts = append(parts, fmt.Sprintf("error=%s", cr.Error))
			}
			result.Failures = append(result.Failures, fmt.Sprintf("case %d: %s", i, strings.Join(parts, " ")))
		}

		tpTotal += cr.TP
		fpTotal += cr.FP
		fnTotal += cr.FN
		result.PerCase = append(result.PerCase, cr)
	}

	if result.Total > 0 {
		result.Accuracy = float64(result.Passed) / float64(result.Total)
	}
	if tpTotal+fpTotal > 0 {
		result.Precision = float64(tpTotal) / float64(tpTotal+fpTotal)
	}
	if tpTotal+fnTotal > 0 {
		result.Recall = float64(tpTotal) / float64(tpTotal+fnTotal)
	}

	return result, nil
}

func collectPredictedFields(rule map[string]any) map[string]bool {
	predicted := make(map[string]bool)
	if selectors, ok := rule["selectors"].(map[string]any); ok {
		for k := range selectors {
			predicted[k] = true
		}
	}
	if variables, ok := rule["variables"].(map[string]any); ok {
		for k := range variables {
			predicted[k] = true
		}
	}
	return predicted
}

func checkExpectedFields(predicted map[string]bool, expected []string) []string {
	var missing []string
	for _, f := range expected {
		if !predicted[f] {
			missing = append(missing, f)
		}
	}
	return missing
}

func countFalsePositives(predicted map[string]bool, expected []string) int {
	expectedSet := make(map[string]bool, len(expected))
	for _, f := range expected {
		expectedSet[f] = true
	}
	fp := 0
	for f := range predicted {
		if !expectedSet[f] {
			fp++
		}
	}
	return fp
}

// hasStableSelector reports whether the rule contains at least one stable selector.
func hasStableSelector(rule map[string]any) bool {
	selectors, ok := rule["selectors"].(map[string]any)
	if !ok {
		return false
	}
	for _, v := range selectors {
		selMap, ok := v.(map[string]any)
		if !ok {
			continue
		}
		sel, _ := selMap["selector"].(string)
		if sel == "" {
			continue
		}
		if isStableSelector(sel) {
			return true
		}
	}
	return false
}

func isStableSelector(sel string) bool {
	s := strings.ToLower(sel)
	if strings.Contains(s, "data-testid") {
		return true
	}
	if strings.Contains(s, "id=") || strings.Contains(s, "aria-") || strings.Contains(s, "role=") {
		return true
	}
	// ID selector at the start of the selector or after a combinator.
	if strings.HasPrefix(s, "#") || strings.Contains(s, " #") {
		return true
	}
	return false
}
