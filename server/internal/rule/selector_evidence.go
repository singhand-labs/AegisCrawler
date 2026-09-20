package rule

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"

	"github.com/andybalholm/cascadia"
	"github.com/singhand-labs/AegisCrawler/internal/models"
	"golang.org/x/net/html"
)

const (
	maxSelectorCatalogCandidates    = 64
	maxSelectorValidationCandidates = 256
	maxSelectorDiscoveryCandidates  = 4 * maxSelectorValidationCandidates
	maxSelectorEvidencePromptBytes  = 32 * 1024
	maxRelativeSelectors            = 12
	maxSelectorStructuralDepthV3    = 4
	maxSelectorStructuralDepth      = 8
)

var (
	ErrSelectorEvidenceUnavailable = errors.New("selector evidence is unavailable")
	ErrSelectorCatalogUnavailable  = errors.New("selector candidate catalog is unavailable")
	ErrSelectorSourceUnavailable   = errors.New("selector source contract is unavailable")

	simpleCSSIdentifier = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*$`)
	classSelectorToken  = regexp.MustCompile(`\.([A-Za-z0-9_-]+)`)
	idSelectorToken     = regexp.MustCompile(`#([A-Za-z0-9_-]+)`)
	attributeToken      = regexp.MustCompile(`(?i)\[\s*(id|class)\s*([~|^$*]?=)\s*(?:"([^"]*)"|'([^']*)'|([^\]\s]+))`)
	unsupportedPseudo   = regexp.MustCompile(`(?i):(containsown|contains|matchesown|matches|haschild|input)(?:\s*\(|\b)`)
	uuidLikeToken       = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	digitsOnlyToken     = regexp.MustCompile(`^[0-9]+$`)
	hexLikeToken        = regexp.MustCompile(`(?i)^[0-9a-f]+$`)
	camelTokenBoundary  = regexp.MustCompile(`([a-z0-9])([A-Z])`)

	implicitRoleByTag = map[string]string{
		"a": "link", "aside": "complementary", "button": "button",
		"footer": "contentinfo", "form": "form", "h1": "heading",
		"h2": "heading", "h3": "heading", "h4": "heading",
		"h5": "heading", "h6": "heading", "header": "banner",
		"img": "image", "li": "listitem", "main": "main",
		"nav": "navigation", "ol": "list", "select": "combobox",
		"table": "table", "textarea": "textbox", "ul": "list",
	}
	implicitRoleByInputType = map[string]string{
		"button": "button", "checkbox": "checkbox", "email": "textbox",
		"image": "button", "number": "textbox", "password": "textbox",
		"radio": "radio", "range": "slider", "reset": "button",
		"search": "searchbox", "submit": "button", "tel": "textbox",
		"text": "textbox", "url": "textbox",
	}
)

// SelectorEvidenceCatalog is a bounded, text-free summary of selectors that
// are provably present in the sanitized recording. snapshots is deliberately
// excluded from JSON so DOM content is never copied into job audit artifacts.
type SelectorEvidenceCatalog struct {
	Candidates           []SelectorEvidenceCandidate `json:"candidates"`
	validationCandidates []SelectorEvidenceCandidate
	legacyCandidates     []SelectorEvidenceCandidate
	legacyValidation     []SelectorEvidenceCandidate
	v3Candidates         []SelectorEvidenceCandidate
	v3Validation         []SelectorEvidenceCandidate
	snapshots            []selectorEvidenceSnapshot
	promptCatalogHash    string
	promptTargets        map[string]*providerTargetEvidence
	promptFields         map[string]*providerFieldEvidence
}

type SelectorEvidenceCandidate struct {
	Selector          string   `json:"selector"`
	State             string   `json:"state"`
	SnapshotSequences []int    `json:"snapshotSequences"`
	Cardinalities     []int    `json:"cardinalities"`
	RelativeSelectors []string `json:"relativeSelectors,omitempty"`
}

type SelectorEvidenceReport struct {
	Checked       int      `json:"checked"`
	Canonicalized int      `json:"canonicalized"`
	ActionPaths   []string `json:"actionPaths,omitempty"`
}

type selectorEvidenceSnapshot struct {
	sequence int
	state    string
	root     *html.Node
}

// BuildSelectorEvidenceCatalog reconstructs complete semantic DOM snapshots
// and derives stable repeated sibling cohorts. It never includes page text.
func BuildSelectorEvidenceCatalog(recording map[string]any) (*SelectorEvidenceCatalog, error) {
	rawSnapshots, _ := recording["snapshots"].([]any)
	if len(rawSnapshots) == 0 {
		rawSnapshots, _ = recording["domSnapshots"].([]any)
	}
	catalog := &SelectorEvidenceCatalog{}
	hasSanitizationProvenance := false
	if meta, ok := recording["meta"].(map[string]any); ok {
		hasSanitizationProvenance =
			strings.TrimSpace(stringValue(meta["sanitizationVersion"])) == "extension-v2"
	}
	for index, raw := range rawSnapshots {
		snapshot, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if capture, ok := snapshot["capture"].(map[string]any); ok && capture["status"] == "failed" {
			continue
		}
		domTree, ok := snapshot["domTree"].(map[string]any)
		if !ok {
			continue
		}
		root := &html.Node{Type: html.DocumentNode}
		if node := semanticNodeToHTML(domTree); node != nil {
			if !hasSanitizationProvenance && node.Type == html.ElementNode {
				node.Attr = append(node.Attr, html.Attribute{
					Key: recordedSanitizationUnknownAttribute, Val: "true",
				})
			}
			root.AppendChild(node)
		} else {
			continue
		}
		sequence := index
		switch value := snapshot["sequence"].(type) {
		case float64:
			sequence = int(value)
		case int:
			sequence = value
		}
		catalog.snapshots = append(catalog.snapshots, selectorEvidenceSnapshot{
			sequence: sequence,
			state:    selectorStateKey(stringValue(snapshot["url"])),
			root:     root,
		})
	}
	if len(catalog.snapshots) == 0 {
		return nil, fmt.Errorf("%w: recording has no usable semantic DOM snapshot", ErrSelectorEvidenceUnavailable)
	}
	legacyDerived := buildSelectorEvidenceCandidates(
		catalog.snapshots,
		false,
		maxSelectorStructuralDepthV3,
	)
	legacyDerived = deduplicateSelectorEvidenceCandidates(legacyDerived, catalog.snapshots)
	v3Derived := buildSelectorEvidenceCandidates(
		catalog.snapshots,
		true,
		maxSelectorStructuralDepthV3,
	)
	v3Derived = deduplicateSelectorEvidenceCandidates(v3Derived, catalog.snapshots)
	derived := buildSelectorEvidenceCandidates(
		catalog.snapshots,
		true,
		maxSelectorStructuralDepth,
	)
	derived = deduplicateSelectorEvidenceCandidates(derived, catalog.snapshots)
	catalog.legacyValidation = boundSelectorEvidenceCandidates(
		legacyDerived, catalog.snapshots, maxSelectorValidationCandidates,
	)
	catalog.legacyCandidates = boundSelectorEvidenceCandidates(
		legacyDerived, catalog.snapshots, maxSelectorCatalogCandidates,
	)
	catalog.v3Validation = boundSelectorEvidenceCandidates(
		v3Derived, catalog.snapshots, maxSelectorValidationCandidates,
	)
	catalog.v3Candidates = boundSelectorEvidenceCandidates(
		v3Derived, catalog.snapshots, maxSelectorCatalogCandidates,
	)
	catalog.validationCandidates = boundSelectorEvidenceCandidates(
		derived, catalog.snapshots, maxSelectorValidationCandidates,
	)
	catalog.Candidates = boundSelectorEvidenceCandidates(
		derived, catalog.snapshots, maxSelectorCatalogCandidates,
	)
	return catalog, nil
}

// ValidateAndStabilizeGeneratedExtractionSelectors validates generated
// extraction selectors against their source recording. It rewrites an exact
// equivalent to the best stable candidate and never broadens or narrows the
// recorded node set.
func ValidateAndStabilizeGeneratedExtractionSelectors(rule *models.Rule, catalog *SelectorEvidenceCatalog) (SelectorEvidenceReport, error) {
	report := SelectorEvidenceReport{}
	if rule == nil || catalog == nil || len(catalog.snapshots) == 0 {
		return report, fmt.Errorf("%w: selector evidence requires complete semantic snapshots", ErrInvalidProvisionalRule)
	}
	selectors := map[string]any{}
	if len(rule.Selectors) > 0 {
		if err := json.Unmarshal(rule.Selectors, &selectors); err != nil {
			return report, fmt.Errorf("%w: selectors must be an object", ErrInvalidProvisionalRule)
		}
	}
	var steps []any
	if err := json.Unmarshal(rule.Steps, &steps); err != nil {
		return report, fmt.Errorf("%w: steps must be an array", ErrInvalidProvisionalRule)
	}
	if err := validateSelectorEvidenceActions(steps, "steps", selectors, catalog, &report); err != nil {
		return report, err
	}
	if len(rule.Hooks) > 0 {
		var hooks map[string]any
		if err := json.Unmarshal(rule.Hooks, &hooks); err != nil {
			return report, fmt.Errorf("%w: hooks must be an object", ErrInvalidProvisionalRule)
		}
		for _, name := range []string{"beforeAll", "afterAll", "onError", "cleanup"} {
			if values, ok := hooks[name].([]any); ok {
				if err := validateSelectorEvidenceActions(values, "hooks."+name, selectors, catalog, &report); err != nil {
					return report, err
				}
			}
		}
		encoded, err := json.Marshal(hooks)
		if err != nil {
			return report, err
		}
		rule.Hooks = encoded
	}
	encoded, err := json.Marshal(steps)
	if err != nil {
		return report, err
	}
	rule.Steps = encoded
	return report, nil
}

func validateSelectorEvidenceActions(values []any, path string, aliases map[string]any, catalog *SelectorEvidenceCatalog, report *SelectorEvidenceReport) error {
	for index, raw := range values {
		step, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		stepPath := fmt.Sprintf("%s[%d]", path, index)
		action, _ := step["action"].(string)
		if extractionWorkflowActions[action] {
			if target, ok := step["target"].(map[string]any); ok {
				originalTarget := cloneWorkflowTarget(target)
				if err := validateOneExtractionSelector(step, target, stepPath, aliases, catalog, report); err != nil {
					return err
				}
				if action == "extract" && step["multiple"] == true {
					synchronizeRepeatedExtractionReadiness(values, index, originalTarget, target)
				}
			}
		}
		for _, branch := range nestedActionListKeys {
			if children, ok := step[branch].([]any); ok {
				if err := validateSelectorEvidenceActions(children, stepPath+"."+branch, aliases, catalog, report); err != nil {
					return err
				}
			}
		}
		if trigger, ok := step["trigger"].(map[string]any); ok {
			if err := validateSelectorEvidenceActions([]any{trigger}, stepPath+".trigger", aliases, catalog, report); err != nil {
				return err
			}
		}
		if cases, ok := step["cases"].([]any); ok {
			for caseIndex, rawCase := range cases {
				caseValue, _ := rawCase.(map[string]any)
				caseSteps, _ := caseValue["steps"].([]any)
				if err := validateSelectorEvidenceActions(caseSteps, fmt.Sprintf("%s.cases[%d].steps", stepPath, caseIndex), aliases, catalog, report); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// synchronizeRepeatedExtractionReadiness keeps the server-derived readiness
// checkpoint bound to the same exact target when selector evidence rewrites an
// equivalent extraction selector to its stable canonical form. Only a visible,
// fail-closed checkpoint matching the pre-stabilized target is eligible.
func synchronizeRepeatedExtractionReadiness(
	values []any,
	extractionIndex int,
	originalTarget map[string]any,
	stabilizedTarget map[string]any,
) {
	readinessIndex := extractionIndex - 1
	for readinessIndex >= 0 && unconditionalFixedWait(values[readinessIndex]) {
		readinessIndex--
	}
	if readinessIndex < 0 {
		return
	}
	wait, _ := values[readinessIndex].(map[string]any)
	if !equivalentVisibleReadiness(wait, originalTarget) {
		return
	}
	wait["target"] = cloneWorkflowTarget(stabilizedTarget)
}

func validateOneExtractionSelector(step, inline map[string]any, path string, aliases map[string]any, catalog *SelectorEvidenceCatalog, report *SelectorEvidenceReport) error {
	resolved := map[string]any{}
	if ref := strings.TrimSpace(stringValue(inline["$ref"])); ref != "" {
		alias, ok := aliases[ref].(map[string]any)
		if !ok {
			return fmt.Errorf("%w: %s.target references unknown selector %q", ErrInvalidProvisionalRule, path, ref)
		}
		for key, value := range alias {
			resolved[key] = value
		}
	}
	for key, value := range inline {
		resolved[key] = value
	}
	if _, ok := resolved["frame"]; ok {
		return fmt.Errorf("%w: %s.target frame selector lacks isolated recording evidence", ErrInvalidProvisionalRule, path)
	}
	if _, ok := resolved["shadowPath"]; ok {
		return fmt.Errorf("%w: %s.target shadow path lacks recorded selector evidence", ErrInvalidProvisionalRule, path)
	}
	for _, unsupported := range []string{"xpath", "text", "position"} {
		if value, exists := resolved[unsupported]; exists && value != nil && strings.TrimSpace(fmt.Sprint(value)) != "" {
			return fmt.Errorf("%w: %s.target.%s is not recording-grounded for generated extraction", ErrInvalidProvisionalRule, path, unsupported)
		}
	}
	selector := strings.TrimSpace(stringValue(resolved["selector"]))
	if selector == "" {
		return fmt.Errorf("%w: %s.target requires a recording-grounded CSS selector", ErrInvalidProvisionalRule, path)
	}
	matcher, err := compileBrowserSelector(selector)
	if err != nil {
		return fmt.Errorf("%w: %s.target selector is not valid CSS: %v", ErrInvalidProvisionalRule, path, err)
	}
	if isPositionalSelector(selector) {
		return fmt.Errorf("%w: %s.target selector %q is positional and not recording-grounded", ErrInvalidProvisionalRule, path, selector)
	}
	multiple := step["multiple"] == true
	candidates := catalog.validationCandidates
	if len(candidates) == 0 {
		candidates = catalog.Candidates
	}
	var stateSnapshots []selectorEvidenceSnapshot
	var nodeSets [][]*html.Node
	if multiple {
		replacement, candidateSnapshots, candidateNodes := exactStableTargetEvidence(candidates, catalog.snapshots, matcher, 2)
		if replacement == "" {
			observedSnapshots, observedNodes, evidenceErr := strongestSelectorEvidence(catalog.snapshots, matcher)
			if evidenceErr != nil {
				return fmt.Errorf("%w: %s.target selector %q has %v", ErrInvalidProvisionalRule, path, selector, evidenceErr)
			}
			for index, nodes := range observedNodes {
				if len(nodes) < 2 {
					return fmt.Errorf("%w: %s.target selector %q recorded cardinality is %d in snapshot %d; repeated extraction requires at least 2", ErrInvalidProvisionalRule, path, selector, len(nodes), observedSnapshots[index].sequence)
				}
			}
			return fmt.Errorf("%w: %s.target selector %q does not match an exact stable recorded repeated cohort", ErrInvalidProvisionalRule, path, selector)
		}
		stateSnapshots, nodeSets = candidateSnapshots, candidateNodes
		if selector != replacement {
			inline["selector"] = replacement
			selector = replacement
			report.Canonicalized++
		}
	} else {
		stateSnapshots, nodeSets, err = strongestSelectorEvidence(catalog.snapshots, matcher)
		if err != nil {
			return fmt.Errorf("%w: %s.target selector %q has %v", ErrInvalidProvisionalRule, path, selector, err)
		}
		for index, nodes := range nodeSets {
			if len(nodes) != 1 {
				return fmt.Errorf("%w: %s.target selector %q recorded cardinality is %d in snapshot %d; single extraction requires exactly 1", ErrInvalidProvisionalRule, path, selector, len(nodes), stateSnapshots[index].sequence)
			}
		}
	}
	if err := validateTargetDisambiguators(resolved, nodeSets, path); err != nil {
		return err
	}
	if !multiple {
		replacement, replacementSnapshots, replacementNodes := exactStableTargetEvidence(candidates, catalog.snapshots, matcher, 1)
		if replacement != "" && replacement != selector && selector != "main" {
			inline["selector"] = replacement
			selector = replacement
			stateSnapshots, nodeSets = replacementSnapshots, replacementNodes
			report.Canonicalized++
		} else if replacement == "" && (isVolatileSelector(selector) || selectorUsesNonUniqueID(selector, stateSnapshots)) {
			return fmt.Errorf("%w: %s.target selector %q contains volatile or positional tokens and has no exact stable equivalent", ErrInvalidProvisionalRule, path, selector)
		}
	}
	if err := validateFreeFormExtractionCapability(step, stringValue(step["action"]), nodeSets, path); err != nil {
		return err
	}
	if fields, ok := step["fields"].(map[string]any); ok {
		if err := validateExtractionFieldSelectors(fields, nodeSets, path+".fields", report); err != nil {
			return err
		}
	}
	report.Checked++
	report.ActionPaths = append(report.ActionPaths, path)
	return nil
}

func validateExtractionFieldSelectors(fields map[string]any, nodeSets [][]*html.Node, path string, report *SelectorEvidenceReport) error {
	names := make([]string, 0, len(fields))
	for name := range fields {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		field, ok := fields[name].(map[string]any)
		if !ok {
			return fmt.Errorf("%w: %s.%s must be an extraction field object", ErrInvalidProvisionalRule, path, name)
		}
		fieldPath := path + "." + name
		fieldNodeSets := nodeSets
		fieldSelector := strings.TrimSpace(stringValue(field["selector"]))
		if fieldSelector != "" {
			fieldMatcher, compileErr := compileBrowserSelector(fieldSelector)
			if compileErr != nil {
				return fmt.Errorf("%w: %s selector is not valid CSS: %v", ErrInvalidProvisionalRule, fieldPath, compileErr)
			}
			if isPositionalSelector(fieldSelector) {
				return fmt.Errorf("%w: %s selector %q is positional and not recording-grounded", ErrInvalidProvisionalRule, fieldPath, fieldSelector)
			}
			var covered bool
			fieldNodeSets, covered = relativeSelectorNodeSets(fieldMatcher, nodeSets)
			if !covered {
				_, hasNestedFields := field["fields"]
				if hasNestedFields || !extractFieldRequiresRecordedText(field["type"]) {
					return fmt.Errorf("%w: %s selector %q must match exactly one descendant in every recorded row", ErrInvalidProvisionalRule, fieldPath, fieldSelector)
				}
				replacement, replacementNodes := unambiguousSemanticFieldReplacement(name, nodeSets)
				if replacement == "" {
					return fmt.Errorf("%w: %s selector %q must match exactly one descendant in every recorded row", ErrInvalidProvisionalRule, fieldPath, fieldSelector)
				}
				field["selector"] = replacement
				fieldSelector = replacement
				fieldNodeSets = replacementNodes
				report.Canonicalized++
			}
			if covered {
				replacement := exactStableRelativeReplacement(nodeSets, fieldMatcher)
				if replacement != "" && replacement != fieldSelector {
					field["selector"] = replacement
					fieldSelector = replacement
					fieldMatcher, _ = compileBrowserSelector(replacement)
					fieldNodeSets, _ = relativeSelectorNodeSets(fieldMatcher, nodeSets)
					report.Canonicalized++
				} else if replacement == "" && isVolatileSelector(fieldSelector) {
					return fmt.Errorf("%w: %s selector %q is volatile and has no exact stable equivalent", ErrInvalidProvisionalRule, fieldPath, fieldSelector)
				}
			}
		}
		nested, err := validateTypedFieldCapability(field, fieldNodeSets, fieldPath, false)
		if err != nil {
			return err
		}
		if nested != nil {
			if err := validateExtractionFieldSelectors(nested, fieldNodeSets, fieldPath+".fields", report); err != nil {
				return err
			}
		}
	}
	return nil
}

func unambiguousSemanticFieldReplacement(fieldName string, nodeSets [][]*html.Node) (string, [][]*html.Node) {
	fieldTokens := semanticSelectorTokens(fieldName)
	if len(fieldTokens) == 0 || selectorEvidenceNodeCount(nodeSets) < 2 {
		return "", nil
	}
	type candidate struct {
		selector string
		nodeSets [][]*html.Node
	}
	var candidates []candidate
	textShapes := map[string]bool{}
	for _, selector := range stableRelativeCandidates(nodeSets) {
		matcher, err := compileBrowserSelector(selector)
		if err != nil || isVolatileSelector(selector) {
			continue
		}
		matched, covered := relativeSelectorNodeSets(matcher, nodeSets)
		if !covered || !nodeSetsHaveNonEmptyText(matched) ||
			!nodeSetsCarrySemanticFieldToken(matched, fieldTokens) {
			continue
		}
		shape := selectorEvidenceTextShape(matched)
		textShapes[shape] = true
		candidates = append(candidates, candidate{selector: selector, nodeSets: matched})
	}
	if len(candidates) == 0 || len(textShapes) != 1 {
		return "", nil
	}
	best := candidates[0]
	for _, current := range candidates[1:] {
		if stableSelectorLess(current.selector, best.selector) {
			best = current
		}
	}
	return best.selector, best.nodeSets
}

func semanticSelectorTokens(value string) map[string]bool {
	value = camelTokenBoundary.ReplaceAllString(value, "$1 $2")
	tokens := map[string]bool{}
	for _, token := range strings.FieldsFunc(strings.ToLower(value), func(r rune) bool {
		return !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9')
	}) {
		if len(token) < 3 {
			continue
		}
		switch token {
		case "content", "data", "field", "item", "result", "row", "text", "value":
			continue
		}
		tokens[token] = true
	}
	return tokens
}

func nodeSetsCarrySemanticFieldToken(nodeSets [][]*html.Node, fieldTokens map[string]bool) bool {
	for _, nodes := range nodeSets {
		for _, node := range nodes {
			matched := false
			for _, attribute := range []string{
				"class", "id", "data-testid", "data-qa", "data-automation-id", "itemprop", "name", "role",
			} {
				for token := range semanticSelectorTokens(attrValue(node, attribute)) {
					if fieldTokens[token] {
						matched = true
						break
					}
				}
				if matched {
					break
				}
			}
			if !matched {
				return false
			}
		}
	}
	return true
}

func selectorEvidenceNodeCount(nodeSets [][]*html.Node) int {
	total := 0
	for _, nodes := range nodeSets {
		total += len(nodes)
	}
	return total
}

func selectorEvidenceTextShape(nodeSets [][]*html.Node) string {
	var result strings.Builder
	for _, nodes := range nodeSets {
		for _, node := range nodes {
			text := normalizedSemanticText(node)
			result.WriteString(fmt.Sprintf("%d:%s;", len(text), text))
		}
		result.WriteByte('|')
	}
	return result.String()
}

func validateTargetDisambiguators(target map[string]any, nodeSets [][]*html.Node, path string) error {
	ariaLabel := strings.TrimSpace(stringValue(target["ariaLabel"]))
	role := strings.TrimSpace(stringValue(target["role"]))
	roleName := strings.TrimSpace(stringValue(target["roleName"]))
	for _, nodes := range nodeSets {
		for _, node := range nodes {
			if ariaLabel != "" && attrValue(node, "aria-label") != ariaLabel {
				return fmt.Errorf("%w: %s.target ariaLabel does not identify the selector-matched nodes", ErrInvalidProvisionalRule, path)
			}
			if role != "" && effectiveSemanticRole(node) != role {
				return fmt.Errorf("%w: %s.target role does not identify the selector-matched nodes", ErrInvalidProvisionalRule, path)
			}
			if roleName != "" && attrValue(node, "aria-label") != roleName {
				return fmt.Errorf("%w: %s.target roleName does not identify the selector-matched nodes", ErrInvalidProvisionalRule, path)
			}
		}
	}
	return nil
}

func strongestSelectorEvidence(snapshots []selectorEvidenceSnapshot, matcher cascadia.Selector) ([]selectorEvidenceSnapshot, [][]*html.Node, error) {
	type group struct {
		snapshots []selectorEvidenceSnapshot
		nodes     [][]*html.Node
		total     int
	}
	groups := map[string]*group{}
	for _, snapshot := range snapshots {
		nodes := matcher.MatchAll(snapshot.root)
		entry := groups[snapshot.state]
		if entry == nil {
			entry = &group{}
			groups[snapshot.state] = entry
		}
		entry.snapshots = append(entry.snapshots, snapshot)
		entry.nodes = append(entry.nodes, nodes)
		entry.total += len(nodes)
	}
	keys := make([]string, 0, len(groups))
	for key := range groups {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var best *group
	sawInconsistent := false
	for _, key := range keys {
		candidate := groups[key]
		if candidate.total == 0 {
			continue
		}
		windowSnapshots, windowNodes, err := consistentSelectorWindow(candidate.snapshots, candidate.nodes, 1)
		if err != nil {
			sawInconsistent = true
			continue
		}
		windowTotal := 0
		for _, nodes := range windowNodes {
			windowTotal += len(nodes)
		}
		if best == nil || windowTotal > best.total {
			best = &group{snapshots: windowSnapshots, nodes: windowNodes, total: windowTotal}
		}
	}
	if best == nil {
		if sawInconsistent {
			return nil, nil, fmt.Errorf("inconsistent recorded evidence")
		}
		return nil, nil, fmt.Errorf("no consistent recorded evidence")
	}
	return best.snapshots, best.nodes, nil
}

func exactStableTargetEvidence(candidates []SelectorEvidenceCandidate, snapshots []selectorEvidenceSnapshot, observed cascadia.Selector, minimum int) (string, []selectorEvidenceSnapshot, [][]*html.Node) {
	best := ""
	var bestSnapshots []selectorEvidenceSnapshot
	var bestNodes [][]*html.Node
	for _, candidate := range candidates {
		if isVolatileSelector(candidate.Selector) || minIntSlice(candidate.Cardinalities) < minimum {
			continue
		}
		stableMatcher, err := cascadia.Compile(candidate.Selector)
		if err != nil {
			continue
		}
		sequences := make(map[int]bool, len(candidate.SnapshotSequences))
		for _, sequence := range candidate.SnapshotSequences {
			sequences[sequence] = true
		}
		exact := true
		candidateSnapshots := make([]selectorEvidenceSnapshot, 0, len(sequences))
		candidateNodes := make([][]*html.Node, 0, len(sequences))
		for _, snapshot := range snapshots {
			if snapshot.state != candidate.State {
				continue
			}
			expected := stableMatcher.MatchAll(snapshot.root)
			if !sameNodeSet(observed.MatchAll(snapshot.root), expected) {
				exact = false
				break
			}
			if sequences[snapshot.sequence] {
				candidateSnapshots = append(candidateSnapshots, snapshot)
				candidateNodes = append(candidateNodes, expected)
			}
		}
		if len(candidateSnapshots) != len(candidate.SnapshotSequences) {
			exact = false
		}
		if exact && (best == "" || stableSelectorLess(candidate.Selector, best)) {
			best = candidate.Selector
			bestSnapshots = candidateSnapshots
			bestNodes = candidateNodes
		}
	}
	return best, bestSnapshots, bestNodes
}

func consistentSelectorWindow(snapshots []selectorEvidenceSnapshot, nodeSets [][]*html.Node, minimum int) ([]selectorEvidenceSnapshot, [][]*html.Node, error) {
	first, last := -1, -1
	for index, nodes := range nodeSets {
		if len(nodes) > 0 {
			if first < 0 {
				first = index
			}
			last = index
		}
	}
	if first < 0 {
		return nil, nil, fmt.Errorf("no consistent recorded evidence")
	}
	expected := len(nodeSets[first])
	if expected < minimum {
		return nil, nil, fmt.Errorf("recorded cardinality is %d", expected)
	}
	for index := first; index <= last; index++ {
		if len(nodeSets[index]) != expected {
			return nil, nil, fmt.Errorf("inconsistent recorded evidence")
		}
	}
	return snapshots[first : last+1], nodeSets[first : last+1], nil
}

func exactStableRelativeReplacement(nodeSets [][]*html.Node, expected cascadia.Selector) string {
	candidates := stableRelativeCandidates(nodeSets)
	best := ""
	for _, candidate := range candidates {
		matcher, err := cascadia.Compile(candidate)
		if err != nil {
			continue
		}
		exact := true
		for _, rows := range nodeSets {
			for _, row := range rows {
				if !sameNodeSet(cascadia.QueryAll(row, matcher), cascadia.QueryAll(row, expected)) {
					exact = false
					break
				}
			}
			if !exact {
				break
			}
		}
		if exact && !isVolatileSelector(candidate) && (best == "" || stableSelectorLess(candidate, best)) {
			best = candidate
		}
	}
	return best
}

func relativeSelectorNodeSets(matcher cascadia.Selector, nodeSets [][]*html.Node) ([][]*html.Node, bool) {
	result := make([][]*html.Node, len(nodeSets))
	for index, rows := range nodeSets {
		result[index] = make([]*html.Node, 0, len(rows))
		for _, row := range rows {
			matches := cascadia.QueryAll(row, matcher)
			if len(matches) != 1 {
				return nil, false
			}
			result[index] = append(result[index], matches[0])
		}
	}
	return result, true
}

func buildSelectorEvidenceCandidates(
	snapshots []selectorEvidenceSnapshot,
	allowDocumentRootAnchor bool,
	maxStructuralDepth int,
) []SelectorEvidenceCandidate {
	type candidateKey struct{ state, selector string }
	type discovery struct {
		snapshotSequences map[int]bool
		repeated          bool
	}
	discovered := map[candidateKey]*discovery{}
	addDiscovery := func(key candidateKey, sequence int, repeated bool) {
		entry := discovered[key]
		if entry == nil {
			entry = &discovery{snapshotSequences: map[int]bool{}}
			discovered[key] = entry
		}
		entry.snapshotSequences[sequence] = true
		entry.repeated = entry.repeated || repeated
	}
	for _, snapshot := range snapshots {
		anchors := stableAnchorIndex(snapshot.root, allowDocumentRootAnchor)
		for _, selector := range anchors {
			if selector == "html" {
				// The implicit document root exists only to stabilize descendant
				// paths. Publishing the whole document as a target would expose
				// a large, low-value optional-field surface.
				continue
			}
			addDiscovery(candidateKey{snapshot.state, selector}, snapshot.sequence, false)
		}
		walkElements(snapshot.root, func(parent *html.Node) {
			anchor := stableStructuralSelector(parent, anchors, maxStructuralDepth)
			if anchor == "" && allowDocumentRootAnchor {
				anchor = stableDocumentRootedStructuralSelector(
					parent,
					anchors,
					maxStructuralDepth,
				)
			}
			if anchor == "" {
				return
			}
			groups := map[string][]*html.Node{}
			for child := parent.FirstChild; child != nil; child = child.NextSibling {
				if child.Type != html.ElementNode {
					continue
				}
				groups["coarse:"+cohortSignature(child)] = append(groups["coarse:"+cohortSignature(child)], child)
				// A coarse tag/id/role cohort preserves broad repeated lists, while
				// stable local selectors expose semantic subcohorts such as ordinary
				// rows beside a special module. The exact direct-child node-set check
				// below prevents a seed from widening or narrowing its own subgroup.
				for _, selector := range stableNodeLocalSelectors(child) {
					groups["local:"+selector] = append(groups["local:"+selector], child)
				}
			}
			for _, nodes := range groups {
				if len(nodes) < 2 {
					continue
				}
				for _, local := range cohortLocalSelectors(nodes) {
					selector := anchor + " > " + local
					matcher, err := cascadia.Compile(local)
					if err == nil && sameNodeSet(matchingElementChildren(parent, matcher), nodes) &&
						!isVolatileSelector(selector) {
						if allowDocumentRootAnchor {
							fullMatcher, fullErr := cascadia.Compile(selector)
							if fullErr != nil ||
								!sameNodeSet(fullMatcher.MatchAll(snapshot.root), nodes) {
								continue
							}
						}
						addDiscovery(candidateKey{snapshot.state, selector}, snapshot.sequence, true)
					}
				}
			}
		})
	}
	keys := make([]candidateKey, 0, len(discovered))
	for key := range discovered {
		keys = append(keys, key)
	}
	keysByState := map[string][]candidateKey{}
	for _, key := range keys {
		keysByState[key.state] = append(keysByState[key.state], key)
	}
	for state := range keysByState {
		stateKeys := keysByState[state]
		sort.Slice(stateKeys, func(i, j int) bool {
			left, right := discovered[stateKeys[i]], discovered[stateKeys[j]]
			if left.repeated != right.repeated {
				return left.repeated
			}
			if len(left.snapshotSequences) != len(right.snapshotSequences) {
				return len(left.snapshotSequences) > len(right.snapshotSequences)
			}
			return stableSelectorLess(stateKeys[i].selector, stateKeys[j].selector)
		})
		keysByState[state] = stateKeys
	}
	keys = keys[:0]
	stateOrder := selectorEvidenceStateOrder(snapshots, keysByState)
	for round := 0; len(keys) < maxSelectorDiscoveryCandidates; round++ {
		added := false
		for _, state := range stateOrder {
			stateKeys := keysByState[state]
			if round >= len(stateKeys) {
				continue
			}
			keys = append(keys, stateKeys[round])
			added = true
			if len(keys) == maxSelectorDiscoveryCandidates {
				break
			}
		}
		if !added {
			break
		}
	}
	candidates := make([]SelectorEvidenceCandidate, 0, len(keys))
	for _, key := range keys {
		matcher, err := cascadia.Compile(key.selector)
		if err != nil {
			continue
		}
		candidate := SelectorEvidenceCandidate{Selector: key.selector, State: key.state}
		var stateSnapshots []selectorEvidenceSnapshot
		var stateNodeSets [][]*html.Node
		for _, snapshot := range snapshots {
			if snapshot.state != key.state {
				continue
			}
			stateSnapshots = append(stateSnapshots, snapshot)
			stateNodeSets = append(stateNodeSets, matcher.MatchAll(snapshot.root))
		}
		evidenceSnapshots, nodeSets, evidenceErr := consistentSelectorWindow(stateSnapshots, stateNodeSets, 1)
		if evidenceErr != nil {
			continue
		}
		for index, snapshot := range evidenceSnapshots {
			candidate.SnapshotSequences = append(candidate.SnapshotSequences, snapshot.sequence)
			candidate.Cardinalities = append(candidate.Cardinalities, len(nodeSets[index]))
		}
		candidate.RelativeSelectors = stableRelativeCandidates(nodeSets)
		if len(candidate.RelativeSelectors) > maxRelativeSelectors {
			candidate.RelativeSelectors = candidate.RelativeSelectors[:maxRelativeSelectors]
		}
		candidates = append(candidates, candidate)
	}
	return candidates
}

func deduplicateSelectorEvidenceCandidates(
	candidates []SelectorEvidenceCandidate,
	snapshots []selectorEvidenceSnapshot,
) []SelectorEvidenceCandidate {
	ordered := append([]SelectorEvidenceCandidate(nil), candidates...)
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].State != ordered[j].State {
			return ordered[i].State < ordered[j].State
		}
		return stableSelectorLess(ordered[i].Selector, ordered[j].Selector)
	})
	seen := map[string]bool{}
	result := make([]SelectorEvidenceCandidate, 0, len(ordered))
	for _, candidate := range ordered {
		matcher, err := cascadia.Compile(candidate.Selector)
		if err != nil {
			continue
		}
		signature := candidate.State + "\x00" + selectorEvidenceNodeSetSignature(candidate.State, matcher, snapshots)
		if seen[signature] {
			continue
		}
		seen[signature] = true
		result = append(result, candidate)
	}
	return result
}

func selectorEvidenceNodeSetSignature(
	state string,
	matcher cascadia.Selector,
	snapshots []selectorEvidenceSnapshot,
) string {
	var signature strings.Builder
	for _, snapshot := range snapshots {
		if snapshot.state != state {
			continue
		}
		signature.WriteString(fmt.Sprintf("%d:", snapshot.sequence))
		for _, node := range matcher.MatchAll(snapshot.root) {
			signature.WriteString(selectorEvidenceNodePath(node))
			signature.WriteByte(',')
		}
		signature.WriteByte(';')
	}
	return signature.String()
}

func selectorEvidenceNodePath(node *html.Node) string {
	indices := make([]int, 0, 8)
	for current := node; current != nil && current.Parent != nil; current = current.Parent {
		index := 0
		for sibling := current.Parent.FirstChild; sibling != nil && sibling != current; sibling = sibling.NextSibling {
			if sibling.Type == html.ElementNode {
				index++
			}
		}
		indices = append(indices, index)
	}
	var result strings.Builder
	for index := len(indices) - 1; index >= 0; index-- {
		if result.Len() > 0 {
			result.WriteByte('.')
		}
		result.WriteString(fmt.Sprint(indices[index]))
	}
	return result.String()
}

func boundSelectorEvidenceCandidates(
	candidates []SelectorEvidenceCandidate,
	snapshots []selectorEvidenceSnapshot,
	limit int,
) []SelectorEvidenceCandidate {
	if limit <= 0 || len(candidates) == 0 {
		return nil
	}
	byState := map[string][]SelectorEvidenceCandidate{}
	for _, candidate := range candidates {
		byState[candidate.State] = append(byState[candidate.State], candidate)
	}
	for state := range byState {
		stateCandidates := byState[state]
		sort.Slice(stateCandidates, func(i, j int) bool {
			return selectorEvidenceCandidateLess(stateCandidates[i], stateCandidates[j])
		})
		byState[state] = stateCandidates
	}
	states := selectorEvidenceStateOrder(snapshots, byState)
	bounded := make([]SelectorEvidenceCandidate, 0, min(limit, len(candidates)))
	for round := 0; len(bounded) < limit; round++ {
		added := false
		for _, state := range states {
			stateCandidates := byState[state]
			if round >= len(stateCandidates) {
				continue
			}
			bounded = append(bounded, stateCandidates[round])
			added = true
			if len(bounded) == limit {
				break
			}
		}
		if !added {
			break
		}
	}
	return bounded
}

func selectorEvidenceCandidateLess(left, right SelectorEvidenceCandidate) bool {
	leftMinimum, rightMinimum := minIntSlice(left.Cardinalities), minIntSlice(right.Cardinalities)
	leftRepeated, rightRepeated := leftMinimum >= 2, rightMinimum >= 2
	if leftRepeated != rightRepeated {
		return leftRepeated
	}
	if len(left.SnapshotSequences) != len(right.SnapshotSequences) {
		return len(left.SnapshotSequences) > len(right.SnapshotSequences)
	}
	if leftMinimum != rightMinimum {
		return leftMinimum > rightMinimum
	}
	if left.Selector != right.Selector {
		return stableSelectorLess(left.Selector, right.Selector)
	}
	return left.State < right.State
}

func selectorEvidenceStateOrder[T any](snapshots []selectorEvidenceSnapshot, values map[string][]T) []string {
	latestSequence := map[string]int{}
	for state := range values {
		latestSequence[state] = -1
	}
	for _, snapshot := range snapshots {
		if _, ok := values[snapshot.state]; !ok {
			continue
		}
		if snapshot.sequence > latestSequence[snapshot.state] {
			latestSequence[snapshot.state] = snapshot.sequence
		}
	}
	states := make([]string, 0, len(values))
	for state := range values {
		states = append(states, state)
	}
	sort.Slice(states, func(i, j int) bool {
		if latestSequence[states[i]] != latestSequence[states[j]] {
			return latestSequence[states[i]] > latestSequence[states[j]]
		}
		return states[i] < states[j]
	})
	return states
}

func stableRelativeCandidates(nodeSets [][]*html.Node) []string {
	if len(nodeSets) == 0 || len(nodeSets[0]) == 0 {
		return nil
	}
	possible := map[string]struct{}{}
	walkElements(nodeSets[0][0], func(node *html.Node) {
		if node == nodeSets[0][0] {
			return
		}
		tag := strings.ToLower(node.Data)
		possible[tag] = struct{}{}
		for _, className := range stableClasses(node) {
			possible["."+className] = struct{}{}
			possible[tag+"."+className] = struct{}{}
		}
		for _, attribute := range stableSemanticAttributeSelectors(node) {
			possible[attribute] = struct{}{}
			possible[tag+attribute] = struct{}{}
		}
		if node.Parent != nil && node.Parent.Type == html.ElementNode {
			parentTag := strings.ToLower(node.Parent.Data)
			possible[parentTag+" > "+tag] = struct{}{}
			for _, className := range stableClasses(node) {
				possible[parentTag+" > "+tag+"."+className] = struct{}{}
			}
			for _, attribute := range stableSemanticAttributeSelectors(node) {
				possible[parentTag+" > "+tag+attribute] = struct{}{}
			}
		}
	})
	ordered := make([]string, 0, len(possible))
	for selector := range possible {
		if isVolatileSelector(selector) {
			continue
		}
		ordered = append(ordered, selector)
	}
	sort.Slice(ordered, func(i, j int) bool { return stableSelectorLess(ordered[i], ordered[j]) })
	type relativeCandidate struct {
		selector string
		nodeSets [][]*html.Node
		hasText  bool
	}
	candidates := make([]relativeCandidate, 0, len(ordered))
	for _, selector := range ordered {
		matcher, err := cascadia.Compile(selector)
		if err != nil {
			continue
		}
		matched, covered := relativeSelectorNodeSets(matcher, nodeSets)
		if !covered {
			continue
		}
		duplicate := false
		for _, existing := range candidates {
			if sameSelectorEvidenceNodeSets(matched, existing.nodeSets) {
				duplicate = true
				break
			}
		}
		if duplicate {
			continue
		}
		candidates = append(candidates, relativeCandidate{
			selector: selector,
			nodeSets: matched,
			hasText:  nodeSetsHaveNonEmptyText(matched),
		})
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].hasText != candidates[j].hasText {
			return candidates[i].hasText
		}
		return stableSelectorLess(candidates[i].selector, candidates[j].selector)
	})
	result := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		result = append(result, candidate.selector)
	}
	return result
}

func extractFieldRequiresRecordedText(raw any) bool {
	switch strings.TrimSpace(stringValue(raw)) {
	case "text", "number", "json", "regex":
		return true
	default:
		return false
	}
}

func nodeSetsHaveNonEmptyText(nodeSets [][]*html.Node) bool {
	if len(nodeSets) == 0 {
		return false
	}
	for _, nodes := range nodeSets {
		if len(nodes) == 0 {
			return false
		}
		for _, node := range nodes {
			if normalizedSemanticText(node) == "" {
				return false
			}
		}
	}
	return true
}

func normalizedSemanticText(node *html.Node) string {
	if node == nil {
		return ""
	}
	var text strings.Builder
	var walk func(*html.Node)
	walk = func(current *html.Node) {
		if current.Type == html.TextNode {
			text.WriteString(current.Data)
			text.WriteByte(' ')
		}
		for child := current.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(node)
	return strings.Join(strings.Fields(text.String()), " ")
}

func sameSelectorEvidenceNodeSets(left, right [][]*html.Node) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if !sameNodeSet(left[index], right[index]) {
			return false
		}
	}
	return true
}

func cohortLocalSelectors(nodes []*html.Node) []string {
	if len(nodes) == 0 {
		return nil
	}
	tag := strings.ToLower(nodes[0].Data)
	classes := stableClasses(nodes[0])
	attributes := stableSemanticAttributeSelectors(nodes[0])
	allHaveID := true
	for _, node := range nodes {
		if node.Data != nodes[0].Data {
			return nil
		}
		allHaveID = allHaveID && hasAttr(node, "id")
		classes = intersectStrings(classes, stableClasses(node))
		attributes = intersectStrings(attributes, stableSemanticAttributeSelectors(node))
	}
	forms := map[string]struct{}{tag: {}}
	for _, className := range classes {
		forms["."+className] = struct{}{}
		forms[tag+"."+className] = struct{}{}
	}
	if len(classes) > 1 {
		forms[tag+"."+strings.Join(classes, ".")] = struct{}{}
	}
	for _, attribute := range attributes {
		forms[attribute] = struct{}{}
		forms[tag+attribute] = struct{}{}
	}
	if allHaveID {
		for form := range cloneStringSet(forms) {
			forms[form+"[id]"] = struct{}{}
		}
	}
	result := make([]string, 0, len(forms))
	for form := range forms {
		result = append(result, form)
	}
	sort.Slice(result, func(i, j int) bool { return stableSelectorLess(result[i], result[j]) })
	return result
}

func stableAnchorIndex(root *html.Node, allowDocumentRoot bool) map[*html.Node]string {
	occurrences := map[string][]*html.Node{}
	walkElements(root, func(node *html.Node) {
		if allowDocumentRoot && node.Data == "html" && node.Parent != nil &&
			node.Parent.Type == html.DocumentNode {
			occurrences["html"] = append(occurrences["html"], node)
		}
		for _, selector := range stableSelfAnchorSelectors(node) {
			occurrences[selector] = append(occurrences[selector], node)
		}
	})
	anchors := map[*html.Node]string{}
	for selector, nodes := range occurrences {
		if len(nodes) != 1 {
			continue
		}
		current := anchors[nodes[0]]
		if current == "" || stableSelectorLess(selector, current) {
			anchors[nodes[0]] = selector
		}
	}
	return anchors
}

func matchingElementChildren(parent *html.Node, matcher cascadia.Selector) []*html.Node {
	result := []*html.Node{}
	for child := parent.FirstChild; child != nil; child = child.NextSibling {
		if child.Type == html.ElementNode && matcher(child) {
			result = append(result, child)
		}
	}
	return result
}

func stableStructuralSelector(
	node *html.Node,
	anchors map[*html.Node]string,
	maxDepth int,
) string {
	if selector := anchors[node]; selector != "" {
		return selector
	}
	path := []string{}
	current := node
	for depth := 0; depth < maxDepth && current != nil && current.Type == html.ElementNode; depth++ {
		local := stableUniqueChildSelector(current)
		if local == "" {
			return ""
		}
		path = append(path, local)
		current = current.Parent
		if anchor := anchors[current]; anchor != "" {
			for left, right := 0, len(path)-1; left < right; left, right = left+1, right-1 {
				path[left], path[right] = path[right], path[left]
			}
			return anchor + " > " + strings.Join(path, " > ")
		}
	}
	return ""
}

func stableDocumentRootedStructuralSelector(
	node *html.Node,
	anchors map[*html.Node]string,
	maxDepth int,
) string {
	path := []string{}
	current := node
	for depth := 0; depth < maxDepth &&
		current != nil &&
		current.Type == html.ElementNode; depth++ {
		selectors := stableNodeLocalSelectors(current)
		if len(selectors) == 0 {
			return ""
		}
		path = append(path, selectors[0])
		current = current.Parent
		anchor := anchors[current]
		if anchor == "" {
			continue
		}
		for left, right := 0, len(path)-1; left < right; left, right = left+1, right-1 {
			path[left], path[right] = path[right], path[left]
		}
		candidate := anchor + " > " + strings.Join(path, " > ")
		matcher, err := cascadia.Compile(candidate)
		if err != nil {
			return ""
		}
		root := current
		for root.Parent != nil {
			root = root.Parent
		}
		for _, match := range matcher.MatchAll(root) {
			if match == node {
				return candidate
			}
		}
		return ""
	}
	return ""
}

func stableUniqueChildSelector(node *html.Node) string {
	if node == nil || node.Parent == nil {
		return ""
	}
	best := ""
	for _, selector := range stableNodeLocalSelectors(node) {
		matcher, err := cascadia.Compile(selector)
		if err != nil {
			continue
		}
		matches := matchingElementChildren(node.Parent, matcher)
		if len(matches) == 1 && matches[0] == node && (best == "" || stableSelectorLess(selector, best)) {
			best = selector
		}
	}
	return best
}

func stableNodeLocalSelectors(node *html.Node) []string {
	tag := strings.ToLower(node.Data)
	forms := map[string]struct{}{tag: {}}
	for _, className := range stableClasses(node) {
		forms["."+className] = struct{}{}
		forms[tag+"."+className] = struct{}{}
	}
	for _, attribute := range stableSemanticAttributeSelectors(node) {
		forms[attribute] = struct{}{}
		forms[tag+attribute] = struct{}{}
	}
	result := make([]string, 0, len(forms))
	for form := range forms {
		if !isVolatileSelector(form) {
			result = append(result, form)
		}
	}
	sort.Slice(result, func(i, j int) bool { return stableSelectorLess(result[i], result[j]) })
	return result
}

func stableSelfAnchorSelectors(node *html.Node) []string {
	result := []string{}
	for _, name := range []string{"data-testid", "data-qa", "data-automation-id"} {
		if value := attrValue(node, name); stableAttributeValue(value) {
			result = append(result, fmt.Sprintf(`[%s="%s"]`, name, cssString(value)))
		}
	}
	if value := attrValue(node, "id"); stableID(value) {
		result = append(result, "#"+value)
	}
	sort.Slice(result, func(i, j int) bool { return stableSelectorLess(result[i], result[j]) })
	return result
}

func stableSemanticAttributeSelectors(node *html.Node) []string {
	result := []string{}
	for _, name := range []string{"data-testid", "data-qa", "data-automation-id", "role", "name", "itemprop"} {
		if value := attrValue(node, name); stableAttributeValue(value) {
			result = append(result, fmt.Sprintf(`[%s="%s"]`, name, cssString(value)))
		}
	}
	sort.Strings(result)
	return result
}

func semanticNodeToHTML(value map[string]any) *html.Node {
	nodeType, _ := value["type"].(string)
	if nodeType == "text" {
		return &html.Node{Type: html.TextNode, Data: stringValue(value["text"])}
	}
	if nodeType != "element" {
		return nil
	}
	tag := strings.ToLower(strings.TrimSpace(stringValue(value["tagName"])))
	if tag == "" {
		return nil
	}
	node := &html.Node{Type: html.ElementNode, Data: tag}
	if sanitization, present := value["sanitization"]; present {
		applyRecordedSanitizationEvidence(node, sanitization)
	}
	if rendered, recorded := value["rendered"].(bool); recorded && !rendered {
		// Private evidence metadata: never sourced from page attributes and
		// never considered by stable selector discovery. Older recordings omit
		// it; known hidden attributes plus live replay visibility still protect
		// that legacy ambiguity.
		node.Attr = append(node.Attr, html.Attribute{Key: recordedRenderedAttribute, Val: "false"})
	}
	if attrs, ok := value["attributes"].([]any); ok {
		for _, raw := range attrs {
			attr, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			name := strings.ToLower(strings.TrimSpace(stringValue(attr["name"])))
			if name != "" && !isRecordedEvidenceAttribute(name) {
				node.Attr = append(node.Attr, html.Attribute{Key: name, Val: stringValue(attr["value"])})
			}
		}
	}
	// Browser selectors cannot cross iframe document boundaries. The iframe
	// element remains queryable, but captured frame children are isolated from
	// top-level evidence until an explicitly framed validator is added.
	if tag == "iframe" {
		if children, present := value["children"]; present &&
			!recordedCapturedFrameChildren(value, children) {
			appendFailClosedRecordedSanitization(node)
		}
		return node
	}
	if childrenValue, present := value["children"]; present {
		children, ok := childrenValue.([]any)
		if !ok {
			appendFailClosedRecordedSanitization(node)
			return node
		}
		for _, raw := range children {
			childMap, ok := raw.(map[string]any)
			if !ok {
				appendFailClosedRecordedSanitization(node)
				continue
			}
			if child := semanticNodeToHTML(childMap); child != nil {
				if child.Type == html.TextNode {
					if sanitization, present := childMap["sanitization"]; present {
						applyRecordedSanitizationEvidence(node, sanitization)
					}
					if _, present := childMap["rendered"]; present {
						node.Attr = append(node.Attr, html.Attribute{
							Key: recordedNonRenderedSubtreeAttribute, Val: "true",
						})
					}
				}
				node.AppendChild(child)
			} else {
				appendFailClosedRecordedSanitization(node)
			}
		}
	}
	return node
}

func recordedCapturedFrameChildren(value map[string]any, children any) bool {
	items, ok := children.([]any)
	if !ok {
		return false
	}
	if len(items) == 0 {
		return true
	}
	matched, _ := value["frameMatched"].(bool)
	status, _ := value["frameStatus"].(string)
	return matched && status == "captured"
}

func isRecordedEvidenceAttribute(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case recordedRenderedAttribute,
		recordedSanitizedMarkupAttribute,
		recordedSanitizedContentAttribute,
		recordedSanitizedAttrsAttribute,
		recordedSanitizationUnknownAttribute,
		recordedNonRenderedSubtreeAttribute:
		return true
	default:
		return false
	}
}

func applyRecordedSanitizationEvidence(node *html.Node, raw any) {
	if node == nil || node.Type != html.ElementNode {
		return
	}
	if raw == nil {
		appendFailClosedRecordedSanitization(node)
		return
	}
	value, ok := raw.(map[string]any)
	if !ok || len(value) == 0 {
		appendFailClosedRecordedSanitization(node)
		return
	}
	markup := false
	content := false
	attributes := []string{}
	valid := true
	for key, item := range value {
		switch key {
		case "markupAltered":
			flag, flagOK := item.(bool)
			if !flagOK || !flag {
				valid = false
			} else {
				markup = true
			}
		case "contentOmitted":
			flag, flagOK := item.(bool)
			if !flagOK || !flag {
				valid = false
			} else {
				markup = true
				content = true
			}
		case "alteredAttributes":
			var values []any
			switch typed := item.(type) {
			case []any:
				values = typed
			case []string:
				values = make([]any, len(typed))
				for index, name := range typed {
					values[index] = name
				}
			default:
				valid = false
			}
			if len(values) == 0 || len(values) > 64 {
				valid = false
			}
			for _, candidate := range values {
				name, nameOK := candidate.(string)
				name = strings.ToLower(strings.TrimSpace(name))
				if !nameOK || name == "" || len(name) > 128 ||
					strings.ContainsAny(name, " \t\r\n\"'<>`=") {
					valid = false
					break
				}
				attributes = append(attributes, name)
			}
			markup = true
		default:
			valid = false
		}
	}
	if !valid || (!markup && !content && len(attributes) == 0) {
		appendFailClosedRecordedSanitization(node)
		return
	}
	if markup {
		node.Attr = append(node.Attr, html.Attribute{
			Key: recordedSanitizedMarkupAttribute, Val: "true",
		})
	}
	if content {
		node.Attr = append(node.Attr, html.Attribute{
			Key: recordedSanitizedContentAttribute, Val: "true",
		})
	}
	if len(attributes) > 0 {
		sort.Strings(attributes)
		node.Attr = append(node.Attr, html.Attribute{
			Key: recordedSanitizedAttrsAttribute, Val: strings.Join(attributes, " "),
		})
	}
}

func appendFailClosedRecordedSanitization(node *html.Node) {
	node.Attr = append(node.Attr,
		html.Attribute{Key: recordedSanitizedMarkupAttribute, Val: "true"},
		html.Attribute{Key: recordedSanitizedContentAttribute, Val: "true"},
		html.Attribute{Key: recordedSanitizedAttrsAttribute, Val: "*"},
	)
}

// compileBrowserSelector rejects Cascadia extensions that Chromium's
// querySelector/querySelectorAll do not implement. Cascadia remains the
// deterministic server matcher, but it must not broaden the public DSL's CSS
// grammar and approve a selector that the replay runtime can only ignore.
func compileBrowserSelector(selector string) (cascadia.Selector, error) {
	code := selectorOutsideStrings(selector)
	switch {
	case unsupportedPseudo.MatchString(code):
		return nil, fmt.Errorf("uses a non-browser pseudo-class")
	case strings.Contains(code, "!=") || strings.Contains(code, "#="):
		return nil, fmt.Errorf("uses a non-browser attribute operator")
	case strings.Contains(code, "::"):
		return nil, fmt.Errorf("pseudo-elements do not identify DOM elements")
	}
	return cascadia.Compile(selector)
}

func selectorOutsideStrings(selector string) string {
	result := []rune(selector)
	quote := rune(0)
	escaped := false
	for index, current := range result {
		if quote != 0 {
			result[index] = ' '
			if escaped {
				escaped = false
				continue
			}
			if current == '\\' {
				escaped = true
				continue
			}
			if current == quote {
				quote = 0
			}
			continue
		}
		if current == '\'' || current == '"' {
			quote = current
			result[index] = ' '
		}
	}
	return string(result)
}

func isVolatileSelector(selector string) bool {
	if isPositionalSelector(selector) {
		return true
	}
	code := selectorOutsideStrings(selector)
	if strings.Contains(code, `\`) || selectorHasVolatileAttribute(selector) {
		return true
	}
	for _, match := range classSelectorToken.FindAllStringSubmatch(code, -1) {
		if volatileToken(match[1]) {
			return true
		}
	}
	for _, match := range idSelectorToken.FindAllStringSubmatch(code, -1) {
		if !stableID(match[1]) {
			return true
		}
	}
	return false
}

func isPositionalSelector(selector string) bool {
	lower := strings.ToLower(selectorOutsideStrings(selector))
	for _, marker := range []string{":nth-", ":first-child", ":last-child", ":first-of-type", ":last-of-type", ":only-child", ":only-of-type"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func selectorUsesNonUniqueID(selector string, snapshots []selectorEvidenceSnapshot) bool {
	for _, match := range idSelectorToken.FindAllStringSubmatch(selectorOutsideStrings(selector), -1) {
		idMatcher, err := cascadia.Compile("#" + match[1])
		if err != nil {
			return true
		}
		for _, snapshot := range snapshots {
			if len(idMatcher.MatchAll(snapshot.root)) != 1 {
				return true
			}
		}
	}
	return false
}

func volatileToken(token string) bool {
	lower := strings.ToLower(token)
	if strings.HasPrefix(lower, "css-") || strings.HasPrefix(lower, "jsx-") || len(token) > 80 {
		return true
	}
	parts := strings.FieldsFunc(token, func(r rune) bool { return r == '_' || r == '-' })
	if len(token) >= 6 && len(token) <= 32 && hexLikeToken.MatchString(token) && containsLetterAndDigit(token) {
		return true
	}
	if looksLikeCompactHash(token) {
		return true
	}
	if len(parts) > 1 {
		suffix := parts[len(parts)-1]
		if len(suffix) >= 5 && len(suffix) <= 12 && containsLowerUpperDigit(suffix) {
			return true
		}
		if len(suffix) >= 6 && len(suffix) <= 32 && (digitsOnlyToken.MatchString(suffix) || (hexLikeToken.MatchString(suffix) && containsLetterAndDigit(suffix))) {
			return true
		}
	}
	if len(token) >= 12 && containsLowerUpperDigit(token) {
		return true
	}
	return false
}

func selectorHasVolatileAttribute(selector string) bool {
	for _, match := range attributeToken.FindAllStringSubmatch(selector, -1) {
		name := strings.ToLower(match[1])
		op := match[2]
		value := match[3]
		if value == "" {
			value = match[4]
		}
		if value == "" {
			value = match[5]
		}
		switch name {
		case "id":
			if op != "=" || !stableID(value) {
				return true
			}
		case "class":
			classes := strings.Fields(value)
			if (op != "=" && op != "~=") || len(classes) == 0 || (op == "=" && len(classes) != 1) {
				return true
			}
			for _, className := range classes {
				if !simpleCSSIdentifier.MatchString(className) || volatileToken(className) {
					return true
				}
			}
		}
	}
	return false
}

func looksLikeCompactHash(token string) bool {
	if len(token) < 10 || len(token) > 32 {
		return false
	}
	letters, digits, transitions := 0, 0, 0
	previousKind := byte(0)
	for index := 0; index < len(token); index++ {
		current := token[index]
		kind := byte(0)
		switch {
		case current >= 'a' && current <= 'z', current >= 'A' && current <= 'Z':
			letters++
			kind = 'a'
		case current >= '0' && current <= '9':
			digits++
			kind = '0'
		default:
			return false
		}
		if previousKind != 0 && previousKind != kind {
			transitions++
		}
		previousKind = kind
	}
	return letters >= 4 && digits >= 4 && transitions >= 4
}

func effectiveSemanticRole(node *html.Node) string {
	if explicit := strings.TrimSpace(attrValue(node, "role")); explicit != "" {
		return explicit
	}
	tag := strings.ToLower(node.Data)
	if tag != "input" {
		return implicitRoleByTag[tag]
	}
	inputType := strings.ToLower(strings.TrimSpace(attrValue(node, "type")))
	if inputType == "hidden" {
		return ""
	}
	if role := implicitRoleByInputType[inputType]; role != "" {
		return role
	}
	return "textbox"
}

func stableID(value string) bool {
	if !simpleCSSIdentifier.MatchString(value) || uuidLikeToken.MatchString(value) || volatileToken(value) {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return true
		}
	}
	return false
}

func stableAttributeValue(value string) bool {
	if value == "" || len(value) > 128 || volatileToken(value) {
		return false
	}
	for _, current := range value {
		if current < 0x20 || current == 0x7f {
			return false
		}
	}
	return true
}

func containsLowerUpperDigit(value string) bool {
	hasLower, hasUpper, hasDigit := false, false, false
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z':
			hasLower = true
		case r >= 'A' && r <= 'Z':
			hasUpper = true
		case r >= '0' && r <= '9':
			hasDigit = true
		}
	}
	return hasLower && hasUpper && hasDigit
}

func containsLetterAndDigit(value string) bool {
	hasLetter, hasDigit := false, false
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
			hasLetter = true
		case r >= '0' && r <= '9':
			hasDigit = true
		}
	}
	return hasLetter && hasDigit
}

func stableClasses(node *html.Node) []string {
	values := strings.Fields(attrValue(node, "class"))
	result := values[:0]
	for _, value := range values {
		if simpleCSSIdentifier.MatchString(value) && !volatileToken(value) {
			result = append(result, value)
		}
	}
	sort.Strings(result)
	return result
}

func cohortSignature(node *html.Node) string {
	return strings.ToLower(node.Data) + fmt.Sprintf("|id=%t|role=%s", hasAttr(node, "id"), attrValue(node, "role"))
}

func selectorStateKey(raw string) string {
	raw = strings.TrimSpace(raw)
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		if index := strings.IndexAny(raw, "?#"); index >= 0 {
			return raw[:index]
		}
		return raw
	}
	state := strings.ToLower(parsed.Scheme+"://"+parsed.Host) + parsed.EscapedPath()
	// Query names and values can contain recorded page or operator data, so they
	// must never enter selector evidence. Query presence is nevertheless part of
	// the navigation state: applications such as DuckDuckGo serve both their
	// homepage and populated results at the same path. Without this opaque
	// marker, consistency windows merge those different DOMs and can discard all
	// result-only extraction candidates.
	if parsed.RawQuery != "" {
		state += "?__aegis_query__"
	}
	return state
}

func stableSelectorLess(left, right string) bool {
	leftQuality, rightQuality := selectorQuality(left), selectorQuality(right)
	if leftQuality != rightQuality {
		return leftQuality > rightQuality
	}
	if len(left) != len(right) {
		return len(left) < len(right)
	}
	return left < right
}

func selectorQuality(selector string) int {
	switch {
	case strings.Contains(selector, "data-testid") || strings.Contains(selector, "data-qa") || strings.Contains(selector, "data-automation-id"):
		return 5
	case strings.Contains(selector, "#"):
		return 4
	case strings.Contains(selector, "[name=") || strings.Contains(selector, "[role=") || strings.Contains(selector, "[aria-"):
		return 3
	case strings.Contains(selector, "."):
		return 2
	default:
		return 1
	}
}

func walkElements(node *html.Node, visit func(*html.Node)) {
	if node == nil {
		return
	}
	if node.Type == html.ElementNode {
		visit(node)
	}
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		walkElements(child, visit)
	}
}

func attrValue(node *html.Node, name string) string {
	for _, attr := range node.Attr {
		if strings.EqualFold(attr.Key, name) {
			return attr.Val
		}
	}
	return ""
}

func hasAttr(node *html.Node, name string) bool {
	for _, attr := range node.Attr {
		if strings.EqualFold(attr.Key, name) {
			return true
		}
	}
	return false
}

func sameNodeSet(left, right []*html.Node) bool {
	if len(left) != len(right) {
		return false
	}
	counts := map[*html.Node]int{}
	for _, node := range left {
		counts[node]++
	}
	for _, node := range right {
		counts[node]--
		if counts[node] < 0 {
			return false
		}
	}
	return true
}

func intersectStrings(left, right []string) []string {
	set := map[string]bool{}
	for _, value := range right {
		set[value] = true
	}
	result := make([]string, 0, len(left))
	for _, value := range left {
		if set[value] {
			result = append(result, value)
		}
	}
	return result
}

func cloneStringSet(source map[string]struct{}) map[string]struct{} {
	result := make(map[string]struct{}, len(source))
	for key := range source {
		result[key] = struct{}{}
	}
	return result
}

func minIntSlice(values []int) int {
	if len(values) == 0 {
		return 0
	}
	result := values[0]
	for _, value := range values[1:] {
		if value < result {
			result = value
		}
	}
	return result
}

func cssString(value string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(value)
}

func stringValue(value any) string {
	text, _ := value.(string)
	return text
}
