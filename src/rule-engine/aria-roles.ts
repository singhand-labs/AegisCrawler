/**
 * Bounded HTML-ARIA tag → implicit role map.
 *
 * Used by:
 * - The recording layer (`extension/src/content.ts`, `PageAgentRecorder.ts`)
 *   to capture an element's *effective* role when no explicit `role`
 *   attribute is present (R-implicit).
 * - The engine's R-verify disambiguation step (`selectors.ts`) to derive the
 *   matched element's effective role for verification, using the SAME map so
 *   that record-time and verify-time stay consistent.
 *
 * Coverage ceiling: ~20 native semantic HTML tags. Interactive `<span>` /
 * `<div>` with partial ARIA, custom elements, and web components remain
 * without a role signal — `ariaLabel` is the effective recovery path for
 * those elements. Full WAI-ARIA coverage is explicitly out of scope (N7).
 *
 * Context-sensitivity: `<header>` → `banner` and `<footer>` → `contentinfo`
 * per WAI-ARIA apply only at top level. This bounded map ships the simpler
 * default. R-verify cannot catch over-derivation here (it uses the same
 * map), but `ariaLabel` fallback bounds the blast radius.
 */

/** Mapping from tag name (lowercase) to implicit ARIA role. */
const TAG_TO_ROLE: Readonly<Record<string, string>> = {
  a: 'link',
  aside: 'complementary',
  button: 'button',
  footer: 'contentinfo',
  form: 'form',
  h1: 'heading',
  h2: 'heading',
  h3: 'heading',
  h4: 'heading',
  h5: 'heading',
  h6: 'heading',
  header: 'banner',
  img: 'image',
  li: 'listitem',
  main: 'main',
  nav: 'navigation',
  ol: 'list',
  select: 'combobox',
  table: 'table',
  textarea: 'textbox',
  ul: 'list',
};

/**
 * Mapping from `input[type=...]` to implicit ARIA role. Keys are lowercase
 * type attribute values. Falls back to `textbox` for unmapped types and for
 * bare `<input>` with no `type` (HTML spec default type is `text`).
 */
const INPUT_TYPE_TO_ROLE: Readonly<Record<string, string>> = {
  button: 'button',
  checkbox: 'checkbox',
  email: 'textbox',
  hidden: '', // no role — not surfaced to accessibility tree
  image: 'button',
  number: 'textbox',
  password: 'textbox',
  radio: 'radio',
  range: 'slider',
  reset: 'button',
  search: 'searchbox',
  submit: 'button',
  tel: 'textbox',
  text: 'textbox',
  url: 'textbox',
};

const INPUT_DEFAULT_ROLE = 'textbox';

/**
 * Returns the implicit WAI-ARIA role for a native semantic HTML tag, or
 * `undefined` if the tag has no mapping (interactive span/div, custom
 * elements, etc.).
 *
 * @param tagName element tag name (case-insensitive — normalized internally)
 * @param inputType for `<input>` only, the `type` attribute value
 *                  (case-insensitive). Ignored for non-input tags.
 */
export function getImplicitRole(tagName: string, inputType?: string): string | undefined {
  const tag = tagName.toLowerCase();

  if (tag === 'input') {
    if (inputType === undefined) return INPUT_DEFAULT_ROLE;
    const type = inputType.toLowerCase();
    if (type === 'hidden') return undefined;
    return INPUT_TYPE_TO_ROLE[type] ?? INPUT_DEFAULT_ROLE;
  }

  return TAG_TO_ROLE[tag];
}

/**
 * Read an element's effective ARIA role, preferring an explicit `role`
 * attribute and falling back to the bounded implicit-role map
 * ({@link getImplicitRole}). Returns `undefined` when neither yields a role
 * (interactive `<span>` / `<div>`, custom elements).
 *
 * Recording sites use this so the captured `role` field reflects the
 * element's *effective* role, not just its explicit attribute. The engine's
 * R-verify step (selectors.ts) derives effective role with the same map, so
 * record-time and verify-time stay consistent (R-implicit / R-verify contract).
 *
 * Note: `Element` is a structural type reference — this function has no
 * runtime DOM dependency and runs safely under jsdom or any DOM host.
 */
export function effectiveRole(el: Element): string | undefined {
  const explicit = el.getAttribute('role');
  if (explicit) return explicit;
  const tag = el.tagName.toLowerCase();
  const inputType = tag === 'input' ? (el as HTMLInputElement).getAttribute('type') ?? undefined : undefined;
  return getImplicitRole(tag, inputType);
}
