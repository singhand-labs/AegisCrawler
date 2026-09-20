package rule

import (
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/andybalholm/cascadia"
	"github.com/singhand-labs/AegisCrawler/internal/models"
	"golang.org/x/net/html"
)

const (
	selectorPromptCatalogVersionV1       = "selector-catalog-v1"
	selectorPromptCatalogVersionV2       = "selector-catalog-v2"
	selectorPromptCatalogVersionV3       = "selector-catalog-v3"
	selectorPromptCatalogVersionV4       = "selector-catalog-v4"
	selectorPromptCatalogVersion         = "selector-catalog-v5"
	selectorPromptV2DigestBytes          = 9
	maxPromptFieldsPerTarget             = 48
	maxPromptV3FieldsPerTarget           = 16
	maxPromptFieldDepth                  = 3
	recordedRenderedAttribute            = "data-aegis-recorded-rendered"
	recordedSanitizedMarkupAttribute     = "data-aegis-recorded-sanitized-markup"
	recordedSanitizedContentAttribute    = "data-aegis-recorded-sanitized-content"
	recordedSanitizedAttrsAttribute      = "data-aegis-recorded-sanitized-attributes"
	recordedSanitizationUnknownAttribute = "data-aegis-recorded-sanitization-unknown"
	recordedNonRenderedSubtreeAttribute  = "data-aegis-recorded-nonrendered-subtree"
)

var selectorCandidateHookNames = []string{"beforeAll", "afterAll", "onError", "cleanup"}

// SelectorPromptCatalog is the exact, bounded, page-text-free catalog sent to a
// provider. Observed CSS is included only as a read-only mapping hint. The
// provider can return only opaque IDs in extraction positions; the server keeps
// the corresponding DOM evidence in private lookups on SelectorEvidenceCatalog.
type SelectorPromptCatalog struct {
	Version     string                          `json:"version"`
	CatalogHash string                          `json:"catalogHash"`
	Candidates  []SelectorPromptTargetCandidate `json:"candidates"`
}

type SelectorPromptTargetCandidate struct {
	RowCandidateID    string                         `json:"rowCandidateId,omitempty"`
	TargetCandidateID string                         `json:"targetCandidateId,omitempty"`
	ObservedSelector  string                         `json:"observedSelector"`
	State             string                         `json:"state"`
	SnapshotSequences []int                          `json:"snapshotSequences"`
	Cardinalities     []int                          `json:"cardinalities"`
	FieldCandidates   []SelectorPromptFieldCandidate `json:"fieldCandidates,omitempty"`
}

type SelectorPromptFieldCandidate struct {
	FieldCandidateID         string   `json:"fieldCandidateId"`
	ParentFieldCandidateID   string   `json:"parentFieldCandidateId,omitempty"`
	ObservedRelativeSelector string   `json:"observedRelativeSelector,omitempty"`
	SupportedTypes           []string `json:"supportedTypes"`
	NonEmptyText             bool     `json:"nonEmptyText"`
}

type SelectorCandidateResolutionReport struct {
	Targets         int `json:"targets"`
	Fields          int `json:"fields"`
	OrdinaryTargets int `json:"ordinaryTargets,omitempty"`
}

const (
	providerTargetFamilyRef             = "ref"
	providerTargetFamilySelector        = "selector"
	providerTargetFamilySelectorVisible = "selectorVisible"
	providerTargetFamilySelectorAny     = "selectorUnfiltered"
	providerTargetFamilyText            = "text"
	providerTargetFamilyTextVisible     = "textVisible"
	providerTargetFamilyAriaLabel       = "ariaLabel"
	providerTargetFamilyRole            = "role"
)

// SelectorCandidateSelectionError identifies the narrow class of provider
// failures that a bounded opaque-ID-only repair may address. Callers must use
// errors.As rather than matching diagnostics: ordinary schema, shape, hash,
// safety, and evidence failures are deliberately ineligible.
type SelectorCandidateSelectionError struct {
	Code   string `json:"code"`
	Slot   string `json:"slot"`
	Detail string `json:"detail"`
}

func (e *SelectorCandidateSelectionError) Error() string {
	if e == nil {
		return ErrInvalidProvisionalRule.Error()
	}
	return fmt.Sprintf("%v: %s", ErrInvalidProvisionalRule, e.Detail)
}

func (e *SelectorCandidateSelectionError) Unwrap() error {
	return ErrInvalidProvisionalRule
}

func candidateSelectionError(code, slot, format string, args ...any) error {
	return &SelectorCandidateSelectionError{
		Code: code, Slot: slot, Detail: fmt.Sprintf(format, args...),
	}
}

type providerTargetEvidence struct {
	prompt   SelectorPromptTargetCandidate
	selector string
	state    string
	nodeSets [][]*html.Node
	fields   []*providerFieldEvidence
}

type providerFieldEvidence struct {
	prompt   SelectorPromptFieldCandidate
	selector string
	targetID string
	nodeSets [][]*html.Node
}

type trustedTargetCandidate struct {
	candidate  SelectorEvidenceCandidate
	fieldPaths [][]string
}

type opaqueCandidateIDAssigner struct {
	version     string
	byCanonical map[string]string
	byID        map[string]string
}

func newOpaqueCandidateIDAssigner(version string) *opaqueCandidateIDAssigner {
	return &opaqueCandidateIDAssigner{
		version:     version,
		byCanonical: map[string]string{},
		byID:        map[string]string{},
	}
}

func (a *opaqueCandidateIDAssigner) assign(kind, canonical string) string {
	prefix := kind
	digestBytes := 16
	if a.version == selectorPromptCatalogVersionV2 ||
		a.version == selectorPromptCatalogVersionV3 ||
		a.version == selectorPromptCatalogVersionV4 ||
		a.version == selectorPromptCatalogVersion {
		switch kind {
		case "row":
			prefix = "r"
		case "target":
			prefix = "t"
		case "field":
			prefix = "f"
		}
		digestBytes = selectorPromptV2DigestBytes
	}
	key := kind + "\x00" + canonical
	if existing := a.byCanonical[key]; existing != "" {
		return existing
	}
	sum := sha256.Sum256([]byte(key))
	id := prefix + "_" + base64.RawURLEncoding.EncodeToString(sum[:digestBytes])
	if other, collision := a.byID[id]; collision && other != key {
		id = prefix + "_" + base64.RawURLEncoding.EncodeToString(sum[:])
		if other, collision = a.byID[id]; collision && other != key {
			secondary := sha512.Sum512([]byte(key))
			id += "_" + base64.RawURLEncoding.EncodeToString(secondary[:])
		}
	}
	a.byCanonical[key] = id
	a.byID[id] = key
	return id
}

// PrepareProviderPrompt builds one prompt scope and rewrites trusted CSS in a
// copy of baseline/current rule to opaque IDs. The original rule is unchanged.
// Referenced extraction candidates are pinned ahead of the ordinary bounded
// catalog so replay repair does not lose a previously accepted selector.
func (c *SelectorEvidenceCatalog) PrepareProviderPrompt(trusted *models.Rule) (string, *models.Rule, error) {
	return c.prepareProviderPrompt(trusted, selectorPromptCatalogVersion)
}

// ReconstructProviderPrompt rebuilds the exact catalog version named by a
// stored provider prompt. Durable history must never be reinterpreted using a
// newer alias format.
func (c *SelectorEvidenceCatalog) ReconstructProviderPrompt(
	storedPrompt string,
	trusted *models.Rule,
) (string, *models.Rule, error) {
	var stored struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal([]byte(storedPrompt), &stored); err != nil {
		return "", nil, fmt.Errorf("%w: stored selector catalog is invalid: %v", ErrSelectorCatalogUnavailable, err)
	}
	if stored.Version != selectorPromptCatalogVersionV1 &&
		stored.Version != selectorPromptCatalogVersionV2 &&
		stored.Version != selectorPromptCatalogVersionV3 &&
		stored.Version != selectorPromptCatalogVersionV4 &&
		stored.Version != selectorPromptCatalogVersion {
		return "", nil, fmt.Errorf(
			"%w: unsupported stored selector catalog version %q",
			ErrSelectorCatalogUnavailable,
			stored.Version,
		)
	}
	return c.prepareProviderPrompt(trusted, stored.Version)
}

func (c *SelectorEvidenceCatalog) prepareProviderPrompt(
	trusted *models.Rule,
	version string,
) (string, *models.Rule, error) {
	if version != selectorPromptCatalogVersionV1 &&
		version != selectorPromptCatalogVersionV2 &&
		version != selectorPromptCatalogVersionV3 &&
		version != selectorPromptCatalogVersionV4 &&
		version != selectorPromptCatalogVersion {
		return "", nil, fmt.Errorf("%w: unsupported selector catalog version %q", ErrSelectorCatalogUnavailable, version)
	}
	if c == nil || len(c.snapshots) == 0 {
		return "", nil, fmt.Errorf("%w: selector evidence requires complete semantic snapshots", ErrSelectorSourceUnavailable)
	}
	_, versionValidationCandidates := c.selectorCandidatesForVersion(version)
	pinned, err := c.trustedTargetCandidates(trusted, versionValidationCandidates, version)
	if err != nil {
		return "", nil, fmt.Errorf("%w: trusted extraction contract cannot be mapped: %v", ErrSelectorSourceUnavailable, err)
	}
	ordered := c.orderedPromptCandidates(pinned, version)
	assigner := newOpaqueCandidateIDAssigner(version)
	targets := make([]*providerTargetEvidence, 0, len(ordered))
	emittedTargetIDs := map[string]struct{}{}
	pinnedByTarget := map[string]trustedTargetCandidate{}
	for _, pin := range pinned {
		pinnedByTarget[selectorPromptTargetKey(pin.candidate)] = pin
	}
	for _, candidate := range ordered {
		pin, isPinned := pinnedByTarget[selectorPromptTargetKey(candidate)]
		target, buildErr := c.buildProviderTargetEvidence(
			candidate, assigner, pin.fieldPaths, !isPinned, version,
		)
		if buildErr != nil {
			if isPinned {
				return "", nil, fmt.Errorf("%w: trusted extraction field evidence is invalid: %v", ErrSelectorSourceUnavailable, buildErr)
			}
			continue
		}
		targetID := target.prompt.RowCandidateID
		if targetID == "" {
			targetID = target.prompt.TargetCandidateID
		}
		// Different observed selectors can authenticate the exact same node sets.
		// Their opaque ID is intentionally evidence-derived, so expose that
		// canonical target only once and keep strict duplicate rejection as a
		// defense-in-depth check for malformed stored/provider catalogs.
		if _, duplicate := emittedTargetIDs[targetID]; duplicate {
			continue
		}
		trial := append(append([]*providerTargetEvidence(nil), targets...), target)
		prompt, _, marshalErr := marshalSelectorPromptCatalog(version, trial)
		if marshalErr != nil {
			return "", nil, fmt.Errorf("%w: %v", ErrSelectorCatalogUnavailable, marshalErr)
		}
		if len(prompt) > maxSelectorEvidencePromptBytes {
			if isPinned {
				return "", nil, fmt.Errorf("%w: trusted extraction selector evidence exceeds the provider prompt bound", ErrSelectorCatalogUnavailable)
			}
			continue
		}
		if isPinned {
			withOptional, optionalErr := c.buildProviderTargetEvidence(
				candidate, assigner, pin.fieldPaths, true, version,
			)
			if optionalErr != nil {
				return "", nil, fmt.Errorf("%w: trusted extraction field evidence is invalid: %v", ErrSelectorSourceUnavailable, optionalErr)
			}
			optionalTrial := append(append([]*providerTargetEvidence(nil), targets...), withOptional)
			optionalPrompt, _, marshalErr := marshalSelectorPromptCatalog(version, optionalTrial)
			if marshalErr != nil {
				return "", nil, fmt.Errorf("%w: %v", ErrSelectorCatalogUnavailable, marshalErr)
			}
			if len(optionalPrompt) <= maxSelectorEvidencePromptBytes {
				target = withOptional
				trial = optionalTrial
			}
		}
		targets = trial
		emittedTargetIDs[targetID] = struct{}{}
	}
	prompt, hash, err := marshalSelectorPromptCatalog(version, targets)
	if err != nil {
		return "", nil, fmt.Errorf("%w: %v", ErrSelectorCatalogUnavailable, err)
	}
	c.promptCatalogHash = hash
	c.promptTargets = map[string]*providerTargetEvidence{}
	c.promptFields = map[string]*providerFieldEvidence{}
	for _, target := range targets {
		if id := target.prompt.RowCandidateID; id != "" {
			c.promptTargets[id] = target
		}
		if id := target.prompt.TargetCandidateID; id != "" {
			c.promptTargets[id] = target
		}
		for _, field := range target.fields {
			c.promptFields[field.prompt.FieldCandidateID] = field
		}
	}
	if trusted == nil {
		return prompt, nil, nil
	}
	providerRule, err := copySelectorCandidateRule(trusted)
	if err != nil {
		return "", nil, fmt.Errorf("%w: trusted rule cannot be copied: %v", ErrSelectorSourceUnavailable, err)
	}
	if err := c.rewriteTrustedExtractionSelectors(providerRule); err != nil {
		return "", nil, fmt.Errorf("%w: trusted extraction contract cannot be reverse-mapped: %v", ErrSelectorSourceUnavailable, err)
	}
	return prompt, providerRule, nil
}

// PromptEvidenceJSON returns a deterministic prompt catalog without a trusted
// rule. Call PrepareProviderPrompt when baseline/current extraction selectors
// need to be pinned and reverse-mapped.
func (c *SelectorEvidenceCatalog) PromptEvidenceJSON() string {
	prompt, _, err := c.PrepareProviderPrompt(nil)
	if err != nil {
		return `{"version":"selector-catalog-v5","catalogHash":"","candidates":[]}`
	}
	return prompt
}

// PreparedProviderPromptCatalogHash returns the authenticated hash of the exact
// opaque candidate catalog most recently prepared for this workflow attempt.
// It is empty until PrepareProviderPrompt succeeds.
func (c *SelectorEvidenceCatalog) PreparedProviderPromptCatalogHash() string {
	if c == nil {
		return ""
	}
	return c.promptCatalogHash
}

func marshalSelectorPromptCatalog(version string, targets []*providerTargetEvidence) (string, string, error) {
	candidates := make([]SelectorPromptTargetCandidate, len(targets))
	for index, target := range targets {
		candidates[index] = target.prompt
	}
	body := struct {
		Version    string                          `json:"version"`
		Candidates []SelectorPromptTargetCandidate `json:"candidates"`
	}{Version: version, Candidates: candidates}
	encodedBody, err := json.Marshal(body)
	if err != nil {
		return "", "", err
	}
	sum := sha256.Sum256(encodedBody)
	hash := base64.RawURLEncoding.EncodeToString(sum[:])
	encoded, err := json.Marshal(SelectorPromptCatalog{
		Version: version, CatalogHash: hash, Candidates: candidates,
	})
	if err != nil {
		return "", "", err
	}
	return string(encoded), hash, nil
}

func (c *SelectorEvidenceCatalog) orderedPromptCandidates(
	pinned []trustedTargetCandidate,
	version string,
) []SelectorEvidenceCandidate {
	result := make([]SelectorEvidenceCandidate, 0, len(pinned)+len(c.validationCandidates))
	candidates, validationCandidates := c.selectorCandidatesForVersion(version)
	seen := map[string]bool{}
	add := func(candidate SelectorEvidenceCandidate) {
		key := candidate.State + "\x00" + candidate.Selector
		if seen[key] {
			return
		}
		seen[key] = true
		result = append(result, candidate)
	}
	for _, pin := range pinned {
		add(pin.candidate)
	}
	for _, candidate := range candidates {
		add(candidate)
	}
	for _, candidate := range validationCandidates {
		add(candidate)
	}
	return result
}

func (c *SelectorEvidenceCatalog) selectorCandidatesForVersion(
	version string,
) ([]SelectorEvidenceCandidate, []SelectorEvidenceCandidate) {
	if version == selectorPromptCatalogVersionV1 || version == selectorPromptCatalogVersionV2 {
		return c.legacyCandidates, c.legacyValidation
	}
	if version == selectorPromptCatalogVersionV3 {
		return c.v3Candidates, c.v3Validation
	}
	return c.Candidates, c.validationCandidates
}

func selectorPromptTargetKey(candidate SelectorEvidenceCandidate) string {
	return candidate.State + "\x00" + candidate.Selector
}

func (c *SelectorEvidenceCatalog) buildProviderTargetEvidence(
	candidate SelectorEvidenceCandidate,
	assigner *opaqueCandidateIDAssigner,
	pinnedFieldPaths [][]string,
	includeOptionalFields bool,
	version string,
) (*providerTargetEvidence, error) {
	nodeSets, err := c.candidateNodeSets(candidate)
	if err != nil {
		return nil, err
	}
	cardinalities := append([]int(nil), candidate.Cardinalities...)
	if version == selectorPromptCatalogVersion && minIntSlice(cardinalities) >= 2 {
		nodeSets = explicitlyRenderedRowNodeSets(nodeSets)
		cardinalities = make([]int, len(nodeSets))
		for index, nodes := range nodeSets {
			cardinalities[index] = len(nodes)
		}
		if minIntSlice(cardinalities) < 2 {
			return nil, fmt.Errorf("repeated candidate has fewer than two explicitly rendered rows")
		}
	}
	selector := candidate.Selector
	if version == selectorPromptCatalogVersionV4 || version == selectorPromptCatalogVersion {
		if replacement := exactSemanticMainTarget(nodeSets); replacement != "" {
			selector = replacement
		}
	}
	canonical := candidate.State + "\x00" +
		fmt.Sprint(candidate.SnapshotSequences) + "\x00" +
		fmt.Sprint(cardinalities) + "\x00" +
		selectorEvidenceNodeSetsSignature(nodeSets)
	target := &providerTargetEvidence{
		selector: selector,
		state:    candidate.State,
		nodeSets: nodeSets,
		prompt: SelectorPromptTargetCandidate{
			ObservedSelector:  selector,
			State:             candidate.State,
			SnapshotSequences: append([]int(nil), candidate.SnapshotSequences...),
			Cardinalities:     cardinalities,
		},
	}
	if minIntSlice(cardinalities) >= 2 {
		target.prompt.RowCandidateID = assigner.assign("row", canonical)
	} else if minIntSlice(cardinalities) == 1 && maxIntSlice(cardinalities) == 1 {
		target.prompt.TargetCandidateID = assigner.assign("target", canonical)
	} else {
		return nil, fmt.Errorf("unsupported candidate cardinality")
	}
	targetID := target.prompt.RowCandidateID
	if targetID == "" {
		targetID = target.prompt.TargetCandidateID
	}
	target.fields, err = buildPinnedProviderFieldEvidence(targetID, nodeSets, pinnedFieldPaths, assigner)
	if err != nil {
		return nil, err
	}
	if includeOptionalFields {
		target.fields = appendOptionalProviderFieldEvidence(targetID, nodeSets, target.fields, assigner)
	}
	target.prompt.FieldCandidates = make([]SelectorPromptFieldCandidate, len(target.fields))
	for index, field := range target.fields {
		target.prompt.FieldCandidates[index] = field.prompt
	}
	return target, nil
}

func explicitlyRenderedRowNodeSets(nodeSets [][]*html.Node) [][]*html.Node {
	filtered := make([][]*html.Node, len(nodeSets))
	for index, nodes := range nodeSets {
		for _, node := range nodes {
			if !nodeOrAncestorIsExplicitlyNonRendered(node) {
				filtered[index] = append(filtered[index], node)
			}
		}
	}
	return filtered
}

func exactSemanticMainTarget(nodeSets [][]*html.Node) string {
	matcher, err := cascadia.Compile("main")
	if err != nil || len(nodeSets) == 0 {
		return ""
	}
	for _, nodes := range nodeSets {
		if len(nodes) != 1 || !strings.EqualFold(nodes[0].Data, "main") {
			return ""
		}
		root := nodes[0]
		for root.Parent != nil {
			root = root.Parent
		}
		if !sameNodeSet(cascadia.QueryAll(root, matcher), nodes) {
			return ""
		}
	}
	return "main"
}

func buildPinnedProviderFieldEvidence(
	targetID string,
	rootNodeSets [][]*html.Node,
	paths [][]string,
	assigner *opaqueCandidateIDAssigner,
) ([]*providerFieldEvidence, error) {
	fields := []*providerFieldEvidence{}
	byID := map[string]*providerFieldEvidence{}
	ordered := append([][]string(nil), paths...)
	sort.Slice(ordered, func(i, j int) bool {
		return strings.Join(ordered[i], "\x00") < strings.Join(ordered[j], "\x00")
	})
	for _, path := range ordered {
		parentID := ""
		parentNodeSets := rootNodeSets
		for depth, rawSelector := range path {
			selector := strings.TrimSpace(rawSelector)
			nodes := parentNodeSets
			if selector != "" {
				matcher, err := compileBrowserSelector(selector)
				if err != nil || isPositionalSelector(selector) {
					return nil, fmt.Errorf("trusted field path depth %d selector %q is not stable browser CSS", depth, selector)
				}
				var covered bool
				nodes, covered = relativeSelectorNodeSets(matcher, parentNodeSets)
				if !covered {
					return nil, fmt.Errorf("trusted field path depth %d selector %q lacks exact row coverage", depth, selector)
				}
				if replacement := exactStableRelativeReplacement(parentNodeSets, matcher); replacement != "" {
					selector = replacement
				} else if isVolatileSelector(selector) {
					return nil, fmt.Errorf("trusted field path depth %d selector %q is volatile", depth, selector)
				}
			}
			field := providerFieldEvidenceValue(targetID, parentID, selector, nodes, assigner)
			if existing := byID[field.prompt.FieldCandidateID]; existing != nil {
				if stableSelectorLess(field.selector, existing.selector) {
					existing.selector = field.selector
					existing.prompt.ObservedRelativeSelector = field.selector
				}
				field = existing
			} else {
				fields = append(fields, field)
				byID[field.prompt.FieldCandidateID] = field
			}
			parentID = field.prompt.FieldCandidateID
			parentNodeSets = field.nodeSets
		}
	}
	return fields, nil
}

func appendOptionalProviderFieldEvidence(
	targetID string,
	rootNodeSets [][]*html.Node,
	fields []*providerFieldEvidence,
	assigner *opaqueCandidateIDAssigner,
) []*providerFieldEvidence {
	type fieldParent struct {
		id       string
		nodeSets [][]*html.Node
		depth    int
	}
	queue := []fieldParent{{nodeSets: rootNodeSets, depth: 0}}
	byID := map[string]*providerFieldEvidence{}
	for _, field := range fields {
		byID[field.prompt.FieldCandidateID] = field
	}
	fieldLimit := maxPromptFieldsPerTarget
	if assigner.version == selectorPromptCatalogVersionV3 ||
		assigner.version == selectorPromptCatalogVersionV4 ||
		assigner.version == selectorPromptCatalogVersion {
		fieldLimit = maxPromptV3FieldsPerTarget
	}
	optionalAdded := 0
	for len(queue) > 0 && optionalAdded < fieldLimit {
		parent := queue[0]
		queue = queue[1:]
		selectors := []string{""}
		relativeSelectors := stableRelativeCandidates(parent.nodeSets)
		if assigner.version == selectorPromptCatalogVersionV4 ||
			assigner.version == selectorPromptCatalogVersion {
			relativeSelectors = append(
				semanticV4RelativeSelectors(parent.nodeSets),
				relativeSelectors...,
			)
			relativeSelectors = prioritizeSemanticRelativeSelectors(relativeSelectors)
		}
		selectors = append(selectors, relativeSelectors...)
		if len(selectors) > maxRelativeSelectors+1 {
			selectors = selectors[:maxRelativeSelectors+1]
		}
		for _, selector := range selectors {
			if optionalAdded == fieldLimit {
				break
			}
			nodes := parent.nodeSets
			if selector != "" {
				matcher, err := compileBrowserSelector(selector)
				if err != nil {
					continue
				}
				var covered bool
				nodes, covered = relativeSelectorNodeSets(matcher, parent.nodeSets)
				if !covered {
					continue
				}
			}
			field := providerFieldEvidenceValue(targetID, parent.id, selector, nodes, assigner)
			if existing := byID[field.prompt.FieldCandidateID]; existing != nil {
				field = existing
			} else {
				fields = append(fields, field)
				byID[field.prompt.FieldCandidateID] = field
				optionalAdded++
			}
			if selector != "" && parent.depth+1 < maxPromptFieldDepth {
				queue = append(queue, fieldParent{
					id: field.prompt.FieldCandidateID, nodeSets: field.nodeSets, depth: parent.depth + 1,
				})
			}
		}
	}
	return fields
}

func prioritizeSemanticRelativeSelectors(selectors []string) []string {
	priority := func(selector string) int {
		normalized := strings.ToLower(strings.TrimSpace(selector))
		switch {
		case normalized == "h1":
			return 0
		case strings.HasPrefix(normalized, "h1") && strings.HasSuffix(normalized, "p"):
			return 1
		case strings.HasPrefix(normalized, "h2#"):
			return 2
		default:
			return 3
		}
	}
	seen := map[string]bool{}
	ordered := make([]string, 0, len(selectors))
	for _, selector := range selectors {
		if !seen[selector] {
			seen[selector] = true
			ordered = append(ordered, selector)
		}
	}
	sort.SliceStable(ordered, func(i, j int) bool {
		return priority(ordered[i]) < priority(ordered[j])
	})
	return ordered
}

func semanticV4RelativeSelectors(nodeSets [][]*html.Node) []string {
	candidates := []string{
		"h1", "h1 + p", "h1 ~ p",
		"h1 + * p", "h1 ~ * p",
	}
	if len(nodeSets) > 0 && len(nodeSets[0]) == 1 {
		if selector := firstParagraphAfterHeadingSelector(nodeSets[0][0]); selector != "" {
			candidates = append(candidates, selector)
		}
		walkElements(nodeSets[0][0], func(node *html.Node) {
			if !strings.EqualFold(node.Data, "h2") {
				return
			}
			id := attrValue(node, "id")
			if regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*$`).MatchString(id) {
				candidates = append(candidates, "h2#"+id)
			}
		})
	}
	result := make([]string, 0, len(candidates))
	for _, selector := range candidates {
		matcher, err := compileBrowserSelector(selector)
		if err != nil {
			continue
		}
		if _, covered := relativeSelectorNodeSets(matcher, nodeSets); covered {
			result = append(result, selector)
		}
	}
	return result
}

func firstParagraphAfterHeadingSelector(root *html.Node) string {
	var heading *html.Node
	var paragraph *html.Node
	walkElements(root, func(node *html.Node) {
		if heading == nil && strings.EqualFold(node.Data, "h1") {
			heading = node
			return
		}
		if heading != nil && paragraph == nil && strings.EqualFold(node.Data, "p") {
			paragraph = node
		}
	})
	if heading == nil || paragraph == nil {
		return ""
	}
	path := []string{}
	for current := paragraph; current != nil && current != root; current = current.Parent {
		selector := stableUniqueChildSelector(current)
		if selector == "" && strings.EqualFold(current.Data, "p") {
			selector = uniqueParagraphWithFollowingSiblingSelector(current)
		}
		if selector == "" {
			return ""
		}
		path = append(path, selector)
	}
	if len(path) == 0 {
		return ""
	}
	for left, right := 0, len(path)-1; left < right; left, right = left+1, right-1 {
		path[left], path[right] = path[right], path[left]
	}
	return strings.Join(path, " > ")
}

func uniqueParagraphWithFollowingSiblingSelector(node *html.Node) string {
	if node == nil || node.Parent == nil || !strings.EqualFold(node.Data, "p") {
		return ""
	}
	const selector = "p:not(p + p)"
	matcher, err := compileBrowserSelector(selector)
	if err != nil {
		return ""
	}
	matches := matchingElementChildren(node.Parent, matcher)
	if len(matches) == 1 && matches[0] == node {
		return selector
	}
	return ""
}

func providerFieldEvidenceValue(
	targetID, parentID, selector string,
	nodeSets [][]*html.Node,
	assigner *opaqueCandidateIDAssigner,
) *providerFieldEvidence {
	canonical := targetID + "\x00" + parentID + "\x00" + selectorEvidenceNodeSetsSignature(nodeSets)
	id := assigner.assign("field", canonical)
	nonEmpty := nodeSetsHaveNonEmptyText(nodeSets)
	return &providerFieldEvidence{
		selector: selector,
		targetID: targetID,
		nodeSets: nodeSets,
		prompt: SelectorPromptFieldCandidate{
			FieldCandidateID:         id,
			ParentFieldCandidateID:   parentID,
			ObservedRelativeSelector: selector,
			SupportedTypes:           supportedProviderFieldTypes(nodeSets),
			NonEmptyText:             nonEmpty,
		},
	}
}

func supportedProviderFieldTypes(nodeSets [][]*html.Node) []string {
	if nodeSetsContainDirectSensitiveEvidence(nodeSets) ||
		nodeSetsContainExplicitlyNonRenderedEvidence(nodeSets) {
		return []string{}
	}
	// CSS fields read live computed styles. Sanitized semantic recordings do
	// not contain those values, so the provider catalog cannot ground them.
	result := []string{"attr", "boolean", "exists"}
	subtreeRendered := !nodeSetsContainExplicitlyNonRenderedSubtree(nodeSets)
	contentFaithful := subtreeRendered && !nodeSetsContainSanitizedContent(nodeSets)
	if contentFaithful {
		result = append(result, "count")
	}
	if subtreeRendered && !nodeSetsContainSensitiveSubtree(nodeSets) &&
		!nodeSetsContainSanitizedMarkup(nodeSets) {
		result = append(result, "html")
	}
	if contentFaithful && !nodeSetsContainSensitiveSubtree(nodeSets) &&
		nodeSetsHaveNonEmptyText(nodeSets) {
		result = append(result, "regex", "text")
		if nodeSetsHaveRuntimeNumbers(nodeSets) {
			result = append(result, "number")
		}
	}
	if contentFaithful && !nodeSetsContainSensitiveSubtree(nodeSets) &&
		nodeSetsHaveValidJSONText(nodeSets) {
		result = append(result, "json")
	}
	sort.Strings(result)
	return result
}

func (c *SelectorEvidenceCatalog) candidateNodeSets(candidate SelectorEvidenceCandidate) ([][]*html.Node, error) {
	matcher, err := compileBrowserSelector(candidate.Selector)
	if err != nil {
		return nil, err
	}
	sequences := map[int]bool{}
	for _, sequence := range candidate.SnapshotSequences {
		sequences[sequence] = true
	}
	nodeSets := make([][]*html.Node, 0, len(sequences))
	for _, snapshot := range c.snapshots {
		if snapshot.state == candidate.State && sequences[snapshot.sequence] {
			nodeSets = append(nodeSets, matcher.MatchAll(snapshot.root))
		}
	}
	if len(nodeSets) != len(candidate.SnapshotSequences) {
		return nil, fmt.Errorf("selector candidate snapshots are incomplete")
	}
	return nodeSets, nil
}

func selectorEvidenceNodeSetsSignature(nodeSets [][]*html.Node) string {
	var signature strings.Builder
	for _, nodes := range nodeSets {
		for _, node := range nodes {
			signature.WriteString(selectorEvidenceNodePath(node))
			signature.WriteByte(',')
		}
		signature.WriteByte(';')
	}
	return signature.String()
}

func maxIntSlice(values []int) int {
	maximum := 0
	for _, value := range values {
		if value > maximum {
			maximum = value
		}
	}
	return maximum
}

func copySelectorCandidateRule(value *models.Rule) (*models.Rule, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var result models.Rule
	if err := json.Unmarshal(encoded, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

func (c *SelectorEvidenceCatalog) trustedTargetCandidates(
	rule *models.Rule,
	validationCandidates []SelectorEvidenceCandidate,
	version string,
) ([]trustedTargetCandidate, error) {
	if rule == nil {
		return nil, nil
	}
	selectors, steps, hooks, err := selectorCandidateRuleParts(rule)
	if err != nil {
		return nil, err
	}
	result := []trustedTargetCandidate{}
	indexByTarget := map[string]int{}
	pathSeenByTarget := map[string]map[string]bool{}
	visit := func(step map[string]any, path string) error {
		candidate, targetErr := c.trustedActionTargetCandidate(
			step,
			path,
			selectors,
			validationCandidates,
			version,
		)
		if targetErr != nil {
			return targetErr
		}
		if candidate.Selector == "" {
			return nil
		}
		fieldPaths, fieldErr := trustedExtractionFieldPaths(step, path)
		if fieldErr != nil {
			return fieldErr
		}
		key := selectorPromptTargetKey(candidate)
		index, exists := indexByTarget[key]
		if !exists {
			index = len(result)
			indexByTarget[key] = index
			result = append(result, trustedTargetCandidate{candidate: candidate})
			pathSeenByTarget[key] = map[string]bool{}
		}
		for _, fieldPath := range fieldPaths {
			pathKey, _ := json.Marshal(fieldPath)
			if pathSeenByTarget[key][string(pathKey)] {
				continue
			}
			pathSeenByTarget[key][string(pathKey)] = true
			result[index].fieldPaths = append(result[index].fieldPaths, fieldPath)
		}
		return nil
	}
	err = walkSelectorCandidateActions(steps, "steps", visit)
	if err != nil {
		return nil, err
	}
	for _, name := range selectorCandidateHookNames {
		values, ok := hooks[name]
		if !ok {
			continue
		}
		if err := walkSelectorCandidateActions(values, "hooks."+name, visit); err != nil {
			return nil, err
		}
	}
	return result, nil
}

func trustedExtractionFieldPaths(step map[string]any, path string) ([][]string, error) {
	if strings.TrimSpace(stringValue(step["action"])) != "extract" {
		return nil, nil
	}
	fields, ok := step["fields"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%w: %s.fields is required for action extract", ErrInvalidProvisionalRule, path)
	}
	result := [][]string{}
	var collect func(map[string]any, []string, string) error
	collect = func(values map[string]any, parent []string, fieldPath string) error {
		for _, name := range sortedMapKeys(values) {
			field, ok := values[name].(map[string]any)
			if !ok {
				return fmt.Errorf("%w: %s.%s must be an extraction field object", ErrInvalidProvisionalRule, fieldPath, name)
			}
			currentPath := fieldPath + "." + name
			if err := rejectTargetBearingCondition(field, currentPath+".condition"); err != nil {
				return err
			}
			if strings.TrimSpace(stringValue(field["type"])) == "attr" {
				attribute := strings.TrimSpace(stringValue(field["attr"]))
				if sensitiveExtractionAttribute(attribute, nil) {
					return fmt.Errorf(
						"%w: %s.attr %q may expose browser credentials or form values",
						ErrInvalidProvisionalRule,
						currentPath,
						attribute,
					)
				}
			}
			chain := append(append([]string(nil), parent...), strings.TrimSpace(stringValue(field["selector"])))
			result = append(result, chain)
			if rawNested, exists := field["fields"]; exists {
				nested, ok := rawNested.(map[string]any)
				if !ok {
					return fmt.Errorf("%w: %s.fields must be an extraction field map", ErrInvalidProvisionalRule, currentPath)
				}
				if err := collect(nested, chain, currentPath+".fields"); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := collect(fields, nil, path+".fields"); err != nil {
		return nil, err
	}
	return result, nil
}

func (c *SelectorEvidenceCatalog) trustedActionTargetCandidate(
	step map[string]any,
	path string,
	aliases map[string]any,
	validationCandidates []SelectorEvidenceCandidate,
	version string,
) (SelectorEvidenceCandidate, error) {
	action := strings.TrimSpace(stringValue(step["action"]))
	if !extractionWorkflowActions[action] {
		return SelectorEvidenceCandidate{}, nil
	}
	if err := rejectTargetBearingCondition(step, path+".condition"); err != nil {
		return SelectorEvidenceCandidate{}, err
	}
	if action == "extractPageInfo" {
		if _, hasTarget := step["target"]; hasTarget {
			return SelectorEvidenceCandidate{}, fmt.Errorf(
				"%w: %s.target is not supported by extractPageInfo",
				ErrInvalidProvisionalRule,
				path,
			)
		}
		return SelectorEvidenceCandidate{}, nil
	}
	multiple, err := extractionActionUsesRepeatedTarget(step, action, path)
	if err != nil {
		return SelectorEvidenceCandidate{}, err
	}
	target, ok := step["target"].(map[string]any)
	if !ok {
		return SelectorEvidenceCandidate{}, fmt.Errorf("%w: %s.target is required", ErrInvalidProvisionalRule, path)
	}
	selector, err := trustedInlineSelector(target, aliases)
	if err != nil {
		return SelectorEvidenceCandidate{}, fmt.Errorf("%w: %s.target %v", ErrInvalidProvisionalRule, path, err)
	}
	candidate, found := c.exactCandidateForSelector(selector, multiple, validationCandidates)
	if !found {
		return SelectorEvidenceCandidate{}, fmt.Errorf(
			"%w: %s.target selector %q has no exact opaque selector candidate",
			ErrInvalidProvisionalRule, path, selector,
		)
	}
	nodeSets, err := c.candidateNodeSets(candidate)
	if err != nil {
		return SelectorEvidenceCandidate{}, fmt.Errorf("%w: %s.target evidence is incomplete", ErrInvalidProvisionalRule, path)
	}
	if version == selectorPromptCatalogVersion && multiple {
		nodeSets = explicitlyRenderedRowNodeSets(nodeSets)
		for _, nodes := range nodeSets {
			if len(nodes) < 2 {
				return SelectorEvidenceCandidate{}, fmt.Errorf(
					"%w: %s.target has fewer than two explicitly rendered rows",
					ErrInvalidProvisionalRule,
					path,
				)
			}
		}
	}
	if err := validateStandaloneExtractionCapability(step, action, nodeSets, path); err != nil {
		return SelectorEvidenceCandidate{}, err
	}
	return candidate, nil
}

func trustedInlineSelector(inline map[string]any, aliases map[string]any) (string, error) {
	resolved, err := trustedExtractionTargetValues(inline, aliases)
	if err != nil {
		return "", err
	}
	selector := strings.TrimSpace(stringValue(resolved["selector"]))
	if selector == "" {
		return "", fmt.Errorf("requires a CSS selector")
	}
	return selector, nil
}

func trustedExtractionTargetValues(inline map[string]any, aliases map[string]any) (map[string]any, error) {
	resolved := map[string]any{}
	if ref := strings.TrimSpace(stringValue(inline["$ref"])); ref != "" {
		alias, ok := aliases[ref].(map[string]any)
		if !ok {
			return nil, fmt.Errorf("references unknown selector %q", ref)
		}
		if err := validateTrustedExtractionTargetKeys(alias, false); err != nil {
			return nil, fmt.Errorf("selector alias %q %v", ref, err)
		}
		for key, value := range alias {
			resolved[key] = value
		}
	}
	if err := validateTrustedExtractionTargetKeys(inline, true); err != nil {
		return nil, err
	}
	for key, value := range inline {
		resolved[key] = value
	}
	selector := strings.TrimSpace(stringValue(resolved["selector"]))
	if selector == "" {
		return nil, fmt.Errorf("requires a CSS selector")
	}
	return resolved, nil
}

func validateTrustedExtractionTargetKeys(target map[string]any, allowRef bool) error {
	for _, key := range sortedMapKeys(target) {
		switch key {
		case "selector", "timeout", "visible":
			continue
		case "$ref":
			if allowRef {
				continue
			}
		}
		return fmt.Errorf("uses unsupported locator or disambiguator %q", key)
	}
	return nil
}

func extractionActionUsesRepeatedTarget(step map[string]any, action, path string) (bool, error) {
	_, declaresMultiple := step["multiple"]
	if action != "extract" && declaresMultiple {
		return false, fmt.Errorf(
			"%w: %s.multiple is supported only by action extract",
			ErrInvalidProvisionalRule,
			path,
		)
	}
	return action == "extract" && step["multiple"] == true, nil
}

func rejectTargetBearingCondition(owner map[string]any, path string) error {
	condition, ok := owner["condition"].(map[string]any)
	if !ok {
		return nil
	}
	if _, exists := condition["target"]; exists {
		return fmt.Errorf(
			"%w: %s.target is not supported by the opaque selector contract",
			ErrInvalidProvisionalRule,
			path,
		)
	}
	return nil
}

func (c *SelectorEvidenceCatalog) exactCandidateForSelector(
	selector string,
	multiple bool,
	validationCandidates []SelectorEvidenceCandidate,
) (SelectorEvidenceCandidate, bool) {
	matcher, err := compileBrowserSelector(selector)
	if err != nil {
		return SelectorEvidenceCandidate{}, false
	}
	minimum := 1
	if multiple {
		minimum = 2
	}
	replacement, _, _ := exactStableTargetEvidence(
		validationCandidates,
		c.snapshots,
		matcher,
		minimum,
	)
	if replacement != "" {
		for _, candidate := range validationCandidates {
			if candidate.Selector != replacement {
				continue
			}
			repeated := minIntSlice(candidate.Cardinalities) >= 2
			if repeated == multiple {
				return candidate, true
			}
		}
	}
	// A trusted recording-derived baseline may contain a stable singleton that
	// was not selected by the general discovery budget. Pin that exact observed
	// singleton into this prompt scope. Repeated cohorts never use this fallback:
	// they still require one of the server-derived stable cohort candidates.
	if !multiple && !isVolatileSelector(selector) {
		snapshots, nodeSets, evidenceErr := strongestSelectorEvidence(c.snapshots, matcher)
		if evidenceErr == nil && len(snapshots) > 0 {
			candidate := SelectorEvidenceCandidate{Selector: selector, State: snapshots[0].state}
			for index, snapshot := range snapshots {
				if snapshot.state != candidate.State || len(nodeSets[index]) != 1 {
					return SelectorEvidenceCandidate{}, false
				}
				candidate.SnapshotSequences = append(candidate.SnapshotSequences, snapshot.sequence)
				candidate.Cardinalities = append(candidate.Cardinalities, 1)
			}
			candidate.RelativeSelectors = stableRelativeCandidates(nodeSets)
			if len(candidate.RelativeSelectors) > maxRelativeSelectors {
				candidate.RelativeSelectors = candidate.RelativeSelectors[:maxRelativeSelectors]
			}
			return candidate, true
		}
	}
	return SelectorEvidenceCandidate{}, false
}

func selectorCandidateRuleParts(rule *models.Rule) (map[string]any, []any, map[string][]any, error) {
	selectors := map[string]any{}
	if len(rule.Selectors) > 0 {
		if err := json.Unmarshal(rule.Selectors, &selectors); err != nil {
			return nil, nil, nil, fmt.Errorf("%w: selectors must be an object", ErrInvalidProvisionalRule)
		}
	}
	var steps []any
	if err := json.Unmarshal(rule.Steps, &steps); err != nil {
		return nil, nil, nil, fmt.Errorf("%w: steps must be an array", ErrInvalidProvisionalRule)
	}
	hooks := map[string][]any{}
	if len(rule.Hooks) > 0 {
		var decoded map[string]any
		if err := json.Unmarshal(rule.Hooks, &decoded); err != nil {
			return nil, nil, nil, fmt.Errorf("%w: hooks must be an object", ErrInvalidProvisionalRule)
		}
		for _, name := range selectorCandidateHookNames {
			if values, ok := decoded[name].([]any); ok {
				hooks[name] = values
			}
		}
	}
	return selectors, steps, hooks, nil
}

func walkSelectorCandidateActions(
	values []any,
	path string,
	visit func(step map[string]any, path string) error,
) error {
	for index, raw := range values {
		step, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		stepPath := fmt.Sprintf("%s[%d]", path, index)
		if err := visit(step, stepPath); err != nil {
			return err
		}
		for _, branch := range nestedActionListKeys {
			if children, ok := step[branch].([]any); ok {
				if err := walkSelectorCandidateActions(children, stepPath+"."+branch, visit); err != nil {
					return err
				}
			}
		}
		if trigger, ok := step["trigger"].(map[string]any); ok {
			if err := walkSelectorCandidateActions([]any{trigger}, stepPath+".trigger", visit); err != nil {
				return err
			}
		}
		if cases, ok := step["cases"].([]any); ok {
			for caseIndex, rawCase := range cases {
				caseValue, _ := rawCase.(map[string]any)
				caseSteps, _ := caseValue["steps"].([]any)
				if err := walkSelectorCandidateActions(
					caseSteps, fmt.Sprintf("%s.cases[%d].steps", stepPath, caseIndex), visit,
				); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func (c *SelectorEvidenceCatalog) rewriteTrustedExtractionSelectors(rule *models.Rule) error {
	selectors, steps, hooks, err := selectorCandidateRuleParts(rule)
	if err != nil {
		return err
	}
	rewrite := func(step map[string]any, path string) error {
		action := strings.TrimSpace(stringValue(step["action"]))
		if !extractionWorkflowActions[action] {
			return nil
		}
		if err := rejectTargetBearingCondition(step, path+".condition"); err != nil {
			return err
		}
		if action == "extractPageInfo" {
			if _, hasTarget := step["target"]; hasTarget {
				return fmt.Errorf(
					"%w: %s.target is not supported by extractPageInfo",
					ErrInvalidProvisionalRule,
					path,
				)
			}
			return nil
		}
		multiple, err := extractionActionUsesRepeatedTarget(step, action, path)
		if err != nil {
			return err
		}
		target, ok := step["target"].(map[string]any)
		if !ok {
			return fmt.Errorf("%w: %s.target is required", ErrInvalidProvisionalRule, path)
		}
		resolvedTarget, selectorErr := trustedExtractionTargetValues(target, selectors)
		if selectorErr != nil {
			return fmt.Errorf("%w: %s.target %v", ErrInvalidProvisionalRule, path, selectorErr)
		}
		selector := strings.TrimSpace(stringValue(resolvedTarget["selector"]))
		evidence := c.promptTargetForSelector(selector, multiple)
		if evidence == nil {
			return fmt.Errorf("%w: %s.target selector %q was not pinned into the provider catalog", ErrInvalidProvisionalRule, path, selector)
		}
		for _, modifier := range []string{"timeout", "visible"} {
			if value, exists := resolvedTarget[modifier]; exists {
				target[modifier] = value
			}
		}
		delete(target, "$ref")
		delete(target, "selector")
		targetID := evidence.prompt.TargetCandidateID
		if multiple {
			targetID = evidence.prompt.RowCandidateID
			target["rowCandidateId"] = targetID
		} else {
			target["targetCandidateId"] = targetID
		}
		if fields, ok := step["fields"].(map[string]any); ok {
			if err := c.rewriteTrustedFields(fields, evidence, targetID, "", path+".fields"); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walkSelectorCandidateActions(steps, "steps", rewrite); err != nil {
		return err
	}
	for _, name := range selectorCandidateHookNames {
		values, ok := hooks[name]
		if !ok {
			continue
		}
		if err := walkSelectorCandidateActions(values, "hooks."+name, rewrite); err != nil {
			return err
		}
	}
	encodedSteps, err := json.Marshal(steps)
	if err != nil {
		return err
	}
	rule.Steps = encodedSteps
	if len(hooks) > 0 {
		var decoded map[string]any
		if err := json.Unmarshal(rule.Hooks, &decoded); err != nil {
			return err
		}
		for _, name := range selectorCandidateHookNames {
			if values, ok := hooks[name]; ok {
				decoded[name] = values
			}
		}
		encodedHooks, err := json.Marshal(decoded)
		if err != nil {
			return err
		}
		rule.Hooks = encodedHooks
	}
	return nil
}

func (c *SelectorEvidenceCatalog) promptTargetForSelector(selector string, multiple bool) *providerTargetEvidence {
	matcher, err := compileBrowserSelector(selector)
	if err != nil {
		return nil
	}
	ids := make([]string, 0, len(c.promptTargets))
	for id := range c.promptTargets {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		target := c.promptTargets[id]
		if multiple != (target.prompt.RowCandidateID != "") {
			continue
		}
		if target.selector == selector {
			return target
		}
		candidateMatcher, err := compileBrowserSelector(target.selector)
		if err != nil {
			continue
		}
		exact := true
		for _, snapshot := range c.snapshots {
			if snapshot.state != target.state {
				continue
			}
			if !sameNodeSet(matcher.MatchAll(snapshot.root), candidateMatcher.MatchAll(snapshot.root)) {
				exact = false
				break
			}
		}
		if exact {
			return target
		}
	}
	return nil
}

func (c *SelectorEvidenceCatalog) rewriteTrustedFields(
	fields map[string]any,
	target *providerTargetEvidence,
	targetID, parentID, path string,
) error {
	names := sortedMapKeys(fields)
	for _, name := range names {
		field, ok := fields[name].(map[string]any)
		if !ok {
			return fmt.Errorf("%w: %s.%s must be an extraction field object", ErrInvalidProvisionalRule, path, name)
		}
		fieldPath := path + "." + name
		if err := rejectTargetBearingCondition(field, fieldPath+".condition"); err != nil {
			return err
		}
		selector := strings.TrimSpace(stringValue(field["selector"]))
		evidence := c.promptFieldForSelector(target, targetID, parentID, selector)
		if evidence == nil {
			return fmt.Errorf("%w: %s selector %q was not pinned into the provider catalog", ErrInvalidProvisionalRule, fieldPath, selector)
		}
		nested, nestedErr := validateTypedFieldCapability(field, evidence.nodeSets, fieldPath, true)
		if nestedErr != nil {
			return nestedErr
		}
		canonicalizeTrustedProviderField(field, nested)
		field["fieldCandidateId"] = evidence.prompt.FieldCandidateID
		if nested != nil {
			if err := c.rewriteTrustedFields(
				nested, target, targetID, evidence.prompt.FieldCandidateID, fieldPath+".fields",
			); err != nil {
				return err
			}
		}
	}
	return nil
}

func sortedMapKeys(values map[string]any) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func (c *SelectorEvidenceCatalog) promptFieldForSelector(
	target *providerTargetEvidence,
	targetID, parentID, selector string,
) *providerFieldEvidence {
	var matcher cascadia.Selector
	var err error
	if selector != "" {
		matcher, err = compileBrowserSelector(selector)
		if err != nil {
			return nil
		}
	}
	var parentNodes [][]*html.Node
	if parentID == "" {
		parentNodes = target.nodeSets
	} else if parent := c.promptFields[parentID]; parent != nil {
		parentNodes = parent.nodeSets
	} else {
		return nil
	}
	expected := parentNodes
	if selector != "" {
		var covered bool
		expected, covered = relativeSelectorNodeSets(matcher, parentNodes)
		if !covered {
			return nil
		}
	}
	var matches []*providerFieldEvidence
	for _, candidate := range target.fields {
		if candidate.targetID == targetID &&
			candidate.prompt.ParentFieldCandidateID == parentID &&
			sameSelectorEvidenceNodeSets(candidate.nodeSets, expected) {
			matches = append(matches, candidate)
		}
	}
	sort.Slice(matches, func(i, j int) bool {
		return stableSelectorLess(matches[i].selector, matches[j].selector)
	})
	if len(matches) == 0 {
		return nil
	}
	return matches[0]
}

// EncodeProviderOrdinaryTargets rewrites canonical ordinary action and
// element-condition targets into the branch-free provider wire contract. The
// public PageAgent target contract is unchanged. Encoding is transactional so
// an unsupported target never partially rewrites the provider prompt baseline.
func EncodeProviderOrdinaryTargets(rule *models.Rule) error {
	if rule == nil {
		return fmt.Errorf("%w: provider rule is required", ErrInvalidProvisionalRule)
	}
	encoded := cloneSelectorCandidateRule(rule)
	selectors, steps, hooks, err := selectorCandidateRuleParts(encoded)
	if err != nil {
		return err
	}
	encode := func(step map[string]any, path string) error {
		action := strings.TrimSpace(stringValue(step["action"]))
		if !extractionWorkflowActions[action] {
			if err := encodeProviderActionTarget(step, "target", path+".target", selectors); err != nil {
				return err
			}
		}
		if condition, ok := step["condition"].(map[string]any); ok {
			if err := encodeProviderActionTarget(
				condition, "target", path+".condition.target", selectors,
			); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walkSelectorCandidateActions(steps, "steps", encode); err != nil {
		return err
	}
	for _, name := range selectorCandidateHookNames {
		values, ok := hooks[name]
		if !ok {
			continue
		}
		if err := walkSelectorCandidateActions(values, "hooks."+name, encode); err != nil {
			return err
		}
	}
	if err := writeProviderActionParts(encoded, steps, hooks); err != nil {
		return err
	}
	*rule = *encoded
	return nil
}

func encodeProviderActionTarget(
	owner map[string]any,
	key string,
	path string,
	aliases map[string]any,
) error {
	raw, exists := owner[key]
	if !exists {
		return nil
	}
	target, ok := raw.(map[string]any)
	if !ok {
		return fmt.Errorf("%w: %s must be an object", ErrInvalidProvisionalRule, path)
	}
	wire, err := canonicalProviderTargetWire(target, path, aliases)
	if err != nil {
		return err
	}
	owner[key] = wire
	return nil
}

func canonicalProviderTargetWire(
	target map[string]any,
	path string,
	aliases map[string]any,
) (map[string]any, error) {
	family := ""
	value := ""
	name := ""
	switch {
	case exactMapKeys(target, "$ref"):
		family = providerTargetFamilyRef
		value = exactNonBlankProviderTargetString(target["$ref"])
		if value != "" {
			if _, exists := aliases[value]; !exists {
				return nil, fmt.Errorf(
					"%w: %s references unknown selector %q",
					ErrInvalidProvisionalRule,
					path,
					value,
				)
			}
		}
	case exactMapKeys(target, "selector"):
		family = providerTargetFamilySelector
		value = exactNonBlankProviderTargetString(target["selector"])
	case exactMapKeys(target, "selector", "visible") && target["visible"] == true:
		family = providerTargetFamilySelectorVisible
		value = exactNonBlankProviderTargetString(target["selector"])
	case exactMapKeys(target, "selector", "visible") && target["visible"] == false:
		family = providerTargetFamilySelectorAny
		value = exactNonBlankProviderTargetString(target["selector"])
	case exactMapKeys(target, "text"):
		family = providerTargetFamilyText
		value = exactNonBlankProviderTargetString(target["text"])
	case exactMapKeys(target, "text", "visible") && target["visible"] == true:
		family = providerTargetFamilyTextVisible
		value = exactNonBlankProviderTargetString(target["text"])
	case exactMapKeys(target, "ariaLabel"):
		family = providerTargetFamilyAriaLabel
		value = exactNonBlankProviderTargetString(target["ariaLabel"])
	case exactMapKeys(target, "role", "roleName"):
		family = providerTargetFamilyRole
		value = exactNonBlankProviderTargetString(target["role"])
		name = exactNonBlankProviderTargetString(target["roleName"])
	default:
		return nil, fmt.Errorf(
			"%w: %s is not one exact provider ordinary-target family",
			ErrInvalidProvisionalRule,
			path,
		)
	}
	if value == "" || family == providerTargetFamilyRole && name == "" {
		return nil, fmt.Errorf(
			"%w: %s contains a blank provider ordinary-target value",
			ErrInvalidProvisionalRule,
			path,
		)
	}
	return map[string]any{"family": family, "value": value, "name": name}, nil
}

func exactMapKeys(value map[string]any, expected ...string) bool {
	if len(value) != len(expected) {
		return false
	}
	for _, key := range expected {
		if _, exists := value[key]; !exists {
			return false
		}
	}
	return true
}

func exactNonBlankProviderTargetString(value any) string {
	text, ok := value.(string)
	if !ok || strings.TrimSpace(text) == "" {
		return ""
	}
	return text
}

// ResolveProviderCandidates transactionally lowers branch-free ordinary target
// wire objects and resolves authenticated extraction candidate IDs. It is the
// current manager-facing provider-IR boundary and requires every authored
// ordinary target to use the provider wire even when structured output is off.
func (c *SelectorEvidenceCatalog) ResolveProviderCandidates(
	rule *models.Rule,
	claimedCatalogHash string,
) (SelectorCandidateResolutionReport, error) {
	return c.resolveProviderCandidates(rule, claimedCatalogHash, true)
}

// ResolveHistoricalProviderCandidates reconstructs an already authenticated
// historical provider artifact. Older prompt versions authored canonical
// ordinary targets, so only lineage-bound reconstruction may retain them.
func (c *SelectorEvidenceCatalog) ResolveHistoricalProviderCandidates(
	rule *models.Rule,
	claimedCatalogHash string,
) (SelectorCandidateResolutionReport, error) {
	return c.resolveProviderCandidates(rule, claimedCatalogHash, false)
}

func (c *SelectorEvidenceCatalog) resolveProviderCandidates(
	rule *models.Rule,
	claimedCatalogHash string,
	requireOrdinaryWire bool,
) (SelectorCandidateResolutionReport, error) {
	report := SelectorCandidateResolutionReport{}
	if rule == nil {
		return report, fmt.Errorf(
			"%w: provider selector catalog was not prepared",
			ErrInvalidProvisionalRule,
		)
	}
	resolved := cloneSelectorCandidateRule(rule)
	ordinary, err := resolveProviderOrdinaryTargets(resolved, requireOrdinaryWire)
	report.OrdinaryTargets = ordinary
	if err != nil {
		return report, err
	}
	extraction, err := c.resolveProviderExtractionCandidates(resolved, claimedCatalogHash)
	report.Targets = extraction.Targets
	report.Fields = extraction.Fields
	if err != nil {
		return report, err
	}
	*rule = *resolved
	return report, nil
}

func resolveProviderOrdinaryTargets(rule *models.Rule, requireWire bool) (int, error) {
	selectors, steps, hooks, err := selectorCandidateRuleParts(rule)
	if err != nil {
		return 0, err
	}
	resolved := 0
	lower := func(step map[string]any, path string) error {
		action := strings.TrimSpace(stringValue(step["action"]))
		if !extractionWorkflowActions[action] {
			changed, targetErr := lowerProviderActionTarget(
				step, "target", path+".target", selectors, requireWire,
			)
			if targetErr != nil {
				return targetErr
			}
			if changed {
				resolved++
			}
		}
		if condition, ok := step["condition"].(map[string]any); ok {
			changed, targetErr := lowerProviderActionTarget(
				condition, "target", path+".condition.target", selectors, requireWire,
			)
			if targetErr != nil {
				return targetErr
			}
			if changed {
				resolved++
			}
		}
		return nil
	}
	if err := walkSelectorCandidateActions(steps, "steps", lower); err != nil {
		return resolved, err
	}
	for _, name := range selectorCandidateHookNames {
		values, ok := hooks[name]
		if !ok {
			continue
		}
		if err := walkSelectorCandidateActions(values, "hooks."+name, lower); err != nil {
			return resolved, err
		}
	}
	if err := writeProviderActionParts(rule, steps, hooks); err != nil {
		return resolved, err
	}
	return resolved, nil
}

func lowerProviderActionTarget(
	owner map[string]any,
	key string,
	path string,
	aliases map[string]any,
	requireWire bool,
) (bool, error) {
	raw, exists := owner[key]
	if !exists {
		return false, nil
	}
	target, ok := raw.(map[string]any)
	if !ok {
		return false, fmt.Errorf("%w: %s must be an object", ErrInvalidProvisionalRule, path)
	}
	if !providerTargetWirePresent(target) {
		if requireWire {
			return false, fmt.Errorf(
				"%w: %s must use the branch-free provider target wire",
				ErrInvalidProvisionalRule,
				path,
			)
		}
		return false, nil
	}
	canonical, err := lowerProviderTargetWire(target, path, aliases)
	if err != nil {
		return false, err
	}
	owner[key] = canonical
	return true, nil
}

func providerTargetWirePresent(target map[string]any) bool {
	for _, key := range []string{"family", "value", "name"} {
		if _, exists := target[key]; exists {
			return true
		}
	}
	return false
}

func lowerProviderTargetWire(
	target map[string]any,
	path string,
	aliases map[string]any,
) (map[string]any, error) {
	if !exactMapKeys(target, "family", "value", "name") {
		return nil, fmt.Errorf(
			"%w: %s provider target must contain exactly family, name, and value",
			ErrInvalidProvisionalRule,
			path,
		)
	}
	family := exactNonBlankProviderTargetString(target["family"])
	value := exactNonBlankProviderTargetString(target["value"])
	name, nameIsString := target["name"].(string)
	if family == "" || value == "" || !nameIsString {
		return nil, fmt.Errorf(
			"%w: %s provider target contains a blank or non-string slot",
			ErrInvalidProvisionalRule,
			path,
		)
	}
	switch family {
	case providerTargetFamilyRef:
		if name != "" {
			break
		}
		if _, exists := aliases[value]; !exists {
			return nil, fmt.Errorf(
				"%w: %s references unknown selector %q",
				ErrInvalidProvisionalRule,
				path,
				value,
			)
		}
		return map[string]any{"$ref": value}, nil
	case providerTargetFamilySelector:
		if name == "" {
			return map[string]any{"selector": value}, nil
		}
	case providerTargetFamilySelectorVisible:
		if name == "" {
			return map[string]any{"selector": value, "visible": true}, nil
		}
	case providerTargetFamilySelectorAny:
		if name == "" {
			return map[string]any{"selector": value, "visible": false}, nil
		}
	case providerTargetFamilyText:
		if name == "" {
			return map[string]any{"text": value}, nil
		}
	case providerTargetFamilyTextVisible:
		if name == "" {
			return map[string]any{"text": value, "visible": true}, nil
		}
	case providerTargetFamilyAriaLabel:
		if name == "" {
			return map[string]any{"ariaLabel": value}, nil
		}
	case providerTargetFamilyRole:
		if strings.TrimSpace(name) != "" {
			return map[string]any{"role": value, "roleName": name}, nil
		}
	default:
		return nil, fmt.Errorf(
			"%w: %s uses unknown provider target family %q",
			ErrInvalidProvisionalRule,
			path,
			family,
		)
	}
	return nil, fmt.Errorf(
		"%w: %s uses an invalid provider target slot combination",
		ErrInvalidProvisionalRule,
		path,
	)
}

func writeProviderActionParts(
	rule *models.Rule,
	steps []any,
	hooks map[string][]any,
) error {
	encodedSteps, err := json.Marshal(steps)
	if err != nil {
		return err
	}
	rule.Steps = encodedSteps
	if len(hooks) == 0 {
		return nil
	}
	var decoded map[string]any
	if err := json.Unmarshal(rule.Hooks, &decoded); err != nil {
		return err
	}
	for _, name := range selectorCandidateHookNames {
		if values, ok := hooks[name]; ok {
			decoded[name] = values
		}
	}
	encodedHooks, err := json.Marshal(decoded)
	if err != nil {
		return err
	}
	rule.Hooks = encodedHooks
	return nil
}

// ResolveProviderExtractionCandidates validates the provider-only extraction
// contract and replaces opaque IDs with canonical CSS. It remains available
// for historical reconstruction and extraction-focused callers. Manager paths
// use ResolveProviderCandidates so ordinary lowering and extraction resolution
// commit atomically.
func (c *SelectorEvidenceCatalog) ResolveProviderExtractionCandidates(
	rule *models.Rule,
	claimedCatalogHash string,
) (SelectorCandidateResolutionReport, error) {
	if rule == nil {
		return SelectorCandidateResolutionReport{}, fmt.Errorf(
			"%w: provider selector catalog was not prepared",
			ErrInvalidProvisionalRule,
		)
	}
	resolved := cloneSelectorCandidateRule(rule)
	report, err := c.resolveProviderExtractionCandidates(resolved, claimedCatalogHash)
	if err != nil {
		return report, err
	}
	*rule = *resolved
	return report, nil
}

func cloneSelectorCandidateRule(rule *models.Rule) *models.Rule {
	cloned := *rule
	cloned.Domain = append(models.JSON(nil), rule.Domain...)
	cloned.URLPattern = append(models.JSON(nil), rule.URLPattern...)
	cloned.Variables = append(models.JSON(nil), rule.Variables...)
	cloned.Selectors = append(models.JSON(nil), rule.Selectors...)
	cloned.Humanize = append(models.JSON(nil), rule.Humanize...)
	cloned.Steps = append(models.JSON(nil), rule.Steps...)
	cloned.Output = append(models.JSON(nil), rule.Output...)
	cloned.SendPolicy = append(models.JSON(nil), rule.SendPolicy...)
	cloned.Hooks = append(models.JSON(nil), rule.Hooks...)
	cloned.Tags = append(models.JSON(nil), rule.Tags...)
	return &cloned
}

func (c *SelectorEvidenceCatalog) resolveProviderExtractionCandidates(
	rule *models.Rule,
	claimedCatalogHash string,
) (SelectorCandidateResolutionReport, error) {
	report := SelectorCandidateResolutionReport{}
	if c == nil || c.promptCatalogHash == "" {
		return report, fmt.Errorf("%w: provider selector catalog was not prepared", ErrInvalidProvisionalRule)
	}
	if strings.TrimSpace(claimedCatalogHash) == "" || claimedCatalogHash != c.promptCatalogHash {
		return report, fmt.Errorf("%w: provider selector catalog hash is missing or stale", ErrInvalidProvisionalRule)
	}
	_, steps, hooks, err := selectorCandidateRuleParts(rule)
	if err != nil {
		return report, err
	}
	resolve := func(step map[string]any, path string) error {
		action := strings.TrimSpace(stringValue(step["action"]))
		if !extractionWorkflowActions[action] {
			if location := currentActionProviderCandidateIDLocation(step); location != "" {
				return fmt.Errorf(
					"%w: %s.%s uses a selector candidate ID outside an extraction action",
					ErrInvalidProvisionalRule,
					path,
					location,
				)
			}
			return nil
		}
		if err := rejectTargetBearingCondition(step, path+".condition"); err != nil {
			return err
		}
		multiple, err := extractionActionUsesRepeatedTarget(step, action, path)
		if err != nil {
			return err
		}
		if action == "extractPageInfo" {
			if _, hasTarget := step["target"]; hasTarget {
				return fmt.Errorf("%w: %s.target is not supported by extractPageInfo", ErrInvalidProvisionalRule, path)
			}
			return nil
		}
		target, ok := step["target"].(map[string]any)
		if !ok {
			return fmt.Errorf("%w: %s.target is required", ErrInvalidProvisionalRule, path)
		}
		rowID := strings.TrimSpace(stringValue(target["rowCandidateId"]))
		singleID := strings.TrimSpace(stringValue(target["targetCandidateId"]))
		if (rowID == "") == (singleID == "") {
			return fmt.Errorf("%w: %s.target must contain exactly one opaque selector candidate ID", ErrInvalidProvisionalRule, path)
		}
		id := singleID
		if multiple {
			id = rowID
			if rowID == "" || singleID != "" {
				return fmt.Errorf("%w: %s.target repeated extraction requires rowCandidateId", ErrInvalidProvisionalRule, path)
			}
		} else if singleID == "" || rowID != "" {
			return fmt.Errorf("%w: %s.target single extraction requires targetCandidateId", ErrInvalidProvisionalRule, path)
		}
		evidence := c.promptTargets[id]
		if evidence == nil {
			return candidateSelectionError(
				"candidate_unknown", path+".target",
				"%s.target references unknown or out-of-scope selector candidate %q", path, id,
			)
		}
		if multiple && evidence.prompt.RowCandidateID != id {
			return candidateSelectionError(
				"candidate_kind", path+".target",
				"%s.target candidate has single-node cardinality", path,
			)
		}
		if !multiple && evidence.prompt.TargetCandidateID != id {
			return candidateSelectionError(
				"candidate_kind", path+".target",
				"%s.target candidate has repeated cardinality", path,
			)
		}
		// Some JSON-only providers redundantly copy read-only locator hints
		// beside a valid opaque ID. Ignore only these locator-shaped values
		// after the authenticated ID, cardinality, and kind have been proven.
		// The private catalog selector below remains the sole selector source.
		deleteProviderTargetLocatorHints(target)
		for _, key := range sortedMapKeys(target) {
			switch key {
			case "rowCandidateId", "targetCandidateId", "timeout", "visible":
				continue
			}
			return fmt.Errorf(
				"%w: %s.target.%s is forbidden in provider extraction output; use one opaque candidate ID with only visible/timeout modifiers",
				ErrInvalidProvisionalRule,
				path,
				key,
			)
		}
		if err := validateStandaloneExtractionCapability(step, action, evidence.nodeSets, path); err != nil {
			return candidateSelectionError(
				"candidate_capability", path+".target", "%v", err,
			)
		}
		delete(target, "rowCandidateId")
		delete(target, "targetCandidateId")
		target["selector"] = evidence.selector
		if action == "extract" {
			fields, fieldsErr := canonicalizeProviderFieldCollection(step["fields"], path+".fields")
			if fieldsErr != nil {
				return fieldsErr
			}
			step["fields"] = fields
			if err := c.resolveProviderFields(fields, evidence, id, "", path+".fields", &report); err != nil {
				return err
			}
		}
		report.Targets++
		return nil
	}
	if err := walkSelectorCandidateActions(steps, "steps", resolve); err != nil {
		return report, err
	}
	for _, name := range selectorCandidateHookNames {
		values, ok := hooks[name]
		if !ok {
			continue
		}
		if err := walkSelectorCandidateActions(values, "hooks."+name, resolve); err != nil {
			return report, err
		}
	}
	encodedSteps, err := json.Marshal(steps)
	if err != nil {
		return report, err
	}
	rule.Steps = encodedSteps
	if len(hooks) > 0 {
		var decoded map[string]any
		if err := json.Unmarshal(rule.Hooks, &decoded); err != nil {
			return report, err
		}
		for _, name := range selectorCandidateHookNames {
			if values, ok := hooks[name]; ok {
				decoded[name] = values
			}
		}
		encodedHooks, err := json.Marshal(decoded)
		if err != nil {
			return report, err
		}
		rule.Hooks = encodedHooks
	}
	return report, nil
}

func canonicalizeProviderFieldCollection(raw any, path string) (map[string]any, error) {
	if fields, ok := raw.(map[string]any); ok {
		return fields, nil
	}
	entries, ok := raw.([]any)
	if !ok || len(entries) == 0 {
		return nil, fmt.Errorf("%w: %s is required for action extract", ErrInvalidProvisionalRule, path)
	}
	fields := make(map[string]any, len(entries))
	for index, rawEntry := range entries {
		entry, ok := rawEntry.(map[string]any)
		if !ok {
			return nil, fmt.Errorf(
				"%w: %s[%d] must be a named extraction field object",
				ErrInvalidProvisionalRule,
				path,
				index,
			)
		}
		name, ok := entry["name"].(string)
		if !ok || name == "" || strings.TrimSpace(name) != name {
			return nil, fmt.Errorf(
				"%w: %s[%d].name must be a non-empty exact field name",
				ErrInvalidProvisionalRule,
				path,
				index,
			)
		}
		if _, duplicate := fields[name]; duplicate {
			return nil, fmt.Errorf(
				"%w: %s contains duplicate field name %q",
				ErrInvalidProvisionalRule,
				path,
				name,
			)
		}
		delete(entry, "name")
		fields[name] = entry
	}
	return fields, nil
}

func (c *SelectorEvidenceCatalog) resolveProviderFields(
	fields map[string]any,
	target *providerTargetEvidence,
	targetID, parentID, path string,
	report *SelectorCandidateResolutionReport,
) error {
	for _, name := range sortedMapKeys(fields) {
		field, ok := fields[name].(map[string]any)
		if !ok {
			return fmt.Errorf("%w: %s.%s must be an extraction field object", ErrInvalidProvisionalRule, path, name)
		}
		fieldPath := path + "." + name
		if err := rejectTargetBearingCondition(field, fieldPath+".condition"); err != nil {
			return err
		}
		id := strings.TrimSpace(stringValue(field["fieldCandidateId"]))
		if id == "" {
			return fmt.Errorf("%w: %s.fieldCandidateId is required", ErrInvalidProvisionalRule, fieldPath)
		}
		evidence := c.promptFields[id]
		if evidence == nil || evidence.targetID != targetID {
			return candidateSelectionError(
				"candidate_scope", fieldPath,
				"%s references an unknown or cross-target field candidate", fieldPath,
			)
		}
		if evidence.prompt.ParentFieldCandidateID != parentID {
			return candidateSelectionError(
				"candidate_parent", fieldPath,
				"%s field candidate is not a descendant of its selected parent", fieldPath,
			)
		}
		// As with targets, discard only redundant locator hints after the
		// authenticated field ID and its exact target/parent scope are proven.
		deleteProviderFieldLocatorHints(field)
		nested, shapeErr := validateProviderFieldShape(field, fieldPath)
		if shapeErr != nil {
			return shapeErr
		}
		capabilityNested, capabilityErr := validateTypedFieldCapability(field, evidence.nodeSets, fieldPath, true)
		if capabilityErr != nil {
			return candidateSelectionError(
				"candidate_capability", fieldPath, "%v", capabilityErr,
			)
		}
		if (nested == nil) != (capabilityNested == nil) {
			return fmt.Errorf("%w: %s has an inconsistent provider field shape", ErrInvalidProvisionalRule, fieldPath)
		}
		delete(field, "fieldCandidateId")
		if evidence.selector == "" {
			delete(field, "selector")
		} else {
			field["selector"] = evidence.selector
		}
		// Provider extraction fields cannot opt out of rendered live-DOM
		// enforcement. Public and legacy DSL keep the historical behavior when
		// this additive property is absent.
		field["visible"] = true
		report.Fields++
		if nested != nil {
			if err := c.resolveProviderFields(
				nested, target, targetID, id, fieldPath+".fields", report,
			); err != nil {
				return err
			}
		}
	}
	return nil
}

func deleteProviderTargetLocatorHints(target map[string]any) {
	for _, key := range []string{"selector", "stableSelector", "observedSelector"} {
		delete(target, key)
	}
}

func deleteProviderFieldLocatorHints(field map[string]any) {
	for _, key := range []string{
		"selector",
		"stableSelector",
		"observedSelector",
		"observedRelativeSelector",
	} {
		delete(field, key)
	}
}

func currentActionProviderCandidateIDLocation(step map[string]any) string {
	if target, ok := step["target"].(map[string]any); ok {
		if key := providerCandidateIDKey(target); key != "" {
			return "target." + key
		}
	}
	if condition, ok := step["condition"].(map[string]any); ok {
		if target, ok := condition["target"].(map[string]any); ok {
			if key := providerCandidateIDKey(target); key != "" {
				return "condition.target." + key
			}
		}
	}
	return ""
}

func providerCandidateIDKey(target map[string]any) string {
	for _, key := range []string{"fieldCandidateId", "rowCandidateId", "targetCandidateId"} {
		if _, exists := target[key]; exists {
			return key
		}
	}
	return ""
}

func providerLeafAllowedKeys(fieldType string) map[string]bool {
	allowed := map[string]bool{
		"type":             true,
		"fieldCandidateId": true,
	}
	switch fieldType {
	case "text":
		allowed["trim"] = true
		allowed["regex"] = true
	case "number":
		allowed["regex"] = true
	case "attr":
		allowed["attr"] = true
		allowed["regex"] = true
		allowed["resolve"] = true
	case "json":
		allowed["path"] = true
	case "regex":
		allowed["regex"] = true
		allowed["trim"] = true
	}
	return allowed
}

func validateProviderFieldShape(field map[string]any, path string) (map[string]any, error) {
	for _, modifier := range []string{"condition", "required", "transform"} {
		if _, exists := field[modifier]; exists {
			return nil, fmt.Errorf(
				"%w: %s.%s is not supported by the extract-field runtime and must be omitted",
				ErrInvalidProvisionalRule,
				path,
				modifier,
			)
		}
	}
	fieldType := strings.TrimSpace(stringValue(field["type"]))
	rawNested, structural := field["fields"]
	if structural {
		nested, ok := rawNested.(map[string]any)
		if !ok || len(nested) == 0 {
			return nil, fmt.Errorf(
				"%w: %s.fields must be a non-empty extraction field map",
				ErrInvalidProvisionalRule,
				path,
			)
		}
		if fieldType != "exists" {
			return nil, fmt.Errorf(
				"%w: %s structural provider field must use type %q",
				ErrInvalidProvisionalRule,
				path,
				"exists",
			)
		}
		allowed := map[string]bool{
			"type": true, "fieldCandidateId": true, "fields": true,
		}
		for _, key := range sortedMapKeys(field) {
			if !allowed[key] {
				return nil, fmt.Errorf(
					"%w: %s.%s is forbidden on a structural provider field",
					ErrInvalidProvisionalRule,
					path,
					key,
				)
			}
		}
		return nested, nil
	}

	allowed := providerLeafAllowedKeys(fieldType)
	for _, key := range sortedMapKeys(field) {
		if !allowed[key] {
			return nil, fmt.Errorf(
				"%w: %s.%s is forbidden or ignored for provider field type %q",
				ErrInvalidProvisionalRule,
				path,
				key,
				fieldType,
			)
		}
	}
	if fieldType == "attr" {
		attribute := strings.ToLower(strings.TrimSpace(stringValue(field["attr"])))
		if _, resolves := field["resolve"]; resolves && attribute != "href" && attribute != "src" {
			return nil, fmt.Errorf(
				"%w: %s.resolve is valid only for href or src attr fields",
				ErrInvalidProvisionalRule,
				path,
			)
		}
	}
	return nil, nil
}

// Provider IR is narrower than public DSL. A trusted baseline is copied before
// this canonicalization, so removing ignored/default modifiers here cannot
// alter an existing or human-authored rule.
func canonicalizeTrustedProviderField(field map[string]any, nested map[string]any) {
	fieldType := strings.TrimSpace(stringValue(field["type"]))
	if nested != nil {
		for key := range field {
			delete(field, key)
		}
		field["type"] = "exists"
		field["fields"] = nested
		return
	}
	allowed := providerLeafAllowedKeys(fieldType)
	retained := map[string]any{"type": fieldType}
	for key := range allowed {
		if key == "type" || key == "fieldCandidateId" {
			continue
		}
		if value, exists := field[key]; exists {
			retained[key] = value
		}
	}
	for key := range field {
		delete(field, key)
	}
	for key, value := range retained {
		field[key] = value
	}
}

func validateTypedFieldCapability(
	field map[string]any,
	nodeSets [][]*html.Node,
	path string,
	providerContract bool,
) (map[string]any, error) {
	if nodeSetsContainExplicitlyNonRenderedEvidence(nodeSets) {
		return nil, fmt.Errorf(
			"%w: %s selects explicitly non-rendered source evidence",
			ErrInvalidProvisionalRule,
			path,
		)
	}
	if nodeSetsContainDirectSensitiveEvidence(nodeSets) {
		return nil, fmt.Errorf(
			"%w: %s selects password, hidden, credential-like, or explicitly redacted source evidence",
			ErrInvalidProvisionalRule,
			path,
		)
	}
	if providerContract {
		for _, modifier := range []string{"condition", "required", "transform"} {
			if _, exists := field[modifier]; exists {
				return nil, fmt.Errorf(
					"%w: %s.%s is not supported by the extract-field runtime and must be omitted",
					ErrInvalidProvisionalRule,
					path,
					modifier,
				)
			}
		}
	}
	if rawNested, exists := field["fields"]; exists {
		nested, ok := rawNested.(map[string]any)
		if !ok || len(nested) == 0 {
			return nil, fmt.Errorf(
				"%w: %s.fields must be a non-empty extraction field map",
				ErrInvalidProvisionalRule,
				path,
			)
		}
		// A nested field is a structural scope at runtime: only its independently
		// validated descendants are emitted, so unrelated sensitive descendants
		// elsewhere under this safe scope do not poison the whole row.
		return nested, nil
	}
	fieldType := strings.TrimSpace(stringValue(field["type"]))
	switch fieldType {
	case "text", "number", "regex", "json", "count":
		if nodeSetsContainExplicitlyNonRenderedSubtree(nodeSets) {
			return nil, fmt.Errorf(
				"%w: %s %s field would consume explicitly non-rendered descendant evidence",
				ErrInvalidProvisionalRule,
				path,
				fieldType,
			)
		}
		if nodeSetsContainSanitizedContent(nodeSets) {
			return nil, fmt.Errorf(
				"%w: %s %s field lacks complete local sanitization provenance",
				ErrInvalidProvisionalRule,
				path,
				fieldType,
			)
		}
	case "html":
		if nodeSetsContainExplicitlyNonRenderedSubtree(nodeSets) {
			return nil, fmt.Errorf(
				"%w: %s html field would consume explicitly non-rendered descendant evidence",
				ErrInvalidProvisionalRule,
				path,
			)
		}
		if nodeSetsContainSanitizedMarkup(nodeSets) {
			return nil, fmt.Errorf(
				"%w: %s html field lacks exact sanitized-markup provenance",
				ErrInvalidProvisionalRule,
				path,
			)
		}
	}
	supported := containsString(supportedProviderFieldTypes(nodeSets), fieldType)
	// Existing human-authored/public DSL can continue to use computed CSS and
	// best-effort number fields. They are excluded only from provider output,
	// where the recording cannot establish an exact successful runtime value.
	if !providerContract {
		if fieldType == "css" {
			supported = true
		}
		if fieldType == "number" && !nodeSetsContainSensitiveSubtree(nodeSets) &&
			nodeSetsHaveNonEmptyText(nodeSets) {
			supported = true
		}
	}
	if !supported {
		return nil, fmt.Errorf(
			"%w: %s field evidence is incompatible with type %q, contains sensitive content, or does not contain non-empty recorded text",
			ErrInvalidProvisionalRule,
			path,
			fieldType,
		)
	}
	_, hasRegexModifier := field["regex"]
	if providerContract && hasRegexModifier && fieldType != "regex" &&
		fieldType != "text" && fieldType != "number" && fieldType != "attr" {
		return nil, fmt.Errorf(
			"%w: %s.regex is unsupported for field type %q",
			ErrInvalidProvisionalRule,
			path,
			fieldType,
		)
	}
	switch fieldType {
	case "attr":
		attribute := strings.TrimSpace(stringValue(field["attr"]))
		if attribute == "" || sensitiveExtractionAttribute(attribute, nodeSets) ||
			!nodeSetsAllHaveAttribute(nodeSets, attribute) ||
			nodeSetsHaveSanitizedAttribute(nodeSets, attribute) {
			return nil, fmt.Errorf(
				"%w: %s attr field evidence cannot safely read exact recorded attribute %q",
				ErrInvalidProvisionalRule,
				path,
				attribute,
			)
		}
		if providerContract && hasRegexModifier {
			values := []string{}
			for _, nodes := range nodeSets {
				for _, node := range nodes {
					values = append(values, attrValue(node, attribute))
				}
			}
			if _, err := validateRecordedRegexValues(field, values, path, false); err != nil {
				return nil, err
			}
		}
	case "json":
		if err := validateRecordedJSONPath(field, nodeSets, path); err != nil {
			return nil, err
		}
	case "regex":
		if err := validateRecordedFieldRegex(field, nodeSets, path); err != nil {
			return nil, err
		}
	case "text", "number":
		values := []string{}
		for _, nodes := range nodeSets {
			for _, node := range nodes {
				values = append(values, selectorEvidenceRawText(node))
			}
		}
		if providerContract && hasRegexModifier {
			trim := fieldType == "text" && field["trim"] != false
			matches, err := validateRecordedRegexValues(field, values, path, trim)
			if err != nil {
				return nil, err
			}
			values = matches
		}
		if providerContract && fieldType == "number" {
			for _, value := range values {
				if _, ok := runtimeNumber(value); !ok {
					return nil, fmt.Errorf(
						"%w: %s does not produce a finite number for every recorded source node",
						ErrInvalidProvisionalRule,
						path,
					)
				}
			}
		}
	}
	return nil, nil
}

func validateFreeFormExtractionCapability(
	step map[string]any,
	action string,
	nodeSets [][]*html.Node,
	path string,
) error {
	if nodeSetsContainExplicitlyNonRenderedEvidence(nodeSets) {
		return fmt.Errorf(
			"%w: %s.target selects explicitly non-rendered source evidence",
			ErrInvalidProvisionalRule,
			path,
		)
	}
	if action == "extractText" {
		if nodeSetsContainSensitiveSubtree(nodeSets) ||
			nodeSetsContainExplicitlyNonRenderedSubtree(nodeSets) ||
			nodeSetsContainSanitizedContent(nodeSets) {
			return fmt.Errorf(
				"%w: %s.target text source contains sensitive or explicitly non-rendered descendant evidence, or lacks complete sanitization provenance",
				ErrInvalidProvisionalRule,
				path,
			)
		}
		// Human-corrected and legacy free-form rules historically permit an
		// empty extractText result. Provider candidate generation remains
		// stricter and requires non-empty recorded evidence.
		return nil
	}
	return validateStandaloneExtractionCapability(step, action, nodeSets, path)
}

func containsString(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func nodeSetsAllHaveAttribute(nodeSets [][]*html.Node, name string) bool {
	if len(nodeSets) == 0 {
		return false
	}
	for _, nodes := range nodeSets {
		if len(nodes) == 0 {
			return false
		}
		for _, node := range nodes {
			if !hasAttr(node, name) {
				return false
			}
		}
	}
	return true
}

func validateStandaloneExtractionCapability(
	step map[string]any,
	action string,
	nodeSets [][]*html.Node,
	path string,
) error {
	if nodeSetsContainExplicitlyNonRenderedEvidence(nodeSets) {
		return fmt.Errorf(
			"%w: %s.target selects explicitly non-rendered source evidence",
			ErrInvalidProvisionalRule,
			path,
		)
	}
	switch action {
	case "extract":
		return nil
	case "extractHtml":
		if !nodeSetsContainSensitiveSubtree(nodeSets) &&
			!nodeSetsContainExplicitlyNonRenderedSubtree(nodeSets) &&
			!nodeSetsContainSanitizedMarkup(nodeSets) {
			return nil
		}
		return fmt.Errorf(
			"%w: %s.target HTML contains sensitive/non-rendered descendants or altered sanitized markup",
			ErrInvalidProvisionalRule,
			path,
		)
	case "extractText":
		if !nodeSetsContainSensitiveSubtree(nodeSets) &&
			!nodeSetsContainExplicitlyNonRenderedSubtree(nodeSets) &&
			!nodeSetsContainSanitizedContent(nodeSets) &&
			nodeSetsHaveNonEmptyText(nodeSets) {
			return nil
		}
		return fmt.Errorf(
			"%w: %s.target lacks safe non-empty recorded text because it contains sensitive or explicitly non-rendered descendant evidence, or lacks complete sanitization provenance",
			ErrInvalidProvisionalRule,
			path,
		)
	case "extractAttribute":
		attribute := strings.TrimSpace(stringValue(step["attr"]))
		if attribute != "" && !sensitiveExtractionAttribute(attribute, nodeSets) &&
			nodeSetsAllHaveAttribute(nodeSets, attribute) &&
			!nodeSetsHaveSanitizedAttribute(nodeSets, attribute) {
			return nil
		}
		if sensitiveExtractionAttribute(attribute, nodeSets) {
			return fmt.Errorf(
				"%w: %s.attr %q may expose browser credentials or redacted form values",
				ErrInvalidProvisionalRule,
				path,
				attribute,
			)
		}
		return fmt.Errorf(
			"%w: %s.target lacks recorded attribute %q",
			ErrInvalidProvisionalRule,
			path,
			attribute,
		)
	case "extractJson":
		if nodeSetsContainSensitiveSubtree(nodeSets) ||
			nodeSetsContainExplicitlyNonRenderedSubtree(nodeSets) ||
			nodeSetsContainSanitizedContent(nodeSets) {
			return fmt.Errorf(
				"%w: %s.target JSON source contains sensitive/non-rendered descendants or lacks complete sanitization provenance",
				ErrInvalidProvisionalRule,
				path,
			)
		}
		if !nodeSetsHaveValidJSONText(nodeSets) {
			return fmt.Errorf(
				"%w: %s.target does not contain valid recorded JSON text",
				ErrInvalidProvisionalRule,
				path,
			)
		}
		return validateRecordedJSONPath(step, nodeSets, path)
	case "extractTable":
		if !nodeSetsContainSensitiveSubtree(nodeSets) &&
			!nodeSetsContainExplicitlyNonRenderedSubtree(nodeSets) &&
			!nodeSetsContainSanitizedContent(nodeSets) &&
			nodeSetsEmitNonEmptyTableRow(step, nodeSets) {
			return nil
		}
		return fmt.Errorf(
			"%w: %s.target cannot emit a safe, visible, provenance-complete non-empty recorded table data row because it contains sensitive or explicitly non-rendered descendants, altered content, or incompatible headers/includeHeader evidence",
			ErrInvalidProvisionalRule,
			path,
		)
	case "extractPageInfo":
		return nil
	default:
		return fmt.Errorf("%w: %s uses unsupported extraction action %q", ErrInvalidProvisionalRule, path, action)
	}
}

func sensitiveExtractionAttribute(name string, nodeSets [][]*html.Node) bool {
	if sensitiveExtractionAttributeName(name) {
		return true
	}
	for _, nodes := range nodeSets {
		for _, node := range nodes {
			if nodeHasDirectSensitiveEvidence(node) {
				return true
			}
			value := strings.ToLower(strings.TrimSpace(attrValue(node, name)))
			if containsExplicitRedaction(value) {
				return true
			}
		}
	}
	return false
}

func sensitiveExtractionAttributeName(name string) bool {
	normalized := strings.ToLower(strings.TrimSpace(name))
	compact := strings.NewReplacer("-", "", "_", "", ":", "").Replace(normalized)
	if normalized == "value" {
		return true
	}
	for _, marker := range []string{
		"password", "passwd", "pwd", "secret", "token", "apikey",
		"authorization", "cookie", "session", "credential",
	} {
		if strings.Contains(compact, marker) {
			return true
		}
	}
	return false
}

func nodeHasDirectSensitiveEvidence(node *html.Node) bool {
	if node == nil {
		return false
	}
	if strings.EqualFold(node.Data, "input") {
		inputType := strings.ToLower(strings.TrimSpace(attrValue(node, "type")))
		if inputType == "password" || inputType == "hidden" {
			return true
		}
	}
	for _, attribute := range node.Attr {
		attributeName := strings.ToLower(strings.TrimSpace(attribute.Key))
		if sensitiveExtractionAttributeName(attributeName) &&
			(attributeName != "value" || strings.EqualFold(node.Data, "input")) {
			return true
		}
		value := strings.ToLower(strings.TrimSpace(attribute.Val))
		if containsExplicitRedaction(value) {
			return true
		}
		switch attributeName {
		case "name", "id", "aria-label", "autocomplete":
			if sensitiveExtractionIdentity(value) {
				return true
			}
		}
	}
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		if child.Type == html.TextNode && containsExplicitRedaction(strings.ToLower(strings.TrimSpace(child.Data))) {
			return true
		}
	}
	return false
}

func sensitiveExtractionIdentity(value string) bool {
	compact := strings.NewReplacer("-", "", "_", "", ":", "", " ", "").Replace(value)
	for _, marker := range []string{
		"password", "passwd", "pwd", "secret", "token", "apikey",
		"authorization", "cookie", "session", "credential",
	} {
		if strings.Contains(compact, marker) {
			return true
		}
	}
	return false
}

func containsExplicitRedaction(value string) bool {
	normalized := strings.ToLower(value)
	for _, marker := range []string{"[redacted]", "<redacted>", "[removed_url]"} {
		if strings.Contains(normalized, marker) {
			return true
		}
	}
	return false
}

func nodeIsExplicitlyNonRendered(node *html.Node) bool {
	if node == nil || node.Type != html.ElementNode {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(node.Data)) {
	case "script", "style", "noscript", "template", "link", "meta":
		return true
	}
	if strings.EqualFold(strings.TrimSpace(attrValue(node, recordedRenderedAttribute)), "false") ||
		hasAttr(node, "hidden") ||
		strings.EqualFold(strings.TrimSpace(attrValue(node, "aria-hidden")), "true") {
		return true
	}
	return strings.EqualFold(node.Data, "input") &&
		strings.EqualFold(strings.TrimSpace(attrValue(node, "type")), "hidden")
}

func nodeOrAncestorIsExplicitlyNonRendered(node *html.Node) bool {
	for current := node; current != nil; current = current.Parent {
		if nodeIsExplicitlyNonRendered(current) {
			return true
		}
	}
	return false
}

func nodeSubtreeContainsExplicitlyNonRendered(node *html.Node) bool {
	if node == nil {
		return false
	}
	if nodeIsExplicitlyNonRendered(node) ||
		strings.EqualFold(
			strings.TrimSpace(attrValue(node, recordedNonRenderedSubtreeAttribute)),
			"true",
		) {
		return true
	}
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		if nodeSubtreeContainsExplicitlyNonRendered(child) {
			return true
		}
	}
	return false
}

func nodeSetsContainExplicitlyNonRenderedEvidence(nodeSets [][]*html.Node) bool {
	for _, nodes := range nodeSets {
		for _, node := range nodes {
			if nodeOrAncestorIsExplicitlyNonRendered(node) {
				return true
			}
		}
	}
	return false
}

func nodeSetsContainExplicitlyNonRenderedSubtree(nodeSets [][]*html.Node) bool {
	for _, nodes := range nodeSets {
		for _, node := range nodes {
			if nodeSubtreeContainsExplicitlyNonRendered(node) {
				return true
			}
		}
	}
	return false
}

func nodeOrAncestorHasUnknownSanitization(node *html.Node) bool {
	for current := node; current != nil; current = current.Parent {
		if strings.EqualFold(
			strings.TrimSpace(attrValue(current, recordedSanitizationUnknownAttribute)),
			"true",
		) {
			return true
		}
	}
	return false
}

func nodeSetsHaveUnknownSanitization(nodeSets [][]*html.Node) bool {
	for _, nodes := range nodeSets {
		for _, node := range nodes {
			if nodeOrAncestorHasUnknownSanitization(node) {
				return true
			}
		}
	}
	return false
}

func nodeSubtreeHasRecordedMarker(node *html.Node, attribute string) bool {
	if node == nil {
		return false
	}
	if strings.EqualFold(strings.TrimSpace(attrValue(node, attribute)), "true") {
		return true
	}
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		if nodeSubtreeHasRecordedMarker(child, attribute) {
			return true
		}
	}
	return false
}

func nodeSetsContainSanitizedContent(nodeSets [][]*html.Node) bool {
	if nodeSetsHaveUnknownSanitization(nodeSets) {
		return true
	}
	for _, nodes := range nodeSets {
		for _, node := range nodes {
			if nodeSubtreeHasRecordedMarker(node, recordedSanitizedContentAttribute) {
				return true
			}
		}
	}
	return false
}

func nodeSetsContainSanitizedMarkup(nodeSets [][]*html.Node) bool {
	if nodeSetsHaveUnknownSanitization(nodeSets) {
		return true
	}
	for _, nodes := range nodeSets {
		for _, node := range nodes {
			if nodeSubtreeHasRecordedMarker(node, recordedSanitizedMarkupAttribute) {
				return true
			}
		}
	}
	return false
}

func nodeSetsHaveSanitizedAttribute(nodeSets [][]*html.Node, name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "class" && nodeSetsHaveUnknownSanitization(nodeSets) {
		return true
	}
	for _, nodes := range nodeSets {
		for _, node := range nodes {
			for _, altered := range strings.Fields(
				strings.ToLower(attrValue(node, recordedSanitizedAttrsAttribute)),
			) {
				if altered == "*" || altered == name {
					return true
				}
			}
		}
	}
	return false
}

func nodeHasSensitiveSubtree(node *html.Node) bool {
	if nodeHasDirectSensitiveEvidence(node) {
		return true
	}
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		if nodeHasSensitiveSubtree(child) {
			return true
		}
	}
	return false
}

func nodeSetsContainDirectSensitiveEvidence(nodeSets [][]*html.Node) bool {
	for _, nodes := range nodeSets {
		for _, node := range nodes {
			if nodeHasDirectSensitiveEvidence(node) {
				return true
			}
		}
	}
	return false
}

func nodeSetsContainSensitiveSubtree(nodeSets [][]*html.Node) bool {
	for _, nodes := range nodeSets {
		for _, node := range nodes {
			if nodeHasSensitiveSubtree(node) {
				return true
			}
		}
	}
	return false
}

func validateRecordedJSONPath(step map[string]any, nodeSets [][]*html.Node, path string) error {
	rawPath, exists := step["path"]
	if !exists || strings.TrimSpace(stringValue(rawPath)) == "" {
		return nil
	}
	jsonPath, ok := rawPath.(string)
	if !ok {
		return fmt.Errorf("%w: %s.path must be a static string", ErrInvalidProvisionalRule, path)
	}
	jsonPath = strings.TrimSpace(jsonPath)
	if strings.Contains(jsonPath, "{{") || strings.Contains(jsonPath, "}}") ||
		strings.Contains(jsonPath, "${") {
		return fmt.Errorf("%w: %s.path must not be templated or dynamic", ErrInvalidProvisionalRule, path)
	}
	segments := strings.Split(jsonPath, ".")
	for _, segment := range segments {
		if segment == "" {
			return fmt.Errorf("%w: %s.path contains an empty segment", ErrInvalidProvisionalRule, path)
		}
	}
	for _, nodes := range nodeSets {
		for _, node := range nodes {
			var value any
			if err := json.Unmarshal([]byte(strings.TrimSpace(selectorEvidenceRawText(node))), &value); err != nil {
				return fmt.Errorf("%w: %s.target JSON evidence is invalid", ErrInvalidProvisionalRule, path)
			}
			if !recordedJSONPathExists(value, segments) {
				return fmt.Errorf(
					"%w: %s.path %q does not resolve in every recorded JSON node",
					ErrInvalidProvisionalRule,
					path,
					jsonPath,
				)
			}
		}
	}
	return nil
}

func validateRecordedFieldRegex(field map[string]any, nodeSets [][]*html.Node, path string) error {
	values := []string{}
	for _, nodes := range nodeSets {
		for _, node := range nodes {
			values = append(values, selectorEvidenceRawText(node))
		}
	}
	_, err := validateRecordedRegexValues(field, values, path, field["trim"] != false)
	return err
}

func validateRecordedRegexValues(
	field map[string]any,
	values []string,
	path string,
	trim bool,
) ([]string, error) {
	rawPattern, exists := field["regex"]
	pattern, ok := rawPattern.(string)
	if !exists || !ok || strings.TrimSpace(pattern) == "" {
		return nil, fmt.Errorf("%w: %s.regex must be a non-empty static pattern", ErrInvalidProvisionalRule, path)
	}
	if strings.Contains(pattern, "{{") || strings.Contains(pattern, "}}") ||
		strings.Contains(pattern, "${") {
		return nil, fmt.Errorf("%w: %s.regex must not be templated or dynamic", ErrInvalidProvisionalRule, path)
	}
	if err := validatePortableJavaScriptRegex(pattern); err != nil {
		return nil, fmt.Errorf(
			"%w: %s.regex is outside the portable JavaScript/Go subset: %v",
			ErrInvalidProvisionalRule,
			path,
			err,
		)
	}
	compiled, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("%w: %s.regex is invalid: %v", ErrInvalidProvisionalRule, path, err)
	}
	matches := make([]string, 0, len(values))
	for _, value := range values {
		if trim {
			value = strings.TrimSpace(value)
		}
		match := compiled.FindString(value)
		if match == "" {
			return nil, fmt.Errorf(
				"%w: %s.regex does not produce a non-empty match for every recorded source node",
				ErrInvalidProvisionalRule,
				path,
			)
		}
		matches = append(matches, match)
	}
	return matches, nil
}

func validatePortableJavaScriptRegex(pattern string) error {
	if strings.Contains(pattern, "(?") {
		return fmt.Errorf("inline flags and special group prefixes are not portable")
	}
	if strings.Contains(pattern, "[[:") {
		return fmt.Errorf("POSIX character classes are not portable")
	}
	for index := 0; index < len(pattern); index++ {
		if pattern[index] != '\\' || index+1 >= len(pattern) {
			continue
		}
		next := pattern[index+1]
		if next >= '0' && next <= '9' {
			return fmt.Errorf("numeric escapes and backreferences are not portable")
		}
		switch next {
		case 'A', 'C', 'E', 'P', 'Q', 'p', 'z':
			return fmt.Errorf("\\%c has different or unsupported JavaScript semantics", next)
		case 'x':
			if index+2 < len(pattern) && pattern[index+2] == '{' {
				return fmt.Errorf("\\x{...} escapes are not portable")
			}
		}
		index++
	}
	return nil
}

func recordedJSONPathExists(value any, segments []string) bool {
	current := value
	for _, segment := range segments {
		switch typed := current.(type) {
		case map[string]any:
			next, exists := typed[segment]
			if !exists {
				return false
			}
			current = next
		case []any:
			index, ok := canonicalJSONArrayIndex(segment)
			if !ok || index >= len(typed) {
				return false
			}
			current = typed[index]
		default:
			return false
		}
	}
	return true
}

func canonicalJSONArrayIndex(segment string) (int, bool) {
	const maxJavaScriptSafeInteger = uint64(1<<53 - 1)
	maxNativeInt := uint64(^uint(0) >> 1)
	if segment == "0" {
		return 0, true
	}
	if len(segment) == 0 || segment[0] < '1' || segment[0] > '9' {
		return 0, false
	}
	for index := 1; index < len(segment); index++ {
		if segment[index] < '0' || segment[index] > '9' {
			return 0, false
		}
	}
	value, err := strconv.ParseUint(segment, 10, 64)
	if err != nil || value > maxJavaScriptSafeInteger || value > maxNativeInt {
		return 0, false
	}
	return int(value), true
}

func nodeSetsHaveValidJSONText(nodeSets [][]*html.Node) bool {
	if len(nodeSets) == 0 {
		return false
	}
	for _, nodes := range nodeSets {
		if len(nodes) == 0 {
			return false
		}
		for _, node := range nodes {
			text := strings.TrimSpace(selectorEvidenceRawText(node))
			if text == "" || !json.Valid([]byte(text)) {
				return false
			}
		}
	}
	return true
}

var runtimeNumberEvidencePattern = regexp.MustCompile(
	`[+-]?[0-9][0-9,\t\n\f\r \x{000b}\x{00a0}\x{202f}]*(\.[0-9]+)?`,
)

func nodeSetsHaveRuntimeNumbers(nodeSets [][]*html.Node) bool {
	if len(nodeSets) == 0 {
		return false
	}
	for _, nodes := range nodeSets {
		if len(nodes) == 0 {
			return false
		}
		for _, node := range nodes {
			if _, ok := runtimeNumber(selectorEvidenceRawText(node)); !ok {
				return false
			}
		}
	}
	return true
}

// runtimeNumber mirrors the ScriptCat toNumber path for the conservative
// whitespace subset accepted into provider evidence. Values outside that
// subset are rejected rather than risking a server/replay disagreement.
func runtimeNumber(value string) (float64, bool) {
	match := runtimeNumberEvidencePattern.FindString(value)
	if match == "" {
		return 0, false
	}
	normalized := strings.Map(func(char rune) rune {
		if char == ',' || unicode.IsSpace(char) {
			return -1
		}
		return char
	}, match)
	parsed, err := strconv.ParseFloat(normalized, 64)
	if err != nil || math.IsInf(parsed, 0) || math.IsNaN(parsed) {
		return 0, false
	}
	return parsed, true
}

func selectorEvidenceRawText(node *html.Node) string {
	if node == nil {
		return ""
	}
	var text strings.Builder
	var walk func(*html.Node)
	walk = func(current *html.Node) {
		if current.Type == html.TextNode {
			text.WriteString(current.Data)
		}
		for child := current.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(node)
	return text.String()
}

func nodeSetsEmitNonEmptyTableRow(step map[string]any, nodeSets [][]*html.Node) bool {
	if len(nodeSets) == 0 {
		return false
	}
	for _, nodes := range nodeSets {
		if len(nodes) == 0 {
			return false
		}
		for _, node := range nodes {
			if node.Type != html.ElementNode || !strings.EqualFold(node.Data, "table") ||
				!tableNodeEmitsNonEmptyRow(step, node) {
				return false
			}
		}
	}
	return true
}

func tableNodeEmitsNonEmptyRow(step map[string]any, table *html.Node) bool {
	var (
		headers       []string
		headerMapping map[string]any
		rows          []*html.Node
	)
	rawHeaders, declaresHeaders := step["headers"]
	if declaresHeaders && rawHeaders != nil {
		var ok bool
		headerMapping, ok = rawHeaders.(map[string]any)
		if !ok {
			return false
		}
		headers = javascriptRuntimeObjectKeys(headerMapping)
		switch {
		case step["includeHeader"] == true:
			rows = tableDescendantsMatching(table, func(node *html.Node) bool {
				return strings.EqualFold(node.Data, "tr")
			})
		case tableHasDescendantTag(table, "tbody"):
			rows = tableDescendantsMatching(table, func(node *html.Node) bool {
				return strings.EqualFold(node.Data, "tr") && hasAncestorBefore(node, table, "tbody")
			})
		default:
			rows = tableDescendantsMatching(table, func(node *html.Node) bool {
				return strings.EqualFold(node.Data, "tr")
			})
		}
	} else if tableHasDescendantTag(table, "thead") {
		headerNodes := tableDescendantsMatching(table, func(node *html.Node) bool {
			return strings.EqualFold(node.Data, "th") && hasAncestorBefore(node, table, "thead")
		})
		for _, header := range headerNodes {
			headers = append(headers, strings.TrimSpace(selectorEvidenceRawText(header)))
		}
		rows = tableDescendantsMatching(table, func(node *html.Node) bool {
			return strings.EqualFold(node.Data, "tr") && hasAncestorBefore(node, table, "tbody")
		})
	} else {
		allRows := tableDescendantsMatching(table, func(node *html.Node) bool {
			return strings.EqualFold(node.Data, "tr")
		})
		if len(allRows) > 0 {
			for _, cell := range tableRowCells(allRows[0]) {
				headers = append(headers, strings.TrimSpace(selectorEvidenceRawText(cell)))
			}
		}
		if step["includeHeader"] == true {
			rows = allRows
		} else {
			for _, row := range allRows {
				if elementSiblingPosition(row) >= 2 {
					rows = append(rows, row)
				}
			}
		}
	}
	if len(headers) == 0 {
		return false
	}
	for _, row := range rows {
		cells := tableRowCells(row)
		emitted := map[string]string{}
		for index, header := range headers {
			key := header
			if headerMapping != nil {
				if mapped, ok := headerMapping[header].(string); ok {
					key = mapped
				}
			}
			value := ""
			if index < len(cells) {
				value = strings.TrimSpace(selectorEvidenceRawText(cells[index]))
			}
			emitted[key] = value
		}
		for _, value := range emitted {
			if value != "" {
				return true
			}
		}
	}
	return false
}

func tableDescendantsMatching(root *html.Node, matches func(*html.Node) bool) []*html.Node {
	result := []*html.Node{}
	var walk func(*html.Node)
	walk = func(parent *html.Node) {
		for child := parent.FirstChild; child != nil; child = child.NextSibling {
			if child.Type == html.ElementNode && matches(child) {
				result = append(result, child)
			}
			walk(child)
		}
	}
	walk(root)
	return result
}

func tableHasDescendantTag(root *html.Node, tag string) bool {
	return len(tableDescendantsMatching(root, func(node *html.Node) bool {
		return strings.EqualFold(node.Data, tag)
	})) > 0
}

func hasAncestorBefore(node, boundary *html.Node, tag string) bool {
	for current := node.Parent; current != nil && current != boundary; current = current.Parent {
		if current.Type == html.ElementNode && strings.EqualFold(current.Data, tag) {
			return true
		}
	}
	return false
}

func tableRowCells(row *html.Node) []*html.Node {
	return tableDescendantsMatching(row, func(node *html.Node) bool {
		return strings.EqualFold(node.Data, "td") || strings.EqualFold(node.Data, "th")
	})
}

func elementSiblingPosition(node *html.Node) int {
	position := 0
	for sibling := node.Parent.FirstChild; sibling != nil; sibling = sibling.NextSibling {
		if sibling.Type != html.ElementNode {
			continue
		}
		position++
		if sibling == node {
			return position
		}
	}
	return 0
}

func javascriptRuntimeObjectKeys(values map[string]any) []string {
	type arrayIndexKey struct {
		key   string
		index uint64
	}
	indexKeys := []arrayIndexKey{}
	otherKeys := []string{}
	for key := range values {
		index, err := strconv.ParseUint(key, 10, 32)
		if err == nil && index < 1<<32-1 && strconv.FormatUint(index, 10) == key {
			indexKeys = append(indexKeys, arrayIndexKey{key: key, index: index})
		} else {
			otherKeys = append(otherKeys, key)
		}
	}
	sort.Slice(indexKeys, func(i, j int) bool {
		return indexKeys[i].index < indexKeys[j].index
	})
	sort.Strings(otherKeys)
	result := make([]string, 0, len(values))
	for _, value := range indexKeys {
		result = append(result, value.key)
	}
	return append(result, otherKeys...)
}
