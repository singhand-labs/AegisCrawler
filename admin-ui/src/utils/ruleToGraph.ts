import type { Action, ActionType, Condition, Rule, RuleHooks, Target } from '../types/rule';
import type { ActionPath } from './ruleEdit';
import { pathToString } from './ruleEdit';

export interface FlowNode {
  id: string;
  combo?: string;
  data: {
    label: string;
    summary: string;
    category: string;
    action: Action;
    originalId?: string;
    path: ActionPath;
  };
}

export interface FlowEdge {
  id: string;
  source: string;
  target: string;
  data?: {
    label?: string;
    dashed?: boolean;
  };
}

export interface FlowCombo {
  id: string;
  combo?: string;
  data: {
    label: string;
    category: string;
    type: string;
    summary?: string;
  };
}

export interface FlowData {
  nodes: FlowNode[];
  edges: FlowEdge[];
  combos: FlowCombo[];
}

const CATEGORY_MAP: Record<string, string> = {
  // interaction
  click: 'interaction',
  doubleClick: 'interaction',
  rightClick: 'interaction',
  middleClick: 'interaction',
  hover: 'interaction',
  hoverClick: 'interaction',
  moveMouse: 'interaction',
  pressAndHold: 'interaction',
  dragAndDrop: 'interaction',
  dragBy: 'interaction',
  slide: 'interaction',
  // input
  type: 'input',
  paste: 'input',
  clear: 'input',
  select: 'input',
  check: 'input',
  selectRadio: 'input',
  typeAndSelect: 'input',
  uploadFile: 'input',
  focus: 'input',
  blur: 'input',
  tabToNext: 'input',
  tabToPrevious: 'input',
  pressKey: 'input',
  keyCombination: 'input',
  // scroll
  scrollTo: 'scroll',
  scrollBy: 'scroll',
  scrollToBottom: 'scroll',
  scrollToTop: 'scroll',
  pageDown: 'scroll',
  pageUp: 'scroll',
  // navigation
  navigate: 'navigation',
  reload: 'navigation',
  goBack: 'navigation',
  goForward: 'navigation',
  setViewport: 'navigation',
  // wait
  waitFor: 'wait',
  waitForText: 'wait',
  waitForUrl: 'wait',
  waitForTimeout: 'wait',
  waitForElementHidden: 'wait',
  waitForElementVisible: 'wait',
  waitForNetworkIdle: 'wait',
  waitForFunction: 'wait',
  readPause: 'wait',
  // extract
  extract: 'extract',
  extractText: 'extract',
  extractAttribute: 'extract',
  extractHtml: 'extract',
  extractJson: 'extract',
  extractTable: 'extract',
  extractPageInfo: 'extract',
  screenshot: 'extract',
  captureRequest: 'extract',
  // transform
  transform: 'transform',
  filter: 'transform',
  deduplicate: 'transform',
  merge: 'transform',
  validateData: 'transform',
  saveSnapshot: 'transform',
  // ops
  checkpoint: 'ops',
  flushResults: 'ops',
  heartbeat: 'ops',
  cleanup: 'ops',
  circuitBreaker: 'ops',
  checkQuota: 'ops',
  setTag: 'ops',
  logMetric: 'ops',
  abort: 'ops',
  recover: 'ops',
  // flow
  if: 'flow',
  switch: 'flow',
  loop: 'flow',
  retry: 'flow',
  break: 'flow',
  continue: 'flow',
  exit: 'flow',
  group: 'flow',
  parallel: 'flow',
  sleep: 'flow',
  // page
  evaluate: 'page',
  setStyle: 'page',
  removeElement: 'page',
  blockRequest: 'page',
  unblockRequest: 'page',
  setCookie: 'page',
  getCookie: 'page',
  deleteCookie: 'page',
  setLocalStorage: 'page',
  getLocalStorage: 'page',
  removeLocalStorage: 'page',
  setSessionStorage: 'page',
  getSessionStorage: 'page',
  removeSessionStorage: 'page',
  setAttribute: 'page',
  removeAttribute: 'page',
  scrollIntoView: 'page',
  // browser
  openTab: 'browser',
  closeTab: 'browser',
  switchTab: 'browser',
  handleDialog: 'browser',
  handleDownload: 'browser',
  setUserAgent: 'browser',
  setExtraHeaders: 'browser',
  setLanguage: 'browser',
  setTimezone: 'browser',
  // auth
  refreshSession: 'auth',
  requestHuman: 'auth',
  solveCaptcha: 'auth',
  // output
  sendResult: 'output',
  sendLog: 'output',
  sendScreenshot: 'output',
  sendHtml: 'output',
  updateStatus: 'output',
  emitEvent: 'output',
};

const ACTION_LABELS: Record<string, string> = {
  click: '点击',
  doubleClick: '双击',
  rightClick: '右键点击',
  middleClick: '中键点击',
  hover: '悬停',
  hoverClick: '悬停后点击',
  moveMouse: '移动鼠标',
  pressAndHold: '长按',
  dragAndDrop: '拖拽',
  dragBy: '相对拖拽',
  slide: '滑动',
  type: '输入文本',
  paste: '粘贴',
  clear: '清空',
  select: '选择',
  check: '勾选',
  selectRadio: '单选',
  typeAndSelect: '输入并选择',
  uploadFile: '上传文件',
  focus: '聚焦',
  blur: '失焦',
  tabToNext: 'Tab 下一项',
  tabToPrevious: 'Tab 上一项',
  pressKey: '按键',
  keyCombination: '组合键',
  scrollTo: '滚动至元素',
  scrollBy: '相对滚动',
  scrollToBottom: '滚动到底部',
  scrollToTop: '滚动到顶部',
  pageDown: 'PageDown',
  pageUp: 'PageUp',
  navigate: '页面跳转',
  reload: '刷新页面',
  goBack: '后退',
  goForward: '前进',
  setViewport: '设置视口',
  waitFor: '等待元素',
  waitForText: '等待文本',
  waitForUrl: '等待 URL',
  waitForTimeout: '等待',
  waitForElementHidden: '等待元素隐藏',
  waitForElementVisible: '等待元素可见',
  waitForNetworkIdle: '等待网络空闲',
  waitForFunction: '等待函数',
  readPause: '阅读停顿',
  extract: '提取数据',
  extractText: '提取文本',
  extractAttribute: '提取属性',
  extractHtml: '提取 HTML',
  extractJson: '提取 JSON',
  extractTable: '提取表格',
  extractPageInfo: '提取页面信息',
  screenshot: '截图',
  captureRequest: '捕获请求',
  transform: '转换数据',
  filter: '过滤数据',
  deduplicate: '去重',
  merge: '合并数据',
  validateData: '校验数据',
  saveSnapshot: '保存快照',
  checkpoint: '检查点',
  flushResults: '刷新结果',
  heartbeat: '心跳',
  cleanup: '清理',
  circuitBreaker: '熔断器',
  checkQuota: '检查配额',
  setTag: '设置标签',
  logMetric: '记录指标',
  abort: '中止任务',
  recover: '恢复',
  if: '条件判断',
  switch: '分支判断',
  loop: '循环',
  retry: '重试',
  break: '跳出循环',
  continue: '继续循环',
  exit: '退出',
  group: '步骤组',
  parallel: '并行执行',
  sleep: '暂停',
  evaluate: '执行脚本',
  setStyle: '设置样式',
  removeElement: '移除元素',
  blockRequest: '阻断请求',
  unblockRequest: '解除阻断',
  setCookie: '设置 Cookie',
  getCookie: '读取 Cookie',
  deleteCookie: '删除 Cookie',
  setLocalStorage: '设置 LocalStorage',
  getLocalStorage: '读取 LocalStorage',
  removeLocalStorage: '移除 LocalStorage',
  setSessionStorage: '设置 SessionStorage',
  getSessionStorage: '读取 SessionStorage',
  removeSessionStorage: '移除 SessionStorage',
  setAttribute: '设置属性',
  removeAttribute: '移除属性',
  scrollIntoView: '滚动到视口',
  openTab: '打开标签页',
  closeTab: '关闭标签页',
  switchTab: '切换标签页',
  handleDialog: '处理弹窗',
  handleDownload: '处理下载',
  setUserAgent: '设置 UserAgent',
  setExtraHeaders: '设置请求头',
  setLanguage: '设置语言',
  setTimezone: '设置时区',
  refreshSession: '刷新会话',
  requestHuman: '请求人工处理',
  solveCaptcha: '识别验证码',
  sendResult: '上报结果',
  sendLog: '记录日志',
  sendScreenshot: '上报截图',
  sendHtml: '上报 HTML',
  updateStatus: '更新任务状态',
  emitEvent: '发送事件',
};

const HOOK_LABELS: Record<string, string> = {
  beforeAll: '前置钩子',
  afterAll: '后置钩子',
  onError: '异常处理',
  cleanup: '最终清理',
};

export function getActionCategory(actionType: ActionType): string {
  return CATEGORY_MAP[actionType] || 'unknown';
}

function getActionLabel(actionType: ActionType): string {
  return ACTION_LABELS[actionType] || actionType;
}

function truncate(str: string, max = 40): string {
  if (!str) return '';
  return str.length > max ? `${str.slice(0, max)}…` : str;
}

function targetName(target?: Target): string {
  if (!target) return '';
  if (target.$ref) return `{{${target.$ref}}}`;
  if (target.text) return `"${target.text}"`;
  if (target.selector) return truncate(target.selector);
  if (target.xpath) return truncate(target.xpath);
  if (target.ariaLabel) return `[aria-label=${target.ariaLabel}]`;
  if (target.role) return `[role=${target.role}]`;
  if (target.position) return `(${target.position.x}, ${target.position.y})`;
  return '';
}

function conditionSummary(condition?: Condition): string {
  if (!condition) return '';
  switch (condition.type) {
    case 'elementExists':
      return `元素存在 ${targetName(condition.target)}`;
    case 'elementNotExists':
      return `元素不存在 ${targetName(condition.target)}`;
    case 'elementVisible':
      return `元素可见 ${targetName(condition.target)}`;
    case 'elementHidden':
      return `元素隐藏 ${targetName(condition.target)}`;
    case 'textContains':
      return `文本包含 "${condition.text}"`;
    case 'textEquals':
      return `文本等于 "${condition.text}"`;
    case 'textMatches':
      return `文本匹配 ${condition.pattern}`;
    case 'urlContains':
      return `URL 包含 ${condition.pattern}`;
    case 'urlMatches':
      return `URL 匹配 ${condition.pattern}`;
    case 'valueEquals':
      return `值等于 ${condition.value}`;
    case 'jsTruthy':
      return `脚本为真`;
    case 'networkIdle':
      return '网络空闲';
    default:
      return condition.type;
  }
}

function valueString(value: unknown): string {
  if (value === undefined || value === null) return '';
  if (typeof value === 'string') return value;
  return JSON.stringify(value);
}

export function getActionSummary(action: Action): { label: string; summary: string; category: string } {
  const type = action.action;
  const category = getActionCategory(type);
  const label = getActionLabel(type);
  let summary = '';

  switch (type) {
    case 'click':
    case 'doubleClick':
    case 'rightClick':
    case 'middleClick':
    case 'hover':
    case 'focus':
    case 'blur':
    case 'scrollTo':
    case 'scrollIntoView':
    case 'removeElement':
    case 'setStyle':
      summary = targetName(action.target as Target);
      break;
    case 'hoverClick':
      summary = `${targetName(action.hoverTarget as Target)} → ${targetName(action.clickTarget as Target)}`;
      break;
    case 'moveMouse':
      summary = targetName(action.to as Target);
      break;
    case 'dragAndDrop':
      summary = `${targetName(action.source as Target)} → ${targetName(action.target as Target)}`;
      break;
    case 'dragBy':
      summary = `${targetName(action.source as Target)} Δ(${(action.delta as { x?: number; y?: number } | undefined)?.x}, ${(action.delta as { x?: number; y?: number } | undefined)?.y})`;
      break;
    case 'slide':
      summary = `${targetName(action.target as Target)} ${action.direction} ${action.distance}px`;
      break;
    case 'pressAndHold':
      summary = `${targetName(action.target as Target)} ${valueString(action.duration)}ms`;
      break;
    case 'type':
    case 'paste':
      summary = `“${truncate(valueString(action.value), 60)}” ${targetName(action.target as Target)}`;
      break;
    case 'clear':
      summary = targetName(action.target as Target);
      break;
    case 'select':
      summary = `${targetName(action.target as Target)} = ${valueString(action.value)}`;
      break;
    case 'check':
      summary = `${targetName(action.target as Target)} ${valueString(action.state)}`;
      break;
    case 'uploadFile':
      summary = `${targetName(action.target as Target)} (${(action.files as string[])?.length ?? 0} 个文件)`;
      break;
    case 'pressKey':
      summary = valueString(action.keys);
      break;
    case 'keyCombination':
      summary = ((action.keys as string[]) ?? []).join('+');
      break;
    case 'scrollBy':
      summary = [
        action.target ? targetName(action.target as Target) : '',
        `${action.direction} ${action.distance}${action.unit === 'pages' ? '页' : 'px'}`,
      ].filter(Boolean).join(' ');
      break;
    case 'scrollToBottom':
    case 'scrollToTop':
      summary = action.stepBy ? '分步' : '';
      break;
    case 'navigate':
    case 'reload':
      summary = truncate(valueString(action.url));
      break;
    case 'setViewport':
      summary = `${action.width}×${action.height}`;
      break;
    case 'waitFor':
    case 'waitForElementHidden':
    case 'waitForElementVisible':
      summary = targetName(action.target as Target);
      break;
    case 'waitForText':
      summary = `"${action.text}" ${targetName(action.target as Target)}`;
      break;
    case 'waitForUrl':
      summary = valueString(action.pattern);
      break;
    case 'waitForTimeout':
    case 'sleep':
    case 'readPause':
      summary = `${valueString(action.ms)}ms`;
      break;
    case 'waitForFunction':
      summary = truncate(valueString(action.script));
      break;
    case 'waitForNetworkIdle':
      summary = `${action.idleTime ?? ''}ms 空闲`;
      break;
    case 'extract':
    case 'extractText':
    case 'extractAttribute':
    case 'extractHtml':
    case 'extractJson':
    case 'extractTable':
    case 'extractPageInfo':
      summary = `→ ${action.name ?? ''} ${targetName(action.target as Target)}`;
      break;
    case 'screenshot':
      summary = `${action.name ?? ''} ${targetName(action.target as Target)}`;
      break;
    case 'captureRequest':
      summary = `${action.capture} ${valueString(action.urlPattern)}`;
      break;
    case 'transform':
    case 'filter':
    case 'deduplicate':
    case 'merge':
    case 'validateData':
      summary = `${action.from ?? ''} → ${action.name ?? ''}`;
      break;
    case 'saveSnapshot':
      summary = `${action.name ?? ''} (${action.type ?? 'html'})`;
      break;
    case 'flushResults':
      summary = action.awaitAck ? '等待确认' : '';
      break;
    case 'checkpoint':
      summary = action.name ? `名称：${action.name}` : '';
      break;
    case 'heartbeat':
      summary = action.payload ? truncate(JSON.stringify(action.payload)) : '';
      break;
    case 'logMetric':
      summary = `${action.name} = ${valueString(action.value)}`;
      break;
    case 'circuitBreaker':
      summary = `${action.name} (阈值 ${action.failureThreshold})`;
      break;
    case 'checkQuota':
      summary = `${action.type} ${action.limit}`;
      break;
    case 'abort':
      summary = action.reason ? `原因：${action.reason}` : '';
      break;
    case 'recover':
      summary = action.checkpointName ? `从 ${action.checkpointName} 恢复` : '';
      break;
    case 'if':
      summary = conditionSummary(action.condition as Condition);
      break;
    case 'switch':
      summary = valueString(action.expression);
      break;
    case 'loop':
      summary = `${action.type}${action.count ? ` ×${valueString(action.count)}` : ''}${action.as ? ` 作为 ${action.as}` : ''}`;
      break;
    case 'retry':
      summary = `${(action.config as { maxAttempts?: number } | undefined)?.maxAttempts ?? '?'} 次重试`;
      break;
    case 'parallel':
      summary = `${(action.steps as Action[])?.length ?? 0} 路并行`;
      break;
    case 'group':
      summary = `${(action.steps as Action[])?.length ?? 0} 步`;
      break;
    case 'cleanup':
      summary = `${(action.steps as Action[])?.length ?? 0} 步清理`;
      break;
    case 'exit':
      summary = `${action.status ?? ''}${action.message ? ` · ${action.message}` : ''}`;
      break;
    case 'evaluate':
      summary = action.name ? `→ ${action.name}` : truncate(valueString(action.script));
      break;
    case 'setCookie':
    case 'getCookie':
    case 'deleteCookie':
      summary = (action.name as string) ?? '';
      break;
    case 'setLocalStorage':
    case 'getLocalStorage':
    case 'removeLocalStorage':
    case 'setSessionStorage':
    case 'getSessionStorage':
    case 'removeSessionStorage':
      summary = (action.key as string) ?? '';
      break;
    case 'setAttribute':
    case 'removeAttribute':
      summary = `${action.attr as string} ${targetName(action.target as Target)}`;
      break;
    case 'openTab':
      summary = truncate(valueString(action.url));
      break;
    case 'switchTab':
      summary = valueString(action.to);
      break;
    case 'closeTab':
      summary = valueString(action.tabId);
      break;
    case 'handleDialog':
      summary = `${action.type} ${action.accept ? '接受' : '取消'}`;
      break;
    case 'handleDownload':
      summary = action.filename ? `保存为 ${action.filename}` : '';
      break;
    case 'setUserAgent':
      summary = truncate(valueString(action.userAgent));
      break;
    case 'setExtraHeaders':
      summary = Object.keys((action.headers as Record<string, string>) ?? {}).join(', ');
      break;
    case 'setLanguage':
    case 'setTimezone':
      summary = valueString(action.value);
      break;
    case 'requestHuman':
      summary = `${action.type as string}${(action.prompt as string | undefined) ? ` · ${truncate(action.prompt as string)}` : ''}`;
      break;
    case 'solveCaptcha':
      summary = targetName(action.target as Target);
      break;
    case 'sendResult':
      summary = action.immediate ? '立即上报' : '批量上报';
      break;
    case 'sendLog':
      summary = `[${action.level as string}] ${truncate(valueString(action.message as string))}`;
      break;
    case 'sendScreenshot':
    case 'sendHtml':
      summary = (action.name as string) ?? '';
      break;
    case 'updateStatus':
      summary = valueString(action.status);
      break;
    case 'emitEvent':
      summary = valueString(action.event);
      break;
    default:
      summary = action.description || '';
  }

  if (action.description && !summary) {
    summary = action.description;
  }

  return { label, summary, category };
}

interface WalkResult {
  entryIds: string[];
  exitIds: string[];
}

interface BuildContext {
  nodes: FlowNode[];
  edges: FlowEdge[];
  combos: FlowCombo[];
}

function makeId(action: Action, pathString: string): string {
  if (action.id) return `${pathString}::${action.id}`;
  return pathString;
}

function addEdge(ctx: BuildContext, source: string, target: string, label?: string, dashed?: boolean): void {
  if (!source || !target || source === target) return;
  const id = `e-${source}-${target}-${ctx.edges.length}`;
  ctx.edges.push({ id, source, target, data: { label, dashed } });
}

function createActionNode(
  ctx: BuildContext,
  action: Action,
  path: ActionPath,
  parentCombo?: string,
  idSuffix?: string,
): FlowNode {
  const { label, summary, category } = getActionSummary(action);
  const pathString = idSuffix ? `${pathToString(path)}::${idSuffix}` : pathToString(path);
  const node: FlowNode = {
    id: makeId(action, pathString),
    combo: parentCombo,
    data: {
      label,
      summary,
      category,
      action,
      originalId: action.id,
      path,
    },
  };
  ctx.nodes.push(node);
  return node;
}

function createCombo(ctx: BuildContext, id: string, label: string, type: string, parentCombo?: string, summary?: string): FlowCombo {
  const combo: FlowCombo = {
    id,
    combo: parentCombo,
    data: { label, category: type === 'hook' ? 'hook' : 'flow', type, summary },
  };
  ctx.combos.push(combo);
  return combo;
}

function walkActionList(
  ctx: BuildContext,
  actions: Action[] | undefined,
  path: ActionPath,
  parentCombo?: string,
): WalkResult {
  if (!actions || actions.length === 0) {
    return { entryIds: [], exitIds: [] };
  }

  let firstEntryIds: string[] | null = null;
  let previousExitIds: string[] = [];

  actions.forEach((action, index) => {
    const actionPath = [...path, index];
    const result = walkAction(ctx, action, actionPath, parentCombo);
    if (firstEntryIds === null) {
      firstEntryIds = result.entryIds;
    }
    previousExitIds.forEach((src) => {
      result.entryIds.forEach((tgt) => addEdge(ctx, src, tgt));
    });
    previousExitIds = result.exitIds;
  });

  return { entryIds: firstEntryIds ?? [], exitIds: previousExitIds };
}

function walkAction(ctx: BuildContext, action: Action, path: ActionPath, parentCombo?: string): WalkResult {
  const type = action.action;

  switch (type) {
    case 'if': {
      const comboId = pathToString(path);
      createCombo(ctx, comboId, '条件判断', 'if', parentCombo);
      const condNode = createActionNode(ctx, action, path, comboId, 'cond');

      const thenResult = walkActionList(ctx, action.then, [...path, 'then'], createCombo(ctx, `${comboId}::then`, '满足条件', 'branch', comboId).id);
      thenResult.entryIds.forEach((tgt) => addEdge(ctx, condNode.id, tgt, '是'));

      const elseResult = action.else?.length
        ? walkActionList(ctx, action.else, [...path, 'else'], createCombo(ctx, `${comboId}::else`, '不满足条件', 'branch', comboId).id)
        : { entryIds: [], exitIds: [] };
      elseResult.entryIds.forEach((tgt) => addEdge(ctx, condNode.id, tgt, '否'));

      const exitIds = [...thenResult.exitIds, ...elseResult.exitIds];
      return { entryIds: [condNode.id], exitIds: exitIds.length ? exitIds : [condNode.id] };
    }

    case 'switch': {
      const comboId = pathToString(path);
      createCombo(ctx, comboId, '分支判断', 'switch', parentCombo);
      const exprNode = createActionNode(ctx, action, path, comboId, 'expr');
      const exitIds: string[] = [];
      action.cases?.forEach((c, idx) => {
        const caseCombo = createCombo(ctx, `${pathToString(path)}::case-${idx}`, `等于 ${c.value}`, 'branch', comboId);
        const caseResult = walkActionList(ctx, c.steps, [...path, 'cases', idx, 'steps'], caseCombo.id);
        caseResult.entryIds.forEach((tgt) => addEdge(ctx, exprNode.id, tgt, String(c.value)));
        exitIds.push(...caseResult.exitIds);
      });
      if (action.default?.length) {
        const defaultCombo = createCombo(ctx, `${pathToString(path)}::default`, '默认', 'branch', comboId);
        const defaultResult = walkActionList(ctx, action.default, [...path, 'default'], defaultCombo.id);
        defaultResult.entryIds.forEach((tgt) => addEdge(ctx, exprNode.id, tgt, '默认'));
        exitIds.push(...defaultResult.exitIds);
      }
      return { entryIds: [exprNode.id], exitIds: exitIds.length ? exitIds : [exprNode.id] };
    }

    case 'loop': {
      const comboId = pathToString(path);
      const { label } = getActionSummary(action);
      createCombo(ctx, comboId, label, 'loop', parentCombo);
      const headNode = createActionNode(ctx, action, path, comboId, 'head');
      const bodyCombo = createCombo(ctx, `${comboId}::body`, '循环体', 'branch', comboId);
      const bodyResult = walkActionList(ctx, action.steps, [...path, 'steps'], bodyCombo.id);
      bodyResult.entryIds.forEach((tgt) => addEdge(ctx, headNode.id, tgt));
      bodyResult.exitIds.forEach((src) => addEdge(ctx, src, headNode.id, '继续循环', true));
      return { entryIds: [headNode.id], exitIds: [headNode.id] };
    }

    case 'group':
    case 'retry':
    case 'cleanup': {
      const comboId = pathToString(path);
      const { label } = getActionSummary(action);
      createCombo(ctx, comboId, label, type, parentCombo);
      return walkActionList(ctx, action.steps, [...path, 'steps'], comboId);
    }

    case 'parallel': {
      const comboId = pathToString(path);
      const { label } = getActionSummary(action);
      createCombo(ctx, comboId, label, 'parallel', parentCombo);
      const forkNode = createActionNode(ctx, action, path, comboId, 'fork');
      const joinNode: FlowNode = {
        id: `${pathToString(path)}::join`,
        combo: comboId,
        data: { label: '合并', summary: '并行结束', category: 'flow', action, path },
      };
      ctx.nodes.push(joinNode);

      const steps = action.steps ?? [];
      steps.forEach((child, idx) => {
        const childResult = walkAction(ctx, child, [...path, 'steps', idx], comboId);
        childResult.entryIds.forEach((tgt) => addEdge(ctx, forkNode.id, tgt));
        childResult.exitIds.forEach((src) => addEdge(ctx, src, joinNode.id));
      });

      return { entryIds: [forkNode.id], exitIds: [joinNode.id] };
    }

    case 'requestHuman': {
      const comboId = pathToString(path);
      const { label } = getActionSummary(action);
      createCombo(ctx, comboId, label, 'requestHuman', parentCombo);
      const node = createActionNode(ctx, action, path, comboId, 'node');
      if (action.then?.length) {
        const thenResult = walkActionList(ctx, action.then, [...path, 'then'], comboId);
        thenResult.entryIds.forEach((tgt) => addEdge(ctx, node.id, tgt));
        return { entryIds: [node.id], exitIds: thenResult.exitIds };
      }
      return { entryIds: [node.id], exitIds: [node.id] };
    }

    default: {
      const node = createActionNode(ctx, action, path, parentCombo);
      return { entryIds: [node.id], exitIds: [node.id] };
    }
  }
}

function walkHooks(ctx: BuildContext, hooks: RuleHooks): void {
  const entries: [string, Action[] | undefined][] = [
    ['beforeAll', hooks.beforeAll],
    ['afterAll', hooks.afterAll],
    ['onError', hooks.onError],
    ['cleanup', hooks.cleanup],
  ];
  entries.forEach(([key, actions]) => {
    if (!actions || actions.length === 0) return;
    const comboId = `hooks::${key}`;
    createCombo(ctx, comboId, HOOK_LABELS[key] || key, 'hook');
    walkActionList(ctx, actions, ['hooks', key], comboId);
  });
}

export function buildFlowData(rule: Rule): FlowData {
  const ctx: BuildContext = { nodes: [], edges: [], combos: [] };

  const startNode: FlowNode = {
    id: '__start__',
    data: { label: '开始', summary: '任务开始', category: 'start', action: { action: 'start' } as Action, path: [] },
  };
  const endNode: FlowNode = {
    id: '__end__',
    data: { label: '结束', summary: '任务结束', category: 'end', action: { action: 'end' } as Action, path: [] },
  };
  ctx.nodes.push(startNode, endNode);

  const mainResult = walkActionList(ctx, rule.steps, ['steps']);
  if (mainResult.entryIds.length) {
    mainResult.entryIds.forEach((tgt) => addEdge(ctx, startNode.id, tgt));
    mainResult.exitIds.forEach((src) => addEdge(ctx, src, endNode.id));
  } else {
    addEdge(ctx, startNode.id, endNode.id);
  }

  walkHooks(ctx, rule.hooks ?? {});

  return { nodes: ctx.nodes, edges: ctx.edges, combos: ctx.combos };
}

export function countNodes(data: FlowData): { nodes: number; edges: number; combos: number } {
  return {
    nodes: data.nodes.length,
    edges: data.edges.length,
    combos: data.combos.length,
  };
}
