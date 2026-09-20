package prompt

import (
	"embed"
	"encoding/json"
	"strings"
	"text/template"

	"github.com/singhand-labs/AegisCrawler/internal/llm/redact"
)

//go:embed templates/*.txt
var templateFS embed.FS

var defaultSystemPrompt string
var userPrompt string
var userTpl *template.Template

func init() {
	sysBytes, err := templateFS.ReadFile("templates/enhance_rule_system.txt")
	if err != nil {
		panic("missing embedded system prompt template: " + err.Error())
	}
	defaultSystemPrompt = string(sysBytes)

	userBytes, err := templateFS.ReadFile("templates/enhance_rule_user.txt")
	if err != nil {
		panic("missing embedded user prompt template: " + err.Error())
	}
	userPrompt = string(userBytes)
	userTpl = template.Must(template.New("user").Parse(userPrompt))
}

type Inputs struct {
	Meta         string
	UserHint     string
	BaselineRule string
	DomTree      string
	Events       string
	Snapshots    string
}

// Build constructs the system and user prompts for rule enhancement using the
// default system prompt template.
// The recording is redacted before being serialized so that PII and secrets
// never reach the LLM prompt.
func Build(recording map[string]any, baselineRule map[string]any, userHint string) (string, string, error) {
	return BuildWithVersion(recording, baselineRule, userHint, "")
}

// BuildWithVersion constructs prompts using the system prompt template for the
// requested version. If the versioned template is missing or version is empty,
// it falls back to the default embedded system prompt.
func BuildWithVersion(recording map[string]any, baselineRule map[string]any, userHint, version string) (string, string, error) {
	sys := defaultSystemPrompt
	if version != "" {
		if b, err := templateFS.ReadFile("templates/enhance_rule_system_" + version + ".txt"); err == nil {
			sys = string(b)
		}
	}

	recording = redact.Recording(recording)

	baselineJSON, err := json.MarshalIndent(baselineRule, "", "  ")
	if err != nil {
		return "", "", err
	}
	eventsJSON, err := json.MarshalIndent(recording["events"], "", "  ")
	if err != nil {
		return "", "", err
	}
	snapshotsJSON, err := json.MarshalIndent(recording["domSnapshots"], "", "  ")
	if err != nil {
		return "", "", err
	}
	metaJSON, err := json.MarshalIndent(recording["meta"], "", "  ")
	if err != nil {
		return "", "", err
	}
	domTreeJSON, err := json.MarshalIndent(extractDomTree(recording), "", "  ")
	if err != nil {
		return "", "", err
	}

	var sb strings.Builder
	if err := userTpl.Execute(&sb, Inputs{
		Meta:         string(metaJSON),
		UserHint:     userHint,
		BaselineRule: string(baselineJSON),
		DomTree:      string(domTreeJSON),
		Events:       string(eventsJSON),
		Snapshots:    string(snapshotsJSON),
	}); err != nil {
		return "", "", err
	}
	return sys, sb.String(), nil
}

func extractDomTree(recording map[string]any) any {
	snapshots, ok := recording["domSnapshots"].([]any)
	if !ok || len(snapshots) == 0 {
		return nil
	}
	last, ok := snapshots[len(snapshots)-1].(map[string]any)
	if !ok {
		return nil
	}
	return last["domTree"]
}
