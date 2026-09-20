import { sharedAjv as validateAjv, sharedSchemaCache as validateSchemaCache } from '../ajv-instance';
import type { CircuitBreakerAction } from '../../rule-engine/types/action';
import type { Action, RuntimeContext, Environment, ExtractField, Humanize } from '../types';
import { runSteps, calculateDelay, requestHumanIntervention } from '../executor-utils';
import { FlowControlSignal, ExitSignal } from '../signals';
import { interpolate, parseRetry, resolveSelectorAlias, resolveExpression, randomBetween, toNumber } from '../utils';
import { isElementVisible } from '../selectors';

function requireEnvMethod<T extends keyof Environment>(
  env: Environment,
  method: T,
): NonNullable<Environment[T]> {
  const fn = env[method];
  if (typeof fn !== 'function') {
    throw new Error(`UnsupportedInEnvironment: ${String(method)} not implemented by host environment`);
  }
  return fn.bind(env) as NonNullable<Environment[T]>;
}

export async function executeAction(action: Action, ctx: RuntimeContext, env: Environment): Promise<any> {
  const type = action.action;
  switch (type) {
    case 'extract':
      return handleExtract(action as any, ctx, env);
    case 'extractText':
      return handleExtractText(action as any, ctx, env);
    case 'extractAttribute':
      return handleExtractAttribute(action as any, ctx, env);
    case 'extractPageInfo':
      return handleExtractPageInfo(action as any, ctx, env);
    case 'extractHtml':
      return handleExtractHtml(action as any, ctx, env);
    case 'extractJson':
      return handleExtractJson(action as any, ctx, env);
    case 'extractTable':
      return handleExtractTable(action as any, ctx, env);
    case 'merge':
      return handleMerge(action as any, ctx, env);
    case 'click':
      return handleClick(action as any, ctx, env);
    case 'doubleClick':
      return handleDoubleClick(action as any, ctx, env);
    case 'rightClick':
      return handleRightClick(action as any, ctx, env);
    case 'middleClick':
      return handleMiddleClick(action as any, ctx, env);
    case 'hover':
      return handleHover(action as any, ctx, env);
    case 'hoverClick':
      return handleHoverClick(action as any, ctx, env);
    case 'moveMouse':
      return handleMoveMouse(action as any, ctx, env);
    case 'pressAndHold':
      return handlePressAndHold(action as any, ctx, env);
    case 'dragAndDrop':
      return handleDragAndDrop(action as any, ctx, env);
    case 'dragBy':
      return handleDragBy(action as any, ctx, env);
    case 'slide':
      return handleSlide(action as any, ctx, env);
    case 'type':
      return handleType(action as any, ctx, env);
    case 'paste':
      return handlePaste(action as any, ctx, env);
    case 'clear':
      return handleClear(action as any, ctx, env);
    case 'select':
      return handleSelect(action as any, ctx, env);
    case 'check':
      return handleCheck(action as any, ctx, env);
    case 'selectRadio':
      return handleSelectRadio(action as any, ctx, env);
    case 'typeAndSelect':
      return handleTypeAndSelect(action as any, ctx, env);
    case 'uploadFile':
      return handleUploadFile(action as any, ctx, env);
    case 'focus':
      return handleFocus(action as any, ctx, env);
    case 'blur':
      return handleBlur(action as any, ctx, env);
    case 'tabToNext':
    case 'tabToPrevious':
      return handleTab(action as any, ctx, env);
    case 'pressKey':
      return handlePressKey(action as any, ctx, env);
    case 'keyCombination':
      return handleKeyCombination(action as any, ctx, env);
    case 'waitFor':
      return handleWaitFor(action as any, ctx, env);
    case 'waitForText':
      return handleWaitForText(action as any, ctx, env);
    case 'waitForTimeout':
      return handleWaitForTimeout(action as any, ctx, env);
    case 'waitForElementHidden':
      return handleWaitForElementHidden(action as any, ctx, env);
    case 'waitForUrl':
      return handleWaitForUrl(action as any, ctx, env);
    case 'waitForElementVisible':
      return handleWaitForElementVisible(action as any, ctx, env);
    case 'waitForFunction':
      return handleWaitForFunction(action as any, ctx, env);
    case 'readPause':
      return handleReadPause(action as any, ctx, env);
    case 'scrollToBottom':
      return handleScrollToBottom(action as any, ctx, env);
    case 'scrollBy':
      return handleScrollBy(action as any, ctx, env);
    case 'scrollToTop':
      return handleScrollToTop(action as any, ctx, env);
    case 'pageDown':
    case 'pageUp':
      return handlePageDown(action as any, ctx, env);
    case 'setStyle':
      return handleSetStyle(action as any, ctx, env);
    case 'removeElement':
      return handleRemoveElement(action as any, ctx, env);
    case 'scrollIntoView':
      return handleScrollIntoView(action as any, ctx, env);
    case 'scrollTo':
      return handleScrollTo(action as any, ctx, env);
    case 'navigate':
      return handleNavigate(action as any, ctx, env);
    case 'reload':
      return handleReload(action as any, ctx, env);
    case 'goBack':
      await requireEnvMethod(env, 'goBack')();
      return;
    case 'goForward':
      await requireEnvMethod(env, 'goForward')();
      return;
    case 'setViewport':
      await requireEnvMethod(env, 'setViewport')({
        width: (action as any).width,
        height: (action as any).height,
        deviceScaleFactor: (action as any).deviceScaleFactor,
        isMobile: (action as any).isMobile,
        hasTouch: (action as any).hasTouch,
        userAgent: (action as any).userAgent,
      });
      return;
    case 'waitForNetworkIdle':
      await requireEnvMethod(env, 'waitForNetworkIdle')(
        (action as any).idleTime ?? 500,
        (action as any).timeout ?? 30000,
      );
      return;
    case 'evaluate':
      return handleEvaluate(action as any, ctx, env);
    case 'sendResult':
      return handleSendResult(action as any, ctx, env);
    case 'sendLog':
      return handleSendLog(action as any, ctx, env);
    case 'sendScreenshot':
      return handleSendScreenshot(action as any, ctx, env);
    case 'sendHtml':
      return handleSendHtml(action as any, ctx, env);
    case 'emitEvent':
      return handleEmitEvent(action as any, ctx, env);
    case 'updateStatus':
      return handleUpdateStatus(action as any, ctx, env);
    case 'checkpoint':
      return handleCheckpoint(action as any, ctx, env);
    case 'flushResults':
      return handleFlushResults(action as any, ctx, env);
    case 'heartbeat':
      return handleHeartbeat(action as any, ctx, env);
    case 'saveSnapshot':
      return handleSaveSnapshot(action as any, ctx, env);
    case 'abort':
      return handleAbort(action as any, ctx, env);
    case 'transform':
      return handleTransform(action as any, ctx, env);
    case 'filter':
      return handleFilter(action as any, ctx, env);
    case 'deduplicate':
      return handleDeduplicate(action as any, ctx, env);
    case 'validateData':
      return handleValidateData(action as any, ctx, env);
    case 'setTag':
      return handleSetTag(action as any, ctx, env);
    case 'logMetric':
      return handleLogMetric(action as any, ctx, env);
    case 'circuitBreaker':
      return handleCircuitBreaker(action as any, ctx, env);
    case 'checkQuota':
      return handleCheckQuota(action as any, ctx, env);
    case 'requestHuman':
      return handleRequestHuman(action as any, ctx, env);
    case 'solveCaptcha':
      return handleSolveCaptcha(action as any, ctx, env);
    case 'recover':
      return handleRecover(action as any, ctx, env);
    case 'sleep':
      return handleSleep(action as any, ctx, env);
    case 'retry':
      return handleRetry(action as any, ctx, env);
    case 'break':
    case 'continue':
      return handleBreakContinue(action as any, ctx, env);
    case 'exit':
      return handleExit(action as any, ctx, env);
    // Phase 3: flow-control actions (if/loop/switch/group) are dispatched
    // inline by runSteps so options+path propagate. Reaching these branches
    // means a caller invoked executeAction directly on a flow-control step —
    // treat as a programmer error rather than silently dropping checkpoints.
    case 'if':
    case 'loop':
    case 'switch':
    case 'group':
      throw new Error(`ScriptError: flow-control action '${type}' must be dispatched via runSteps, not executeAction`);
    default:
      throw new Error(`Unsupported action: ${type}`);
  }
}

// ==================== 提取 ====================

async function handleExtract(action: any, ctx: RuntimeContext, env: Environment): Promise<any> {
  const target = interpolate(resolveSelectorAlias(action.target, ctx.variables.__selectors), ctx);
  const timeout = action.timeout ?? 5000;
  const elements = action.multiple ? await env.findElements(target, timeout) : [await env.findElement(target, timeout)];
  const foundElements = elements.filter(Boolean);
  if (foundElements.length === 0 && (action.onEmpty === 'fail' || (!action.multiple && target.visible === true))) {
    throw elementNotFound(target);
  }
  const results = [];
  for (const el of foundElements) {
    const item: Record<string, any> = {};
    for (const [key, fieldDef] of Object.entries(action.fields as Record<string, ExtractField>)) {
      item[key] = extractField(el!, fieldDef as ExtractField, ctx);
    }
    results.push(item);
  }
  const value = action.multiple ? results : results[0];
  ctx.extracted[action.name] = value;
  return value;
}

function extractField(el: Element, field: ExtractField, ctx: RuntimeContext): any {
  const target = field.selector ? resolveSelectorAlias({ selector: field.selector }, ctx.variables.__selectors) : null;
  const node = target ? (target.selector ? el.querySelector(target.selector) : el) : el;
  if (field.visible === true && (!node || !isElementVisible(node))) {
    throw elementNotFound({ selector: field.selector ?? ':scope', visible: true });
  }
  if (!node) return field.default ?? null;

  if (field.fields) {
    const nested: Record<string, any> = {};
    for (const [key, nestedField] of Object.entries(field.fields)) {
      nested[key] = extractField(node, nestedField, ctx);
    }
    return nested;
  }

  switch (field.type) {
    case 'text':
      return applyTransform((field.trim !== false ? node.textContent?.trim() : node.textContent) ?? field.default ?? '', field);
    case 'html':
      return node.innerHTML;
    case 'number':
      return toNumber(applyTransform(node.textContent ?? '', field)) ?? field.default ?? null;
    case 'boolean':
      return !!node;
    case 'attr':
      let v = node.getAttribute(field.attr ?? '') ?? field.default ?? null;
      if (field.resolve && v && (field.attr === 'href' || field.attr === 'src')) v = new URL(v, location.href).href;
      return applyTransform(v, field);
    case 'css':
      return window.getComputedStyle(node).getPropertyValue(field.cssProperty ?? '');
    case 'json': {
      const value = parseExtractedJson(node.textContent?.trim() ?? '', 'extract field');
      const resolved = field.path ? getJsonPath(value, field.path) : value;
      return resolved === undefined ? (field.default ?? null) : resolved;
    }
    case 'regex':
      return extractRegexField(
        (field.trim !== false ? node.textContent?.trim() : node.textContent) ?? '',
        field,
      );
    case 'exists':
      return true;
    case 'count':
      return node.childElementCount;
    default:
      return node.textContent?.trim() ?? field.default ?? null;
  }
}

function applyTransform(value: any, field: ExtractField): any {
  if (field.regex && typeof value === 'string') {
    const m = value.match(new RegExp(field.regex));
    value = m ? m[0] : (field.default ?? '');
  }
  return value;
}

function extractRegexField(value: string, field: ExtractField): any {
  if (typeof field.regex !== 'string' || field.regex.trim() === '') {
    throw new Error('ScriptError: regex extract field requires a non-empty pattern');
  }
  let pattern: RegExp;
  try {
    pattern = new RegExp(field.regex);
  } catch {
    throw new Error('ScriptError: regex extract field has an invalid pattern');
  }
  const match = value.match(pattern);
  return match ? match[0] : (field.default ?? '');
}

async function handleExtractText(action: any, ctx: RuntimeContext, env: Environment): Promise<string> {
  const target = interpolate(resolveSelectorAlias(action.target, ctx.variables.__selectors), ctx);
  const el = await env.findElement(target);
  if (!el && target.visible === true) throw elementNotFound(target);
  const value = el?.textContent?.trim() ?? '';
  ctx.extracted[action.name] = value;
  return value;
}

async function handleExtractAttribute(action: any, ctx: RuntimeContext, env: Environment): Promise<string | null> {
  const target = interpolate(resolveSelectorAlias(action.target, ctx.variables.__selectors), ctx);
  const el = await env.findElement(target);
  if (!el && target.visible === true) throw elementNotFound(target);
  const value = el?.getAttribute(action.attr) ?? null;
  ctx.extracted[action.name] = value;
  return value;
}

async function handleExtractPageInfo(action: any, ctx: RuntimeContext, env: Environment): Promise<Record<string, any>> {
  const info: Record<string, any> = {};
  for (const [key, def] of Object.entries(action.fields) as [string, any][]) {
    switch (def.type) {
      case 'url':
        info[key] = env.getUrl();
        break;
      case 'title':
        info[key] = env.getTitle();
        break;
      case 'domain':
        info[key] = location.hostname;
        break;
      case 'timestamp':
        info[key] = Date.now();
        break;
      case 'referrer':
        info[key] = document.referrer;
        break;
      case 'userAgent':
        info[key] = navigator.userAgent;
        break;
    }
  }
  ctx.extracted[action.name] = info;
  return info;
}

async function handleExtractHtml(action: any, ctx: RuntimeContext, env: Environment): Promise<string> {
  const target = interpolate(resolveSelectorAlias(action.target, ctx.variables.__selectors), ctx);
  const el = await env.findElement(target);
  if (!el) throw elementNotFound(target);
  const value = el.outerHTML;
  ctx.extracted[action.name] = value;
  return value;
}

async function handleExtractJson(action: any, ctx: RuntimeContext, env: Environment): Promise<any> {
  const target = interpolate(resolveSelectorAlias(action.target, ctx.variables.__selectors), ctx);
  const el = await env.findElement(target);
  if (!el) throw elementNotFound(target);
  const text = el.textContent?.trim() ?? '{}';
  let value = parseExtractedJson(text, `extractJson ${action.name}`);
  if (action.path) {
    value = getJsonPath(value, interpolate(action.path, ctx));
  }
  ctx.extracted[action.name] = value;
  return value;
}

function parseExtractedJson(text: string, source: string): any {
  try {
    return JSON.parse(text);
  } catch {
    throw new Error(`ScriptError: invalid JSON in ${source}`);
  }
}

function getJsonPath(value: any, path: string): any {
  const normalizedPath = path.trim();
  if (normalizedPath === '') return value;
  const segments = normalizedPath.split('.');
  if (segments.some((segment) => segment.length === 0)) {
    throw new Error(`ScriptError: invalid JSON path "${path}": empty path segment`);
  }

  let current = value;
  for (const segment of segments) {
    if (Array.isArray(current)) {
      if (!/^(?:0|[1-9]\d*)$/.test(segment)) {
        throw new Error(`ScriptError: invalid JSON path "${path}": array index "${segment}" is not canonical`);
      }
      const index = Number(segment);
      if (!Number.isSafeInteger(index)) {
        throw new Error(`ScriptError: invalid JSON path "${path}": array index "${segment}" exceeds the safe integer range`);
      }
      current = current[index];
      continue;
    }
    if (current !== null && typeof current === 'object') {
      current = Object.prototype.hasOwnProperty.call(current, segment) ? current[segment] : undefined;
      continue;
    }
    return undefined;
  }
  return current;
}

async function handleExtractTable(action: any, ctx: RuntimeContext, env: Environment): Promise<any> {
  const target = interpolate(resolveSelectorAlias(action.target, ctx.variables.__selectors), ctx);
  const el = await env.findElement(target, action.timeout ?? 5000) as HTMLTableElement | null;
  if (!el) throw elementNotFound(target);

  let headers: string[];
  let rowSelector: string;
  if (action.headers) {
    headers = Object.keys(action.headers);
    rowSelector = action.includeHeader ? 'tr' : (el.querySelector('tbody') ? 'tbody tr' : 'tr');
  } else if (el.querySelector('thead')) {
    headers = Array.from(el.querySelectorAll('thead th')).map((th) => th.textContent?.trim() ?? '');
    rowSelector = 'tbody tr';
  } else {
    const firstRow = el.querySelector('tr');
    headers = Array.from(firstRow?.querySelectorAll('td, th') ?? []).map((cell) => cell.textContent?.trim() ?? '');
    rowSelector = action.includeHeader ? 'tr' : 'tr:nth-child(n+2)';
  }

  const rows: Record<string, string>[] = [];
  for (const tr of Array.from(el.querySelectorAll(rowSelector))) {
    const cells = Array.from(tr.querySelectorAll('td, th')).map((td) => td.textContent?.trim() ?? '');
    const row: Record<string, string> = {};
    headers.forEach((h: string, i: number) => { row[action.headers?.[h] ?? h] = cells[i] ?? ''; });
    if (Object.values(row).some((v) => v !== '')) rows.push(row);
  }
  const value = { headers, rows };
  ctx.extracted[action.name] = value;
  return value;
}

async function handleMerge(action: any, ctx: RuntimeContext, env: Environment): Promise<any> {
  if (!Array.isArray(action.from)) {
    throw new Error(`ScriptError: merge requires a 'from' array`);
  }
  const strategy = action.strategy ?? 'concat';
  const inputs = (action.from as string[]).map((f) => resolveExpressionSource(interpolate(f, ctx), ctx));
  let value: any;
  if (strategy === 'concat') {
    value = inputs.flat();
  } else if (strategy === 'assign') {
    value = Object.assign({}, ...inputs);
  } else if (strategy === 'zip') {
    const len = Math.min(...inputs.map((i) => (Array.isArray(i) ? i.length : 0)));
    value = [];
    for (let i = 0; i < len; i++) {
      value.push(Object.assign({}, ...inputs.map((inp) => (Array.isArray(inp) ? inp[i] : {}))));
    }
  } else {
    throw new Error(`ScriptError: unsupported merge strategy ${strategy}`);
  }
  ctx.extracted[action.name] = value;
  return value;
}

// ==================== 鼠标与输入 ====================

async function waitForUrlChange(env: Environment, startUrl: string, timeoutMs: number): Promise<boolean> {
  const start = env.now();
  while (env.now() - start < timeoutMs) {
    if (env.getUrl() !== startUrl) return true;
    await env.sleep(100);
  }
  return false;
}

function elementMayNavigate(el: Element): boolean {
  const tag = el.tagName.toLowerCase();
  if (tag === 'a') {
    const href = (el as HTMLAnchorElement).getAttribute('href');
    return href !== null && href !== '#' && !href.startsWith('javascript:');
  }
  if (tag === 'input') {
    const input = el as HTMLInputElement;
    if (input.type !== 'submit' && input.type !== 'image') {
      return false;
    }
    return input.form !== null;
  }
  if (tag === 'button') {
    const button = el as HTMLButtonElement;
    const explicitType = el.getAttribute('type')?.toLowerCase();
    const isSubmitType = explicitType === 'submit' || explicitType === '' || explicitType === null;
    return isSubmitType && button.form !== null;
  }
  return false;
}

async function handleClick(action: any, ctx: RuntimeContext, env: Environment): Promise<void> {
  const target = interpolate(resolveSelectorAlias(action.target, ctx.variables.__selectors), ctx);
  const el = await env.findElement(target, action.timeout ?? 5000);
  if (!el) throw elementNotFound(target);
  if (isDisabledFormControl(el)) throw elementDisabled(target);
  await humanizeBefore(resolveHumanize(action, ctx), env, el);
  const startUrl = env.getUrl();
  dispatchMouseEvent(el, 'mousedown', action.button ?? 'left');
  await env.sleep(50);
  dispatchMouseEvent(el, 'mouseup', action.button ?? 'left');
  dispatchMouseEvent(el, 'click', action.button ?? 'left');
  if (elementMayNavigate(el)) {
    await waitForUrlChange(env, startUrl, action.navigationTimeout ?? 5000);
  }
  await humanizeAfter(resolveHumanize(action, ctx), env);
}

async function handleType(action: any, ctx: RuntimeContext, env: Environment): Promise<void> {
  const target = interpolate(resolveSelectorAlias(action.target, ctx.variables.__selectors), ctx);
  const el = await env.findElement(target, action.timeout ?? 5000) as HTMLInputElement | HTMLTextAreaElement | null;
  if (!el) throw elementNotFound(target);
  const value = String(interpolate(action.value, ctx));
  if (!action.append) el.value = '';
  await humanizeBefore(resolveHumanize(action, ctx), env);
  el.focus();
  const delay = resolveHumanize(action, ctx)?.typingDelay;
  for (const ch of value) {
    el.value += ch;
    el.dispatchEvent(new Event('input', { bubbles: true }));
    el.dispatchEvent(new Event('keyup', { bubbles: true }));
    if (delay) await env.sleep(randomBetween(delay));
  }
  if (action.submit) {
    const startUrl = env.getUrl();
    el.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }));
    await waitForUrlChange(env, startUrl, action.navigationTimeout ?? 5000);
  }
  await humanizeAfter(resolveHumanize(action, ctx), env);
}

async function handleClear(action: any, ctx: RuntimeContext, env: Environment): Promise<void> {
  const target = interpolate(resolveSelectorAlias(action.target, ctx.variables.__selectors), ctx);
  const el = await env.findElement(target, action.timeout ?? 5000) as HTMLInputElement | null;
  if (!el) throw elementNotFound(target);
  el.value = '';
  el.dispatchEvent(new Event('input', { bubbles: true }));
}

async function handleSelect(action: any, ctx: RuntimeContext, env: Environment): Promise<void> {
  const target = interpolate(resolveSelectorAlias(action.target, ctx.variables.__selectors), ctx);
  const el = await env.findElement(target, action.timeout ?? 5000) as HTMLSelectElement | null;
  if (!el) throw elementNotFound(target);
  const value = interpolate(action.value, ctx);
  const by = action.by ?? 'value';
  const option = Array.from(el.options).find((o) => (by === 'value' ? o.value == value : by === 'text' ? o.text == value : o.index == value));
  if (option) {
    el.value = option.value;
    el.dispatchEvent(new Event('change', { bubbles: true }));
  }
}

async function handleCheck(action: any, ctx: RuntimeContext, env: Environment): Promise<void> {
  const target = interpolate(resolveSelectorAlias(action.target, ctx.variables.__selectors), ctx);
  const el = await env.findElement(target, action.timeout ?? 5000) as HTMLInputElement | null;
  if (!el) throw elementNotFound(target);
  const state = interpolate(action.state, ctx);
  const desired = state === 'toggle' ? !el.checked : !!state;
  if (el.checked !== desired) {
    el.click();
  }
}

async function handlePressKey(action: any, ctx: RuntimeContext, env: Environment): Promise<void> {
  const keys = action.keys as string[] | string[][];
  const flat = Array.isArray(keys[0]) ? (keys as string[][]).flat() : (keys as string[]);
  const keyDuration = resolveHumanize(action, ctx)?.keyDuration;
  for (const key of flat) {
    document.dispatchEvent(new KeyboardEvent('keydown', { key, bubbles: true }));
    if (keyDuration) await env.sleep(randomBetween(keyDuration));
    document.dispatchEvent(new KeyboardEvent('keyup', { key, bubbles: true }));
  }
}

async function handleKeyCombination(action: any, ctx: RuntimeContext, env: Environment): Promise<void> {
  const keys = action.keys as string[];
  for (const key of keys) {
    document.dispatchEvent(new KeyboardEvent('keydown', { key, bubbles: true }));
  }
  await env.sleep(50);
  for (const key of [...keys].reverse()) {
    document.dispatchEvent(new KeyboardEvent('keyup', { key, bubbles: true }));
  }
}

async function handleDoubleClick(action: any, ctx: RuntimeContext, env: Environment): Promise<void> {
  const target = interpolate(resolveSelectorAlias(action.target, ctx.variables.__selectors), ctx);
  const el = await env.findElement(target, action.timeout ?? 5000);
  if (!el) throw elementNotFound(target);
  await humanizeBefore(resolveHumanize(action, ctx), env);
  dispatchMouseEvent(el, 'dblclick', action.button ?? 'left');
  await humanizeAfter(resolveHumanize(action, ctx), env);
}

async function handleRightClick(action: any, ctx: RuntimeContext, env: Environment): Promise<void> {
  const target = interpolate(resolveSelectorAlias(action.target, ctx.variables.__selectors), ctx);
  const el = await env.findElement(target, action.timeout ?? 5000);
  if (!el) throw elementNotFound(target);
  await humanizeBefore(resolveHumanize(action, ctx), env);
  dispatchMouseEvent(el, 'contextmenu', 'right');
  await humanizeAfter(resolveHumanize(action, ctx), env);
}

async function handleMiddleClick(action: any, ctx: RuntimeContext, env: Environment): Promise<void> {
  const target = interpolate(resolveSelectorAlias(action.target, ctx.variables.__selectors), ctx);
  const el = await env.findElement(target, action.timeout ?? 5000);
  if (!el) throw elementNotFound(target);
  await humanizeBefore(resolveHumanize(action, ctx), env);
  dispatchMouseEvent(el, 'click', 'middle');
  await humanizeAfter(resolveHumanize(action, ctx), env);
}

async function handleHover(action: any, ctx: RuntimeContext, env: Environment): Promise<void> {
  const target = interpolate(resolveSelectorAlias(action.target, ctx.variables.__selectors), ctx);
  const el = await env.findElement(target, action.timeout ?? 5000);
  if (!el) throw elementNotFound(target);
  await humanizeBefore(resolveHumanize(action, ctx), env);
  dispatchMouseEvent(el, 'mouseover', 'left');
  dispatchMouseEvent(el, 'mouseenter', 'left');
  const duration = action.duration;
  if (duration) await env.sleep(Array.isArray(duration) ? randomBetween(duration as [number, number]) : duration);
  dispatchMouseEvent(el, 'mouseout', 'left');
  await humanizeAfter(resolveHumanize(action, ctx), env);
}

async function handleHoverClick(action: any, ctx: RuntimeContext, env: Environment): Promise<void> {
  const hoverTarget = interpolate(resolveSelectorAlias(action.hoverTarget, ctx.variables.__selectors), ctx);
  const clickTarget = interpolate(resolveSelectorAlias(action.clickTarget, ctx.variables.__selectors), ctx);

  const hoverEl = await env.findElement(hoverTarget, action.timeout ?? 5000);
  if (!hoverEl) throw elementNotFound(hoverTarget);

  dispatchMouseEvent(hoverEl, 'mouseover', 'left');
  dispatchMouseEvent(hoverEl, 'mouseenter', 'left');

  const menuAppearTimeout = action.menuAppearTimeout ?? 1000;
  const menuAppearStart = Date.now();
  let clickEl: Element | null = null;
  while (Date.now() - menuAppearStart < menuAppearTimeout) {
    clickEl = await env.findElement(clickTarget, 200);
    if (clickEl) break;
    await env.sleep(100);
  }
  if (!clickEl) throw elementNotFound(clickTarget);

  await humanizeBefore(resolveHumanize(action, ctx), env);
  dispatchMouseEvent(clickEl, 'click', 'left');
  await humanizeAfter(resolveHumanize(action, ctx), env);
}

async function handleMoveMouse(action: any, ctx: RuntimeContext, env: Environment): Promise<void> {
  let endX: number;
  let endY: number;
  if ('x' in action.to) {
    endX = action.to.x;
    endY = action.to.y;
  } else {
    const target = interpolate(resolveSelectorAlias(action.to, ctx.variables.__selectors), ctx);
    const el = await env.findElement(target, action.timeout ?? 5000);
    if (!el) throw elementNotFound(target);
    const rect = el.getBoundingClientRect();
    endX = rect.left + rect.width / 2;
    endY = rect.top + rect.height / 2;
  }

  const startX = (window as any).__lastMouseX ?? endX;
  const startY = (window as any).__lastMouseY ?? endY;
  const duration = action.duration ?? 500;
  const durationMs = Array.isArray(duration) ? randomBetween(duration as [number, number]) : duration;
  const path = action.path ?? 'bezier';

  const steps = Math.max(10, Math.floor(durationMs / 16));
  for (let i = 0; i <= steps; i++) {
    const t = i / steps;
    let x: number, y: number;
    if (path === 'linear') {
      x = startX + (endX - startX) * t;
      y = startY + (endY - startY) * t;
    } else if (path === 'bezier') {
      const cp1x = startX + (endX - startX) * 0.2;
      const cp1y = startY + (endY - startY) * 0.8;
      const cp2x = startX + (endX - startX) * 0.8;
      const cp2y = startY + (endY - startY) * 0.2;
      const p = cubicBezier(t, { x: startX, y: startY }, { x: cp1x, y: cp1y }, { x: cp2x, y: cp2y }, { x: endX, y: endY });
      x = p.x;
      y = p.y;
    } else {
      x = startX + (endX - startX) * t + (Math.random() - 0.5) * 10;
      y = startY + (endY - startY) * t + (Math.random() - 0.5) * 10;
    }
    dispatchMouseMove(x, y);
    await env.sleep(durationMs / steps);
  }
  (window as any).__lastMouseX = endX;
  (window as any).__lastMouseY = endY;
}

async function handlePressAndHold(action: any, ctx: RuntimeContext, env: Environment): Promise<void> {
  const target = interpolate(resolveSelectorAlias(action.target, ctx.variables.__selectors), ctx);
  const el = await env.findElement(target, action.timeout ?? 5000);
  if (!el) throw elementNotFound(target);
  const button = action.button ?? 'left';
  dispatchMouseEvent(el, 'mousedown', button);
  const duration = action.duration ?? 2000;
  await env.sleep(Array.isArray(duration) ? randomBetween(duration as [number, number]) : duration);
  dispatchMouseEvent(el, 'mouseup', button);
}

async function handleDragAndDrop(action: any, ctx: RuntimeContext, env: Environment): Promise<void> {
  const sourceTarget = interpolate(resolveSelectorAlias(action.source, ctx.variables.__selectors), ctx);
  const targetTarget = interpolate(resolveSelectorAlias(action.target, ctx.variables.__selectors), ctx);
  const source = await env.findElement(sourceTarget, action.timeout ?? 5000);
  const target = await env.findElement(targetTarget, action.timeout ?? 5000);
  if (!source || !target) throw elementNotFound(source && target ? {} : (!source ? sourceTarget : targetTarget));

  const duration = action.duration ?? [800, 1500];
  const durationMs = Array.isArray(duration) ? randomBetween(duration as [number, number]) : duration;
  const wobble = resolveHumanize(action, ctx)?.wobble ?? 0;

  const startRect = source.getBoundingClientRect();
  const endRect = target.getBoundingClientRect();
  const startX = startRect.left + startRect.width / 2;
  const startY = startRect.top + startRect.height / 2;
  const endX = endRect.left + endRect.width / 2;
  const endY = endRect.top + endRect.height / 2;

  dispatchMouseEvent(source, 'mousedown', 'left', startX, startY);
  await env.sleep(100);

  const steps = Math.max(15, Math.floor(durationMs / 16));
  for (let i = 0; i <= steps; i++) {
    const t = i / steps;
    const x = startX + (endX - startX) * t + (Math.random() - 0.5) * wobble;
    const y = startY + (endY - startY) * t + (Math.random() - 0.5) * wobble;
    dispatchMouseMove(x, y);
    await env.sleep(durationMs / steps);
  }

  dispatchMouseEvent(target, 'mouseup', 'left', endX, endY);
  dispatchMouseEvent(target, 'drop', 'left', endX, endY);
}

async function handleDragBy(action: any, ctx: RuntimeContext, env: Environment): Promise<void> {
  const sourceTarget = interpolate(resolveSelectorAlias(action.source, ctx.variables.__selectors), ctx);
  const source = await env.findElement(sourceTarget, action.timeout ?? 5000);
  if (!source) throw elementNotFound(sourceTarget);

  const rect = source.getBoundingClientRect();
  const startX = rect.left + rect.width / 2;
  const startY = rect.top + rect.height / 2;
  const endX = startX + action.delta.x;
  const endY = startY + action.delta.y;

  const duration = action.duration ?? [500, 1000];
  const durationMs = Array.isArray(duration) ? randomBetween(duration as [number, number]) : duration;
  const wobble = resolveHumanize(action, ctx)?.wobble ?? 0;

  dispatchMouseEvent(source, 'mousedown', 'left', startX, startY);
  await env.sleep(100);

  const steps = Math.max(15, Math.floor(durationMs / 16));
  for (let i = 0; i <= steps; i++) {
    const t = i / steps;
    const x = startX + (endX - startX) * t + (Math.random() - 0.5) * wobble;
    const y = startY + (endY - startY) * t + (Math.random() - 0.5) * wobble;
    dispatchMouseMove(x, y);
    await env.sleep(durationMs / steps);
  }

  dispatchMouseEvent(source, 'mouseup', 'left', endX, endY);
}

async function handleSlide(action: any, ctx: RuntimeContext, env: Environment): Promise<void> {
  const target = interpolate(resolveSelectorAlias(action.target, ctx.variables.__selectors), ctx);
  const el = await env.findElement(target, action.timeout ?? 5000);
  if (!el) throw elementNotFound(target);

  const rect = el.getBoundingClientRect();
  const startX = rect.left + rect.width / 2;
  const startY = rect.top + rect.height / 2;
  const distance = action.distance ?? 200;
  const endX = action.direction === 'left' ? startX - distance : action.direction === 'right' ? startX + distance : startX;
  const endY = action.direction === 'up' ? startY - distance : action.direction === 'down' ? startY + distance : startY;

  const duration = action.duration ?? [800, 1500];
  const durationMs = Array.isArray(duration) ? randomBetween(duration as [number, number]) : duration;

  dispatchMouseEvent(el, 'mousedown', 'left', startX, startY);
  await env.sleep(100);

  const steps = Math.max(15, Math.floor(durationMs / 16));
  for (let i = 0; i <= steps; i++) {
    const t = i / steps;
    const x = startX + (endX - startX) * t;
    const y = startY + (endY - startY) * t;
    dispatchMouseMove(x, y);
    await env.sleep(durationMs / steps);
  }

  dispatchMouseEvent(el, 'mouseup', 'left', endX, endY);
}

async function handlePaste(action: any, ctx: RuntimeContext, env: Environment): Promise<void> {
  const target = interpolate(resolveSelectorAlias(action.target, ctx.variables.__selectors), ctx);
  const el = await env.findElement(target, action.timeout ?? 5000) as HTMLInputElement | HTMLTextAreaElement | null;
  if (!el) throw elementNotFound(target);
  const value = String(interpolate(action.value, ctx));
  el.focus();
  if (action.simulateClipboard) {
    el.dispatchEvent(new ClipboardEvent('paste', { bubbles: true, clipboardData: new DataTransfer() }));
  }
  el.value = value;
  el.dispatchEvent(new Event('input', { bubbles: true }));
  el.dispatchEvent(new Event('change', { bubbles: true }));
}

async function handleSelectRadio(action: any, ctx: RuntimeContext, env: Environment): Promise<void> {
  const target = interpolate(resolveSelectorAlias(action.target, ctx.variables.__selectors), ctx);
  const el = await env.findElement(target, action.timeout ?? 5000) as HTMLInputElement | null;
  if (!el) throw elementNotFound(target);
  if (!el.checked) {
    el.click();
  }
}

async function handleTypeAndSelect(action: any, ctx: RuntimeContext, env: Environment): Promise<void> {
  const target = interpolate(resolveSelectorAlias(action.target, ctx.variables.__selectors), ctx);
  const el = await env.findElement(target, action.timeout ?? 5000) as HTMLInputElement | null;
  if (!el) throw elementNotFound(target);
  const value = String(interpolate(action.value, ctx));
  el.focus();
  el.value = value;
  el.dispatchEvent(new Event('input', { bubbles: true }));

  const waitForSuggestions = action.waitForSuggestions ?? 1000;
  const start = Date.now();
  let selected = false;
  while (Date.now() - start < waitForSuggestions) {
    const suggestions = document.querySelectorAll(action.suggestionSelector);
    const matchBy = action.matchBy ?? 'contains';
    for (const s of Array.from(suggestions)) {
      const text = s.textContent?.trim() ?? '';
      const matches = matchBy === 'text' ? text === value : matchBy === 'startsWith' ? text.startsWith(value) : text.includes(value);
      if (matches) {
        (s as HTMLElement).click();
        selected = true;
        break;
      }
    }
    if (selected) break;
    await env.sleep(200);
  }
  if (!selected) throw new Error(`typeAndSelect: no suggestion matched for "${value}"`);
}

async function handleUploadFile(action: any, ctx: RuntimeContext, env: Environment): Promise<void> {
  const target = interpolate(resolveSelectorAlias(action.target, ctx.variables.__selectors), ctx);
  const el = await env.findElement(target, action.timeout ?? 5000) as HTMLInputElement | null;
  if (!el) throw elementNotFound(target);

  try {
    const files = (action.files as string[]).map((content, index) => {
      const blob = new Blob([content], { type: 'application/octet-stream' });
      return new File([blob], `upload-${index}.bin`, { type: blob.type });
    });

    const dt = new DataTransfer();
    for (const f of files) dt.items.add(f);
    el.files = dt.files;
    el.dispatchEvent(new Event('change', { bubbles: true }));
  } catch (e) {
    await env.transport.sendLog('warn', 'uploadFile: browser blocked setting input.files', { error: (e as Error).message });
  }
}

async function handleFocus(action: any, ctx: RuntimeContext, env: Environment): Promise<void> {
  const target = interpolate(resolveSelectorAlias(action.target, ctx.variables.__selectors), ctx);
  const el = await env.findElement(target, action.timeout ?? 5000) as HTMLElement | null;
  if (!el) throw elementNotFound(target);
  el.focus();
}

async function handleBlur(action: any, ctx: RuntimeContext, env: Environment): Promise<void> {
  if (action.target) {
    const target = interpolate(resolveSelectorAlias(action.target, ctx.variables.__selectors), ctx);
    const el = await env.findElement(target, action.timeout ?? 5000) as HTMLElement | null;
    if (el) el.blur();
  } else {
    (document.activeElement as HTMLElement)?.blur?.();
  }
}

async function handleTab(action: any, ctx: RuntimeContext, env: Environment): Promise<void> {
  const count = action.count ?? 1;
  const key = action.action === 'tabToPrevious' ? 'Shift+Tab' : 'Tab';
  for (let i = 0; i < count; i++) {
    if (key === 'Shift+Tab') {
      document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Tab', shiftKey: true, bubbles: true }));
      document.dispatchEvent(new KeyboardEvent('keyup', { key: 'Tab', shiftKey: true, bubbles: true }));
    } else {
      document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Tab', bubbles: true }));
      document.dispatchEvent(new KeyboardEvent('keyup', { key: 'Tab', bubbles: true }));
    }
    await env.sleep(100);
  }
}

// ==================== 等待 ====================

async function handleWaitFor(action: any, ctx: RuntimeContext, env: Environment): Promise<void> {
  const target = interpolate(resolveSelectorAlias(action.target, ctx.variables.__selectors), ctx);
  const el = await env.findElement(target, action.timeout ?? 10000);
  if (!el) throw elementNotFound(target);
}

async function handleWaitForText(action: any, ctx: RuntimeContext, env: Environment): Promise<void> {
  const target = interpolate(resolveSelectorAlias(action.target, ctx.variables.__selectors), ctx);
  const text = interpolate(action.text, ctx);
  const start = Date.now();
  while (Date.now() - start < (action.timeout ?? 10000)) {
    const el = await env.findElement(target, 500);
    if (el && el.textContent?.includes(text)) return;
    await env.sleep(200);
  }
  throw new Error('TimeoutError');
}

async function handleWaitForTimeout(action: any, ctx: RuntimeContext, env: Environment): Promise<void> {
  const ms = action.ms;
  const delay = Array.isArray(ms) ? randomBetween(ms as [number, number]) : ms;
  await env.sleep(delay);
}

async function handleWaitForElementHidden(action: any, ctx: RuntimeContext, env: Environment): Promise<void> {
  const target = interpolate(resolveSelectorAlias(action.target, ctx.variables.__selectors), ctx);
  const start = Date.now();
  while (Date.now() - start < (action.timeout ?? 10000)) {
    const el = await env.findElement(target, 500);
    if (!el) return;
    await env.sleep(200);
  }
  throw new Error('TimeoutError');
}

async function handleWaitForUrl(action: any, ctx: RuntimeContext, env: Environment): Promise<void> {
  const pattern = interpolate(action.pattern, ctx);
  const matchType = action.matchType ?? 'contains';
  const timeout = action.timeout ?? 10000;
  const start = Date.now();
  while (Date.now() - start < timeout) {
    const url = env.getUrl();
    let matched = false;
    if (matchType === 'equals') matched = url === pattern;
    else if (matchType === 'contains') matched = url.includes(pattern);
    else if (matchType === 'matches') matched = new RegExp(pattern).test(url);
    if (matched) return;
    await env.sleep(200);
  }
  throw new Error('TimeoutError');
}

async function handleWaitForElementVisible(action: any, ctx: RuntimeContext, env: Environment): Promise<void> {
  const target = interpolate(resolveSelectorAlias(action.target, ctx.variables.__selectors), ctx);
  const timeout = action.timeout ?? 10000;
  const start = Date.now();
  while (Date.now() - start < timeout) {
    const el = await env.findElement(target, 500);
    if (el && isElementVisible(el)) return;
    await env.sleep(200);
  }
  throw new Error('TimeoutError');
}

async function handleWaitForFunction(action: any, ctx: RuntimeContext, env: Environment): Promise<void> {
  const script = interpolate(action.script, ctx);
  const pollingInterval = action.pollingInterval ?? 200;
  const timeout = action.timeout ?? 10000;
  const start = Date.now();
  while (Date.now() - start < timeout) {
    try {
      const result = await env.evaluate(script, { variables: ctx.variables, extracted: ctx.extracted, evaluated: ctx.evaluated }, []);
      if (result) return;
    } catch (e) {
      throw new Error(`ScriptError: ${(e as Error).message}`);
    }
    await env.sleep(pollingInterval);
  }
  throw new Error('TimeoutError');
}

async function handleReadPause(action: any, ctx: RuntimeContext, env: Environment): Promise<void> {
  const ms = action.ms;
  const delay = Array.isArray(ms) ? randomBetween(ms as [number, number]) : ms;
  if (action.scrollWhileReading) {
    const steps = 5;
    const stepMs = delay / steps;
    const stepPx = window.innerHeight * 0.5;
    for (let i = 0; i < steps; i++) {
      window.scrollBy({ top: stepPx, behavior: 'smooth' });
      await env.sleep(stepMs);
    }
  } else {
    await env.sleep(delay);
  }
}

// ==================== 滚动与导航 ====================

async function handleScrollToBottom(action: any, ctx: RuntimeContext, env: Environment): Promise<void> {
  if (action.stepBy) {
    const step = window.innerHeight * 0.8;
    while (window.innerHeight + window.scrollY < document.body.scrollHeight - 100) {
      window.scrollBy({ top: step, behavior: 'smooth' });
      await env.sleep(500);
    }
  } else {
    window.scrollTo({ top: document.body.scrollHeight, behavior: 'smooth' });
  }
  await env.sleep(500);
}

async function handleScrollBy(action: any, ctx: RuntimeContext, env: Environment): Promise<void> {
  const horizontal = action.direction === 'left' || action.direction === 'right';
  const unitDistance = action.unit === 'pages'
    ? (horizontal ? window.innerWidth : window.innerHeight)
    : 1;
  const distance = action.distance * unitDistance * (action.direction === 'up' || action.direction === 'left' ? -1 : 1);
  const top = action.direction === 'up' || action.direction === 'down' ? distance : 0;
  const left = action.direction === 'left' || action.direction === 'right' ? distance : 0;
  if (action.target) {
    const target = interpolate(resolveSelectorAlias(action.target, ctx.variables.__selectors), ctx);
    const el = await env.findElement(target, action.timeout ?? 5000) as HTMLElement | null;
    if (!el) throw elementNotFound(target);
    el.scrollBy({ top, left, behavior: 'smooth' });
  } else {
    window.scrollBy({ top, left, behavior: 'smooth' });
  }
  await env.sleep(500);
}

async function handleScrollTo(action: any, ctx: RuntimeContext, env: Environment): Promise<void> {
  const target = interpolate(resolveSelectorAlias(action.target, ctx.variables.__selectors), ctx);
  const el = await env.findElement(target, action.timeout ?? 5000);
  if (!el) throw elementNotFound(target);
  el.scrollIntoView({ behavior: 'smooth', block: action.align ?? 'center' });
  await env.sleep(500);
}

async function handleScrollToTop(action: any, ctx: RuntimeContext, env: Environment): Promise<void> {
  window.scrollTo({ top: 0, behavior: 'smooth' });
  await env.sleep(500);
}

async function handlePageDown(action: any, ctx: RuntimeContext, env: Environment): Promise<void> {
  const count = action.count ?? 1;
  const direction = action.action === 'pageUp' ? -1 : 1;
  window.scrollBy({ top: direction * count * window.innerHeight * 0.8, behavior: 'smooth' });
  await env.sleep(500);
}

async function handleSetStyle(action: any, ctx: RuntimeContext, env: Environment): Promise<void> {
  const target = interpolate(resolveSelectorAlias(action.target, ctx.variables.__selectors), ctx);
  const el = await env.findElement(target, action.timeout ?? 5000);
  if (!el) throw elementNotFound(target);
  const style = interpolate(action.style, ctx);
  Object.assign((el as HTMLElement).style, style);
}

async function handleRemoveElement(action: any, ctx: RuntimeContext, env: Environment): Promise<void> {
  const target = interpolate(resolveSelectorAlias(action.target, ctx.variables.__selectors), ctx);
  const el = await env.findElement(target, action.timeout ?? 5000);
  if (!el) throw elementNotFound(target);
  el.remove();
}

async function handleScrollIntoView(action: any, ctx: RuntimeContext, env: Environment): Promise<void> {
  const target = interpolate(resolveSelectorAlias(action.target, ctx.variables.__selectors), ctx);
  const el = await env.findElement(target, action.timeout ?? 5000);
  if (!el) throw elementNotFound(target);
  el.scrollIntoView({ behavior: 'smooth', block: action.align ?? 'center' });
  await env.sleep(500);
}

async function handleNavigate(action: any, ctx: RuntimeContext, env: Environment): Promise<void> {
  const url = interpolate(action.url, ctx);
  // H-1: reject dangerous URL schemes that could execute code in the page
  // context (javascript:, data:, blob:). Preflight skips dynamic URLs with
  // {{...}}, so runtime validation is the last line of defense.
  let parsed: URL;
  try {
    parsed = new URL(url, typeof location !== 'undefined' ? location.href : 'https://localhost/');
  } catch {
    throw new Error(`navigate URL is not valid: ${url}`);
  }
  if (parsed.protocol !== 'http:' && parsed.protocol !== 'https:') {
    throw new Error(`navigate URL scheme "${parsed.protocol}" is not allowed; only http/https are permitted`);
  }
  const startUrl = env.getUrl();
  location.href = url;
  const changed = await waitForUrlChange(env, startUrl, action.timeout ?? 5000);
  if (!changed) {
    throw new Error(`TimeoutError: navigation did not commit from ${startUrl}`);
  }
  ctx.page = { url: env.getUrl(), title: env.getTitle() };
}

async function handleReload(action: any, ctx: RuntimeContext, env: Environment): Promise<void> {
  location.reload();
  await env.sleep(3000);
}

// ==================== 脚本与页面操作 ====================

async function handleEvaluate(action: any, ctx: RuntimeContext, env: Environment): Promise<any> {
  const script = interpolate(action.script, ctx);
  const args = interpolate(action.args ?? [], ctx);
  const scriptCtx = {
    variables: ctx.variables,
    extracted: ctx.extracted,
    evaluated: ctx.evaluated,
    loopIndex: ctx.loopIndex,
    loopItem: ctx.loopItem,
  };
  const result = await env.evaluate(script, scriptCtx, args);
  if (action.name) {
    ctx.evaluated[action.name] = result;
  }
  return result;
}

// ==================== 输出与状态 ====================

async function handleSendResult(action: any, ctx: RuntimeContext, env: Environment): Promise<void> {
  const payload = interpolate(action.payload, ctx);
  await env.transport.sendResult(payload, action.immediate !== false);
  ctx.resultsSent++;
}

async function handleSendLog(action: any, ctx: RuntimeContext, env: Environment): Promise<void> {
  const message = interpolate(action.message, ctx);
  const extra = interpolate(action.extra ?? {}, ctx);
  await env.transport.sendLog(action.level, message, extra);
  ctx.logsSent++;
}

async function handleSendScreenshot(action: any, ctx: RuntimeContext, env: Environment): Promise<void> {
  const target = action.target ? interpolate(resolveSelectorAlias(action.target, ctx.variables.__selectors), ctx) : undefined;
  const snapshot = target ? await env.saveSnapshot(action.name ?? 'screenshot', 'screenshot') : await env.screenshot(action.name ?? 'screenshot');
  if (snapshot) await env.transport.sendSnapshot(snapshot);
}

async function handleSendHtml(action: any, ctx: RuntimeContext, env: Environment): Promise<void> {
  let html: string;
  if (action.target) {
    const target = interpolate(resolveSelectorAlias(action.target, ctx.variables.__selectors), ctx);
    const el = await env.findElement(target, action.timeout ?? 5000);
    if (!el) throw elementNotFound(target);
    html = el.outerHTML;
  } else {
    html = document.documentElement.innerHTML;
  }
  await env.transport.sendResult({ [action.name ?? 'html']: html }, action.immediate !== false);
}

async function handleEmitEvent(action: any, ctx: RuntimeContext, env: Environment): Promise<void> {
  const eventName = action.event;
  if (!eventName) {
    throw new Error('ScriptError: emitEvent requires a non-empty event name');
  }
  const event = new CustomEvent(eventName, {
    detail: interpolate(action.payload ?? {}, ctx),
    bubbles: true,
    cancelable: true,
  });
  document.dispatchEvent(event);
}

async function handleUpdateStatus(action: any, ctx: RuntimeContext, env: Environment): Promise<void> {
  const message = interpolate(action.message, ctx);
  await env.transport.sendStatus(action.status, message);
}

// ==================== 生产级运维 ====================

async function handleCheckpoint(action: any, ctx: RuntimeContext, env: Environment): Promise<void> {
  ctx.checkpoint = {
    stepId: action.name,
    url: env.getUrl(),
    variables: { ...ctx.variables },
    extracted: { ...ctx.extracted },
    timestamp: Date.now(),
  };
  await env.transport.sendLog('info', `Checkpoint saved: ${action.name}`, { checkpoint: ctx.checkpoint });
}

async function handleFlushResults(action: any, ctx: RuntimeContext, env: Environment): Promise<void> {
  await env.transport.flushResults?.();
}

async function handleHeartbeat(action: any, ctx: RuntimeContext, env: Environment): Promise<void> {
  const payload = interpolate(action.payload ?? {}, ctx);
  await env.transport.sendHeartbeat(payload);
}

async function handleSaveSnapshot(action: any, ctx: RuntimeContext, env: Environment): Promise<void> {
  const snapshot = await env.saveSnapshot(action.name, action.type ?? 'html');
  if (snapshot) await env.transport.sendSnapshot(snapshot);
}

async function handleAbort(action: any, ctx: RuntimeContext, env: Environment): Promise<never> {
  if (action.flushBeforeAbort) {
    await env.transport.flushResults?.();
  }
  throw new Error(`Aborted: ${action.reason ?? 'unknown'}`);
}

// ==================== 数据操作 ====================

async function handleTransform(action: any, ctx: RuntimeContext, env: Environment): Promise<any> {
  let data = resolveExpressionSource(action.from, ctx);
  for (const op of action.operations) {
    data = applyTransformOp(data, op, ctx);
  }
  ctx.extracted[action.name] = data;
  return data;
}

async function handleFilter(action: any, ctx: RuntimeContext, env: Environment): Promise<any> {
  const data = resolveExpressionSource(action.from, ctx);
  const arr = Array.isArray(data) ? data : [];
  const criteria = interpolate(action.criteria, ctx);
  const filtered = arr.filter((item) => matchesFilterCriteria(item, criteria));
  ctx.extracted[action.name] = filtered;
  return filtered;
}

async function handleDeduplicate(action: any, ctx: RuntimeContext, env: Environment): Promise<any> {
  const data = resolveExpressionSource(action.from, ctx);
  const arr = Array.isArray(data) ? data : [];
  const seen = new Set<string>();
  const result = [];
  for (const item of arr) {
    const key = action.keys.map((k: string) => getPath(item, k)).join('|');
    if (!seen.has(key)) {
      seen.add(key);
      result.push(item);
    }
  }
  ctx.extracted[action.name] = result;
  return result;
}

async function handleValidateData(action: any, ctx: RuntimeContext, env: Environment): Promise<any> {
  const data = resolveExpressionSource(action.from, ctx);
  if (!action.schema) return data;

  let validate = validateSchemaCache.get(action.schema);
  if (!validate) {
    validate = validateAjv.compile(action.schema);
    validateSchemaCache.set(action.schema, validate);
  }

  const valid = validate(data);
  if (!valid) {
    const errors = validateAjv.errorsText(validate.errors);
    if (action.onInvalid !== 'warn') {
      throw new Error(`ValidationError: ${action.from} → ${errors}`);
    }
    await env.transport.sendLog('warn', `validateData failed for ${action.from}`, { errors, data });
  }
  return data;
}

async function handleSetTag(action: any, ctx: RuntimeContext, env: Environment): Promise<void> {
  // Legacy behavior: accumulate log metadata for TaggedTransport. Scope and
  // checkpoint/navigation resume semantics remain intentionally unsupported;
  // collection result payloads are never modified.
  ctx.tags = { ...(ctx.tags ?? {}), ...(action.tags ?? {}) };
  await env.transport.sendLog('info', 'setTag', { set: action.tags, active: ctx.tags });
}

async function handleLogMetric(action: any, ctx: RuntimeContext, env: Environment): Promise<void> {
  const value = typeof action.value === 'string' ? Number(interpolate(action.value, ctx)) : action.value;
  await env.transport.sendLog('info', 'metric', { name: action.name, value, unit: action.unit, tags: action.tags });
}

async function handleSleep(action: any, ctx: RuntimeContext, env: Environment): Promise<void> {
  const ms = action.ms;
  await env.sleep(Array.isArray(ms) ? randomBetween(ms as [number, number]) : ms);
}

// ==================== 流程控制 ====================
// Note (Phase 3): if/loop/switch/group handlers now live in executor-utils.ts
// next to runSteps so the inline flow-control dispatcher can propagate
// checkpoint options+containerPath. Only retry/break/continue/exit remain
// here — they are dispatched via executeAction as leaf actions.

async function handleRetry(action: any, ctx: RuntimeContext, env: Environment): Promise<any> {
  const config = parseRetry(action.config, 0);
  let lastError: any;
  for (let attempt = 0; attempt <= config.maxAttempts; attempt++) {
    try {
      const result = await runSteps(action.steps, ctx, env, executeAction);
      if (result.error) throw new Error(result.error.message);
      return;
    } catch (err: any) {
      if (err instanceof FlowControlSignal || err instanceof ExitSignal) throw err;
      lastError = err;
      if (attempt === config.maxAttempts) break;
      const delay = calculateDelay(config, attempt);
      await env.transport.sendLog('warn', `Retrying group ${action.id ?? 'retry'}`, { attempt, delay });
      await env.sleep(delay);
    }
  }
  throw lastError;
}

async function handleBreakContinue(action: any, ctx: RuntimeContext, env: Environment): Promise<never> {
  throw new FlowControlSignal(action.action);
}

const EXIT_STATUSES: Array<ExitSignal['status']> = ['success', 'failure', 'cancelled'];

async function handleExit(action: any, ctx: RuntimeContext, env: Environment): Promise<never> {
  const status = action.status ?? 'success';
  if (!EXIT_STATUSES.includes(status)) {
    throw new Error(`ScriptError: invalid exit status ${status}`);
  }
  throw new ExitSignal(status, action.message ?? '');
}

// ==================== 辅助函数 ====================

function elementNotFound(target: any): Error {
  return new Error(`ElementNotFound: ${JSON.stringify(target)}`);
}

function elementDisabled(target: any): Error {
  return new Error(`ElementDisabled: ${JSON.stringify(target)}`);
}

function isDisabledFormControl(el: Element): boolean {
  return ['button', 'input', 'select', 'textarea', 'option', 'optgroup', 'fieldset'].includes(
    el.tagName.toLowerCase(),
  ) && el.matches(':disabled');
}

function resolveHumanize(action: Action, ctx: RuntimeContext): Humanize | undefined {
  const ruleH = ctx.ruleDefaults?.humanize;
  const stepH = action.humanize;
  if (!ruleH) return stepH;
  if (!stepH) return ruleH;
  return { ...ruleH, ...stepH };
}

async function humanizeBefore(humanize: any, env: Environment, el?: Element | null): Promise<void> {
  if (humanize?.preDelay) await env.sleep(randomBetween(humanize.preDelay));
  if (humanize?.moveMouse && el) {
    const rect = el.getBoundingClientRect();
    const x = rect.left + rect.width / 2 + randomOffset(humanize.randomOffset);
    const y = rect.top + rect.height / 2 + randomOffset(humanize.randomOffset);
    const startX = (window as any).__lastMouseX ?? x;
    const startY = (window as any).__lastMouseY ?? y;
    const steps = 10;
    for (let i = 0; i <= steps; i++) {
      const t = i / steps;
      const mx = startX + (x - startX) * t + (Math.random() - 0.5) * 5;
      const my = startY + (y - startY) * t + (Math.random() - 0.5) * 5;
      dispatchMouseMove(mx, my);
      await env.sleep(16);
    }
  }
}

async function humanizeAfter(humanize: any, env: Environment): Promise<void> {
  if (humanize?.postDelay) await env.sleep(randomBetween(humanize.postDelay));
}

function randomOffset(offset: number | { x: number; y: number } | undefined): number {
  if (offset === undefined) return 0;
  if (typeof offset === 'number') return (Math.random() - 0.5) * offset;
  return (Math.random() - 0.5) * offset.x;
}

function resolveExpressionSource(from: string, ctx: RuntimeContext): any {
  if (from === 'extracted') return ctx.extracted;
  if (from === 'variables') return ctx.variables;
  if (from === 'evaluated') return ctx.evaluated;
  if (from.startsWith('extracted.')) return getPath(ctx.extracted, from.slice(10));
  if (from.startsWith('variables.')) return getPath(ctx.variables, from.slice(10));
  if (from.startsWith('evaluated.')) return getPath(ctx.evaluated, from.slice(10));
  return ctx.extracted[from] ?? ctx.variables[from] ?? ctx.evaluated[from];
}

function getPath(obj: any, path: string): any {
  return path.split('.').reduce((o, k) => (o == null ? undefined : o[k]), obj);
}

function applyTransformOp(data: any, op: any, ctx: RuntimeContext): any {
  if (op.type === 'map' && Array.isArray(data)) {
    const rename = op.params?.rename ?? {};
    return data.map((item) => {
      const out: any = {};
      for (const [k, v] of Object.entries(item)) out[rename[k] ?? k] = v;
      return out;
    });
  }
  if (op.type === 'number') {
    if (Array.isArray(data)) return data.map((item) => (op.field ? { ...item, [op.field]: toNumber(getPath(item, op.field)) } : toNumber(item)));
    return toNumber(data);
  }
  if (op.type === 'filter') {
    const arr = Array.isArray(data) ? data : [];
    const criteria = op.params?.criteria ?? { field: op.field, ...op.params };
    return arr.filter((item) => matchesFilterCriteria(item, criteria));
  }
  if (op.type === 'regex') {
    const apply = (v: any): any => {
      if (typeof v !== 'string') return v;
      try {
        const re = new RegExp(op.params?.pattern ?? '');
        const m = v.match(re);
        if (!m) return v;
        const group = op.params?.group ?? 0;
        return m[group] ?? m[0];
      } catch { return v; }
    };
    return op.field ? applyToField(data, op.field, apply) : apply(data);
  }
  if (op.type === 'replace') {
    const apply = (v: any): any => {
      if (typeof v !== 'string') return v;
      try {
        const re = new RegExp(op.params?.pattern ?? '', op.params?.flags ?? 'g');
        return v.replace(re, op.params?.replacement ?? '');
      } catch { return v; }
    };
    return op.field ? applyToField(data, op.field, apply) : apply(data);
  }
  if (op.type === 'trim') {
    const apply = (v: any): any => (typeof v === 'string' ? v.trim() : v);
    return op.field ? applyToField(data, op.field, apply) : apply(data);
  }
  if (op.type === 'date') {
    const apply = (v: any): any => {
      if (v == null || v instanceof Date) return v instanceof Date ? v.toISOString() : v;
      const d = new Date(v);
      return isNaN(d.getTime()) ? v : d.toISOString();
    };
    return op.field ? applyToField(data, op.field, apply) : apply(data);
  }
  if (op.type === 'jsonParse') {
    const apply = (v: any): any => {
      if (typeof v !== 'string') return v;
      try { return JSON.parse(v); } catch { return v; }
    };
    return op.field ? applyToField(data, op.field, apply) : apply(data);
  }
  if (op.type === 'custom') {
    // Custom-script execution is intentionally NOT implemented in the
    // ScriptCat content-script runtime (no vm module; arbitrary script
    // eval is unsafe in MV3). Remains [stub] — op accepted by schema
    // for forward compat but is a no-op here. Use `evaluate` action with
    // trusted=true for arbitrary scripting.
    return data;
  }
  return data;
}

/** Apply a transform to a single field path, preserving the surrounding object/array. */
function applyToField(data: any, field: string, fn: (v: any) => any): any {
  if (Array.isArray(data)) {
    return data.map((item) => {
      if (item == null) return item;
      const v = getPath(item, field);
      return { ...item, [field]: fn(v) };
    });
  }
  if (data == null || typeof data !== 'object') return data;
  return { ...data, [field]: fn(getPath(data, field)) };
}

function matchesFilterCriteria(item: any, criteria: any): boolean {
  const value = criteria.field ? getPath(item, criteria.field) : item;
  const expected = criteria.value;
  switch (criteria.op) {
    case 'eq':
      return value == expected;
    case 'ne':
      return value != expected;
    case 'gt':
      return value > expected;
    case 'gte':
      return value >= expected;
    case 'lt':
      return value < expected;
    case 'lte':
      return value <= expected;
    case 'contains':
      return String(value).includes(String(expected));
    case 'matches':
      // regex match: expected is the pattern string. Invalid regex → no match.
      try { return new RegExp(expected).test(String(value)); } catch { return false; }
    case 'exists':
      // field path resolves to a non-null/undefined value
      return value !== undefined && value !== null;
    case 'notEmpty':
      return value !== undefined && value !== null && value !== '';
    default:
      return true;
  }
}

// ==================== 高级 action 处理器 ====================

interface CircuitBreakerState {
  failures: number[]; // timestamps within windowMs
  open: boolean;
  openedAt: number; // ms epoch when opened
  halfOpen: boolean;
  probeStartedAt: number; // ms epoch when the single half-open probe was admitted
}

function normalizeCircuitBreakerState(
  stored: unknown,
  now: number,
  threshold: number,
): CircuitBreakerState {
  const value = stored && typeof stored === 'object'
    ? stored as Record<string, unknown>
    : {};
  const storedOpenedAt = typeof value.openedAt === 'number' && Number.isFinite(value.openedAt)
    ? value.openedAt
    : 0;
  const legacyLastFailure = typeof value.lastFailure === 'number' && Number.isFinite(value.lastFailure)
    ? value.lastFailure
    : 0;

  let failures: number[];
  if (Array.isArray(value.failures)) {
    failures = value.failures.filter((timestamp): timestamp is number => (
      typeof timestamp === 'number' && Number.isFinite(timestamp)
    ));
  } else if (typeof value.failures === 'number' && Number.isFinite(value.failures)) {
    // Before the sliding-window implementation, the browser-local state used
    // a numeric counter plus lastFailure. Preserve enough entries to retain
    // threshold behavior without trusting an unbounded page-controlled count.
    const count = Math.min(
      Math.max(0, Math.floor(value.failures)),
      Math.max(1, Math.min(Math.ceil(threshold), 10_000)),
    );
    const timestamp = legacyLastFailure || storedOpenedAt || now;
    failures = Array.from({ length: count }, () => timestamp);
  } else {
    failures = [];
  }

  const open = value.open === true;
  const halfOpen = !open && value.halfOpen === true;
  const storedProbeStartedAt = typeof value.probeStartedAt === 'number' && Number.isFinite(value.probeStartedAt)
    ? value.probeStartedAt
    : 0;

  return {
    failures,
    open,
    openedAt: open ? (storedOpenedAt || legacyLastFailure || now) : 0,
    halfOpen,
    probeStartedAt: halfOpen ? (storedProbeStartedAt || now) : 0,
  };
}

function persistCircuitBreakerState(
  store: Record<string, unknown>,
  name: string,
  state: CircuitBreakerState,
): void {
  store[name] = state;
  (window as any).__ocCircuitBreakers = store;
}

async function runCircuitBreakerOnOpen(
  action: CircuitBreakerAction,
  ctx: RuntimeContext,
  env: Environment,
): Promise<void> {
  if (!action.onOpen) return;
  const result = await runSteps(action.onOpen, ctx, env, executeAction);
  if (result.error) {
    throw new Error(result.error.message);
  }
}

async function throwCircuitBreakerOpen(
  action: CircuitBreakerAction,
  ctx: RuntimeContext,
  env: Environment,
  state: CircuitBreakerState,
  message: string,
): Promise<never> {
  await env.transport.sendLog('warn', message, { state });
  await runCircuitBreakerOnOpen(action, ctx, env);
  throw new Error(`CircuitBreakerOpen: ${action.name}`);
}

async function reopenCircuitBreaker(
  action: CircuitBreakerAction,
  ctx: RuntimeContext,
  env: Environment,
  store: Record<string, unknown>,
  state: CircuitBreakerState,
  now: number,
  halfOpenFailure: boolean,
): Promise<never> {
  state.open = true;
  state.openedAt = now;
  state.halfOpen = false;
  state.probeStartedAt = 0;
  persistCircuitBreakerState(store, action.name, state);
  const message = halfOpenFailure
    ? `Circuit breaker ${action.name} REOPENED after half-open probe failure`
    : `Circuit breaker ${action.name} OPENED after ${state.failures.length} failures`;
  return throwCircuitBreakerOpen(action, ctx, env, state, message);
}

async function handleCircuitBreaker(
  action: CircuitBreakerAction,
  ctx: RuntimeContext,
  env: Environment,
): Promise<void> {
  const stored = (window as any).__ocCircuitBreakers;
  const store: Record<string, unknown> = stored && typeof stored === 'object' ? stored : {};

  const now = env.now();
  const windowMs = action.windowMs ?? 60000;
  const cooldownMs = action.cooldownMs ?? 300000;
  const threshold = action.failureThreshold ?? 5;
  const state = normalizeCircuitBreakerState(store[action.name], now, threshold);

  // Prune failures outside the sliding window
  state.failures = state.failures.filter((t) => now - t < windowMs);

  if (state.open) {
    if (now - state.openedAt < cooldownMs) {
      // Still within cooldown — reject
      persistCircuitBreakerState(store, action.name, state);
      return throwCircuitBreakerOpen(
        action,
        ctx,
        env,
        state,
        `Circuit breaker ${action.name} is OPEN`,
      );
    }

    // Cooldown elapsed: admit one probe and retain an explicit half-open
    // state. A failure report for that probe reopens immediately, independent
    // of the normal closed-state threshold.
    state.open = false;
    state.openedAt = 0;
    state.failures = [];
    state.halfOpen = true;
    state.probeStartedAt = now;

    if (action.record === true) {
      state.failures = [now];
      return reopenCircuitBreaker(action, ctx, env, store, state, now, true);
    }

    persistCircuitBreakerState(store, action.name, state);
    return;
  }

  if (state.halfOpen) {
    if (action.record === true) {
      state.failures = [now];
      return reopenCircuitBreaker(action, ctx, env, store, state, now, true);
    }

    // Explicit record:false is the success acknowledgement. For compatibility
    // with existing gate-before-operation rules that omit a success action,
    // the next sequential gate also proves that no failure was recorded for
    // the prior probe and closes the breaker before admitting normal work.
    state.failures = [];
    state.halfOpen = false;
    state.probeStartedAt = 0;
    persistCircuitBreakerState(store, action.name, state);
    return;
  }

  // Record a failure when invoked in record mode (used after a failing
  // operation). When omitted, the action is a pure gate that checks state.
  if (action.record) {
    state.failures.push(now);
    if (state.failures.length >= threshold) {
      return reopenCircuitBreaker(action, ctx, env, store, state, now, false);
    }
  }

  persistCircuitBreakerState(store, action.name, state);
}

async function handleCheckQuota(action: any, ctx: RuntimeContext, env: Environment): Promise<void> {
  const key = `oc_quota_${action.type}_${action.limit}`;
  const now = Date.now();
  const windowMs = action.cooldownMs ?? 60000;
  const stored = JSON.parse(localStorage.getItem(key) ?? '{}');
  if (stored.resetAt && now > stored.resetAt) {
    stored.count = 0;
    stored.resetAt = now + windowMs;
  }
  const count = (stored.count ?? 0) + 1;
  if (count > action.limit) {
    const onExceeded = action.onExceeded ?? 'fail';
    if (onExceeded === 'wait') {
      await env.sleep(Math.max(1000, (stored.resetAt ?? now) - now));
    } else if (onExceeded === 'fail') {
      throw new Error(`QuotaExceeded: ${action.type} limit ${action.limit}`);
    } else if (onExceeded === 'skip') {
      return;
    }
  }
  stored.count = count;
  stored.resetAt = stored.resetAt ?? now + windowMs;
  localStorage.setItem(key, JSON.stringify(stored));
}

async function handleRequestHuman(action: any, ctx: RuntimeContext, env: Environment): Promise<void> {
  const timeout = action.timeout ?? 120000;
  await requestHumanIntervention({
    type: action.type,
    prompt: action.prompt ?? 'human intervention required',
    timeoutMs: timeout,
    stepId: action.id ?? ctx.lastStepId ?? `requestHuman:${action.type}`,
  }, ctx, env);
  if (Array.isArray(action.then) && action.then.length > 0) {
    const result = await runSteps(action.then, ctx, env, executeAction);
    if (result.error) {
      throw new Error(result.error.message);
    }
  }
}

async function handleSolveCaptcha(action: any, ctx: RuntimeContext, env: Environment): Promise<string | null> {
  if (action.fallbackToHuman) {
    return handleRequestHuman({ type: 'captcha', prompt: 'Please solve the captcha', timeout: 120000 }, ctx, env).then(() => null);
  }
  throw new Error('CaptchaError: no captcha provider configured');
}

async function handleRecover(action: any, ctx: RuntimeContext, env: Environment): Promise<void> {
  const checkpointName = action.checkpointName;
  if (checkpointName && ctx.checkpoint?.stepId === checkpointName) {
    await env.transport.sendLog('info', `Recovering from checkpoint ${checkpointName}`, { checkpoint: ctx.checkpoint });
  }
  if (action.steps) {
    for (const step of action.steps) {
      await executeAction(step, ctx, env);
    }
  }
}

// ==================== 鼠标事件辅助函数 ====================

function dispatchMouseEvent(el: Element | null, type: string, button: string, clientX?: number, clientY?: number): void {
  if (!el) return;
  const rect = el.getBoundingClientRect();
  const x = clientX ?? rect.left + rect.width / 2 + (Math.random() - 0.5) * 4;
  const y = clientY ?? rect.top + rect.height / 2 + (Math.random() - 0.5) * 4;
  const buttonNumber = button === 'left' ? 0 : button === 'middle' ? 1 : 2;
  const evt = new MouseEvent(type, {
    bubbles: true,
    cancelable: true,
    button: buttonNumber,
    buttons: type === 'mouseup' ? 0 : 1,
    clientX: x,
    clientY: y,
  });
  el.dispatchEvent(evt);
  (window as any).__lastMouseX = x;
  (window as any).__lastMouseY = y;
}

function dispatchMouseMove(clientX: number, clientY: number): void {
  const evt = new MouseEvent('mousemove', {
    bubbles: true,
    cancelable: true,
    clientX,
    clientY,
  });
  document.dispatchEvent(evt);
  (window as any).__lastMouseX = clientX;
  (window as any).__lastMouseY = clientY;
}

function cubicBezier(t: number, p0: { x: number; y: number }, p1: { x: number; y: number }, p2: { x: number; y: number }, p3: { x: number; y: number }): { x: number; y: number } {
  const cX = 3 * (p1.x - p0.x);
  const bX = 3 * (p2.x - p1.x) - cX;
  const aX = p3.x - p0.x - cX - bX;
  const cY = 3 * (p1.y - p0.y);
  const bY = 3 * (p2.y - p1.y) - cY;
  const aY = p3.y - p0.y - cY - bY;
  const x = aX * t * t * t + bX * t * t + cX * t + p0.x;
  const y = aY * t * t * t + bY * t * t + cY * t + p0.y;
  return { x, y };
}
