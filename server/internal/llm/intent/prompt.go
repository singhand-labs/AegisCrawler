package intent

import (
	"bytes"
	"embed"
	"encoding/json"
	"text/template"

	"github.com/singhand-labs/AegisCrawler/internal/llm/redact"
)

//go:embed templates/*.txt
var templateFS embed.FS

var predictSystemPrompt string
var predictUserTpl *template.Template

func init() {
	sysBytes, err := templateFS.ReadFile("templates/predict_intent_system.txt")
	if err != nil {
		panic("missing embedded system prompt template: " + err.Error())
	}
	predictSystemPrompt = string(sysBytes)

	userBytes, err := templateFS.ReadFile("templates/predict_intent_user.txt")
	if err != nil {
		panic("missing embedded user prompt template: " + err.Error())
	}
	predictUserTpl = template.Must(template.New("user").Parse(string(userBytes)))
}

type promptInputs struct {
	Meta      string
	DomTree   string
	Events    string
	Snapshots string
}

const (
	maxPromptSnapshots = 2
	maxPromptEvents    = 50
	maxPromptDomNodes  = 500
	maxPromptTextLen   = 100
)

// BuildPredictPrompt builds the system and user prompts for intent prediction.
// Large recordings are condensed before being sent to the LLM so the request
// stays within model context limits while keeping the most relevant context.
func BuildPredictPrompt(recording map[string]any) (string, string, error) {
	condensed := condenseRecordingForPrompt(recording)
	condensed = redact.Recording(condensed)

	metaJSON, err := json.MarshalIndent(condensed["meta"], "", "  ")
	if err != nil {
		return "", "", err
	}
	domTreeJSON, err := json.MarshalIndent(extractDomTree(condensed), "", "  ")
	if err != nil {
		return "", "", err
	}
	eventsJSON, err := json.MarshalIndent(condensed["events"], "", "  ")
	if err != nil {
		return "", "", err
	}
	snapshotsJSON, err := json.MarshalIndent(getSnapshots(condensed), "", "  ")
	if err != nil {
		return "", "", err
	}

	var sb bytes.Buffer
	if err := predictUserTpl.Execute(&sb, promptInputs{
		Meta:      string(metaJSON),
		DomTree:   string(domTreeJSON),
		Events:    string(eventsJSON),
		Snapshots: string(snapshotsJSON),
	}); err != nil {
		return "", "", err
	}
	return predictSystemPrompt, sb.String(), nil
}

// condenseRecordingForPrompt returns a smaller copy of the recording suitable
// for the LLM prompt. It keeps the last snapshots and recent events, drops
// heavy fields like selectorMap, and truncates DOM trees.
func condenseRecordingForPrompt(recording map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range recording {
		out[k] = v
	}

	if meta, ok := recording["meta"].(map[string]any); ok {
		out["meta"] = meta
	}

	if events, ok := recording["events"].([]any); ok {
		out["events"] = truncateEvents(events, maxPromptEvents)
	}

	snapshots := getSnapshots(recording)
	if len(snapshots) > 0 {
		out["snapshots"] = condenseSnapshots(snapshots, maxPromptSnapshots)
		out["domSnapshots"] = nil
	}

	return out
}

func truncateEvents(events []any, maxCount int) []any {
	if len(events) <= maxCount {
		return events
	}
	return events[len(events)-maxCount:]
}

func condenseSnapshots(snapshots []any, maxCount int) []any {
	start := 0
	if len(snapshots) > maxCount {
		start = len(snapshots) - maxCount
	}
	out := make([]any, 0, len(snapshots)-start)
	for i := start; i < len(snapshots); i++ {
		snap, ok := snapshots[i].(map[string]any)
		if !ok {
			continue
		}
		condensed := map[string]any{}
		if v, ok := snap["timestamp"].(float64); ok {
			condensed["timestamp"] = v
		}
		if v, ok := snap["url"].(string); ok {
			condensed["url"] = v
		}
		if tree, ok := snap["domTree"]; ok && tree != nil {
			condensed["domTree"] = truncateDomTree(tree, maxPromptDomNodes, maxPromptTextLen)
		}
		out = append(out, condensed)
	}
	return out
}

func truncateDomTree(tree any, maxNodes int, maxTextLen int) any {
	remaining := maxNodes
	return truncateNode(tree, &remaining, maxTextLen)
}

func truncateNode(node any, remaining *int, maxTextLen int) any {
	if *remaining <= 0 {
		return nil
	}
	*remaining--

	switch n := node.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, v := range n {
			switch k {
			case "text":
				if s, ok := v.(string); ok {
					out[k] = truncateString(s, maxTextLen)
				}
			case "children":
				if children, ok := v.([]any); ok {
					truncated := make([]any, 0, len(children))
					for _, child := range children {
						if t := truncateNode(child, remaining, maxTextLen); t != nil {
							truncated = append(truncated, t)
						}
					}
					if len(truncated) > 0 {
						out[k] = truncated
					}
				}
			default:
				out[k] = v
			}
		}
		return out
	case string:
		return truncateString(n, maxTextLen)
	default:
		return n
	}
}

func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "…"
}

func getSnapshots(recording map[string]any) []any {
	if v, ok := recording["domSnapshots"].([]any); ok {
		return v
	}
	if v, ok := recording["snapshots"].([]any); ok {
		return v
	}
	return nil
}

func extractDomTree(recording map[string]any) any {
	snapshots := getSnapshots(recording)
	if len(snapshots) == 0 {
		return nil
	}
	last, ok := snapshots[len(snapshots)-1].(map[string]any)
	if !ok {
		return nil
	}
	return last["domTree"]
}
