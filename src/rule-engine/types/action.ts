/**
 * AegisCrawler Rule Engine - Action Type Definitions
 *
 * 全场景拟人化 Web 自动化动作 DSL。
 * 覆盖：鼠标、键盘、触屏手势、表单、滚动导航、等待观察、数据提取、
 *       流程控制、页面操作、浏览器级操作、人机协作、输出上报。
 */

// ==================== 通用类型 ====================

/** 元素定位描述 */
export interface Target {
  /** 指向规则顶层 `selectors` 中定义的别名，运行时解析为完整 Target */
  $ref?: string;
  /** CSS 选择器 */
  selector?: string;
  /** XPath 表达式 */
  xpath?: string;
  /** 按可见文本匹配 */
  text?: string;
  /** 按 aria-label 匹配 */
  ariaLabel?: string;
  /** 按 role + name 匹配 */
  role?: string;
  roleName?: string;
  /** 绝对或相对坐标 */
  position?: { x: number; y: number };
  /** iframe 定位：selector 或索引 */
  frame?: string | number;
  /** Shadow DOM 穿透路径 */
  shadowPath?: string[];
  /** 在多个匹配元素中取第 index 个（从 0 开始） */
  index?: number;
  /** 是否匹配多个元素 */
  multiple?: boolean;
  /** 查找超时（毫秒） */
  timeout?: number;
  /** 是否在视口内才认为找到 */
  visible?: boolean;
}

/** Named selector aliases keyed by their rule-local reference name. */
export interface TargetMap {
  [name: string]: Target;
}

/** 拟人化行为参数 */
export interface Humanize {
  /** 动作前随机等待 [min, max] ms */
  preDelay?: [number, number];
  /** 动作后随机等待 [min, max] ms */
  postDelay?: [number, number];
  /** 是否先移动鼠标到目标 */
  moveMouse?: boolean;
  /** 鼠标移动路径 */
  mousePath?: 'linear' | 'bezier' | 'random' | 'natural';
  /** 鼠标移动速度（像素/秒） */
  mouseSpeed?: [number, number];
  /** 点击位置随机偏移（像素） */
  randomOffset?: number | { x: number; y: number };
  /** 打字每个字符间隔 [min, max] ms */
  typingDelay?: [number, number];
  /** 按键按下→抬起间隔 [min, max] ms（仅 pressKey 生效） */
  keyDuration?: [number, number];
  /** 打字出错概率（0-1） */
  typingMistakeRate?: number;
  /** 是否模拟发现并纠正错字 */
  typingErrorCorrection?: boolean;
  /** 滚动速度（像素/秒） */
  scrollSpeed?: [number, number];
  /** 滚动分多少步完成 */
  scrollSteps?: number;
  /** 滚动间歇 [min, max] ms */
  scrollPause?: [number, number];
  /** 拖拽/滑动过程中的抖动幅度 */
  wobble?: number;
  /** 是否启用全套自然行为（由引擎根据动作类型自动组合） */
  natural?: boolean;
}

/** 重试配置 */
export interface RetryConfig {
  maxAttempts: number;
  delay?: number | [number, number];
  backoff?: 'fixed' | 'linear' | 'exponential';
  /** 只对指定错误类型重试 */
  on?: string[];
}

/** 条件表达式 */
export interface Condition {
  type:
    | 'elementExists'
    | 'elementNotExists'
    | 'elementVisible'
    | 'elementHidden'
    | 'textContains'
    | 'textEquals'
    | 'textMatches'
    | 'urlContains'
    | 'urlMatches'
    | 'valueEquals'
    | 'jsTruthy'
    | 'networkIdle';
  target?: Target;
  text?: string;
  pattern?: string;
  value?: string | number | boolean;
  script?: string;
  timeout?: number;
  /** For networkIdle: quiet period in ms (default 500). */
  idleTime?: number;
}

/** 错误分类，用于重试策略、告警和统计 */
export type ErrorType =
  | 'ElementNotFound'
  | 'ElementDisabled'
  | 'ElementNotVisible'
  | 'ElementVerificationFailed'
  | 'TimeoutError'
  | 'NavigationError'
  | 'NetworkError'
  | 'HttpError'
  | 'ValidationError'
  | 'AuthenticationError'
  | 'CaptchaError'
  | 'RateLimited'
  | 'Blocked'
  | 'SessionExpired'
  | 'PageCrashed'
  | 'ScriptError'
  | 'QuotaExceeded'
  | 'HumanTimeout'
  | 'UnsupportedInEnvironment'
  | 'UnknownError';

/** 所有 action 的公共字段 */
export interface BaseAction {
  /** 步骤 ID，可用于日志、重试、跳转 */
  id?: string;
  /** 动作类型 */
  action: ActionType;
  /** 人类可读的步骤说明 */
  description?: string;
  /** 单步超时（毫秒） */
  timeout?: number;
  /** 软超时：超过后发送一次 warn 日志但不影响执行（观察性指标，不触发重试/fallback） */
  softTimeout?: number;
  /** 硬超时：超过后强制终止并上报失败 */
  hardTimeout?: number;
  /** 重试次数或重试配置 */
  retry?: number | RetryConfig;
  /** 出错后的行为 */
  onError?: 'continue' | 'stop' | 'retry' | 'skip' | 'requestHuman';
  /** 该步骤是否为关键步骤；失败后是否触发快速失败（fail-fast） */
  critical?: boolean;
  /** 执行前是否打检查点，便于异常恢复 */
  checkpoint?: boolean;
  /** 执行条件 */
  condition?: Condition;
  /** 拟人化参数 */
  humanize?: Humanize;
  /** 用于日志、监控、计费的标签 */
  tags?: Record<string, string>;
  /**
   * 仅对 `evaluate` / `waitForFunction` 生效。`true` 表示作者已审核脚本内容、
   * 确认在目标域上安全执行；回放预检据此放行。其他动作忽略此字段。
   * 详见 docs/dsl-design.md §4 与 §15.1.1。
   */
  trusted?: boolean;
}

// ==================== 鼠标操作 ====================

export type MouseButton = 'left' | 'right' | 'middle';
export type ModifierKey = 'ctrl' | 'shift' | 'alt' | 'meta';

export interface ClickAction extends BaseAction {
  action: 'click';
  target: Target;
  button?: MouseButton;
  /** 修饰键 */
  modifiers?: ModifierKey[];
  /** 连击次数 */
  clickCount?: number;
}

export interface DoubleClickAction extends BaseAction {
  action: 'doubleClick';
  target: Target;
  button?: MouseButton;
  modifiers?: ModifierKey[];
}

export interface RightClickAction extends BaseAction {
  action: 'rightClick';
  target: Target;
  modifiers?: ModifierKey[];
}

export interface MiddleClickAction extends BaseAction {
  action: 'middleClick';
  target: Target;
}

export interface HoverAction extends BaseAction {
  action: 'hover';
  target: Target;
  /** 悬停持续时间（毫秒） */
  duration?: number | [number, number];
}

export interface HoverClickAction extends BaseAction {
  action: 'hoverClick';
  hoverTarget: Target;
  clickTarget: Target;
  /** 鼠标从 hoverTarget 移动到 clickTarget 的最大允许距离，超过则认为菜单关闭 */
  maxDistance?: number;
  /** 悬停后等待菜单出现的时间 */
  menuAppearTimeout?: number;
}

export interface MoveMouseAction extends BaseAction {
  action: 'moveMouse';
  to: Target | { x: number; y: number };
  /** 移动时长 [min, max] ms */
  duration?: number | [number, number];
  /** 路径类型 */
  path?: 'linear' | 'bezier' | 'random' | 'natural';
}

export interface PressAndHoldAction extends BaseAction {
  action: 'pressAndHold';
  target: Target;
  /** 按住时长 [min, max] ms */
  duration: number | [number, number];
  button?: MouseButton;
}

export interface DragAndDropAction extends BaseAction {
  action: 'dragAndDrop';
  source: Target;
  target: Target;
  /** 拖动时长 [min, max] ms */
  duration?: number | [number, number];
}

export interface DragByAction extends BaseAction {
  action: 'dragBy';
  source: Target;
  delta: { x: number; y: number };
  duration?: number | [number, number];
}

export interface SlideAction extends BaseAction {
  action: 'slide';
  target: Target;
  /** 滑动方向 */
  direction: 'left' | 'right' | 'up' | 'down';
  /** 滑动距离（像素） */
  distance: number;
  duration?: number | [number, number];
}

// ==================== 键盘与输入 ====================

export interface TypeAction extends BaseAction {
  action: 'type';
  target: Target;
  value: string;
  /** true=追加，false=先清空 */
  append?: boolean;
  /** 输入后是否提交（触发 Enter） */
  submit?: boolean;
}

export interface PasteAction extends BaseAction {
  action: 'paste';
  target: Target;
  value: string;
  /** 是否模拟从剪贴板粘贴行为（focus + paste 事件） */
  simulateClipboard?: boolean;
}

export interface ClearAction extends BaseAction {
  action: 'clear';
  target: Target;
  method?: 'selectAll' | 'backspace' | 'tripleClick' | 'focusSelectAll';
}

export interface SelectAction extends BaseAction {
  action: 'select';
  target: Target;
  value: string | number | string[];
  /** 匹配方式 */
  by?: 'value' | 'text' | 'index';
}

export interface CheckAction extends BaseAction {
  action: 'check';
  target: Target;
  /** true=选中，false=取消，toggle=切换 */
  state: boolean | 'toggle';
}

export interface SelectRadioAction extends BaseAction {
  action: 'selectRadio';
  target: Target;
}

export interface TypeAndSelectAction extends BaseAction {
  action: 'typeAndSelect';
  target: Target;
  value: string;
  /** 候选项选择器 */
  suggestionSelector: string;
  /** 匹配方式 */
  matchBy?: 'text' | 'contains' | 'startsWith';
  /** 输入后等待建议出现的时间 */
  waitForSuggestions?: number;
}

export interface UploadFileAction extends BaseAction {
  action: 'uploadFile';
  target: Target;
  files: string[];
}

export interface FocusAction extends BaseAction {
  action: 'focus';
  target: Target;
}

export interface BlurAction extends BaseAction {
  action: 'blur';
  target?: Target;
}

export interface TabAction extends BaseAction {
  action: 'tabToNext' | 'tabToPrevious';
  /** 按几次 Tab */
  count?: number;
}

export interface PressKeyAction extends BaseAction {
  action: 'pressKey';
  /** 按键序列，组合键写在一个数组里 */
  keys: string[] | string[][];
  /** 每个按键按下时长 [min, max] ms */
  duration?: number | [number, number];
}

export interface KeyCombinationAction extends BaseAction {
  action: 'keyCombination';
  keys: string[];
}

// ==================== 滚动与导航 ====================

export interface ScrollToAction extends BaseAction {
  action: 'scrollTo';
  target: Target;
  /** 滚动到元素时的对齐方式 */
  align?: 'start' | 'center' | 'end' | 'nearest';
}

export interface ScrollByAction extends BaseAction {
  action: 'scrollBy';
  /** 可选滚动容器；省略时滚动窗口 */
  target?: Target;
  direction: 'up' | 'down' | 'left' | 'right';
  distance: number;
  /** 距离单位，默认为 pixels */
  unit?: 'pixels' | 'pages';
}

export interface ScrollToBottomAction extends BaseAction {
  action: 'scrollToBottom';
  /** 是否分步滚动 */
  stepBy?: boolean;
}

export interface ScrollToTopAction extends BaseAction {
  action: 'scrollToTop';
}

export interface PageDownAction extends BaseAction {
  action: 'pageDown' | 'pageUp';
  count?: number;
}

export interface NavigateAction extends BaseAction {
  action: 'navigate';
  url: string;
  /** 是否等待页面加载完成 */
  waitUntil?: 'load' | 'domcontentloaded' | 'networkidle';
}

export interface ReloadAction extends BaseAction {
  action: 'reload';
  /** 是否强制刷新（忽略缓存） */
  force?: boolean;
  waitUntil?: 'load' | 'domcontentloaded' | 'networkidle';
}

export interface GoBackAction extends BaseAction {
  action: 'goBack' | 'goForward';
}

export interface SetViewportAction extends BaseAction {
  action: 'setViewport';
  width: number;
  height: number;
  deviceScaleFactor?: number;
  isMobile?: boolean;
  hasTouch?: boolean;
  userAgent?: string;
}

// ==================== 等待与观察 ====================

export interface WaitForAction extends BaseAction {
  action: 'waitFor';
  target: Target;
}

export interface WaitForTextAction extends BaseAction {
  action: 'waitForText';
  target: Target;
  text: string;
  matchType?: 'equals' | 'contains' | 'matches';
}

export interface WaitForUrlAction extends BaseAction {
  action: 'waitForUrl';
  pattern: string;
  matchType?: 'equals' | 'contains' | 'matches';
}

export interface WaitForTimeoutAction extends BaseAction {
  action: 'waitForTimeout';
  ms: number | [number, number];
}

export interface WaitForElementHiddenAction extends BaseAction {
  action: 'waitForElementHidden' | 'waitForElementVisible';
  target: Target;
}

export interface WaitForNetworkIdleAction extends BaseAction {
  action: 'waitForNetworkIdle';
  /** 多少毫秒内没有网络请求则认为 idle */
  idleTime?: number;
  timeout?: number;
}

export interface WaitForFunctionAction extends BaseAction {
  action: 'waitForFunction';
  script: string;
  /** 轮询间隔 */
  pollingInterval?: number;
}

export interface ReadPauseAction extends BaseAction {
  action: 'readPause';
  ms: number | [number, number];
  /** 是否伴随缓慢滚动 */
  scrollWhileReading?: boolean;
}

// ==================== 数据提取 ====================

export interface ExtractField {
  /** 字段名 */
  name?: string;
  /** 提取类型 */
  type: 'text' | 'html' | 'number' | 'boolean' | 'attr' | 'css' | 'json' | 'regex' | 'count' | 'exists';
  /** 元素选择器 */
  selector?: string;
  /** Require the selected node and its ancestors to be visibly rendered. */
  visible?: boolean;
  /** 属性名（type=attr 时使用） */
  attr?: string;
  /** CSS 属性名（type=css 时使用） */
  cssProperty?: string;
  /** 正则表达式（type=regex 或从文本中提取） */
  regex?: string;
  /** JSONPath（type=json 时使用） */
  path?: string;
  /** 默认值 */
  default?: any;
  /** 是否去除空白 */
  trim?: boolean;
  /** 转成绝对路径（对 attr=href/src 有效） */
  resolve?: boolean;
  /** 子字段（用于嵌套提取） */
  fields?: ExtractFieldMap;
  /** 该字段是否必填；提取失败且没有 default 时记录 error 但不中断 */
  required?: boolean;
  /** 仅当条件满足时才提取该字段 */
  condition?: Condition;
  /** 简单的字段级转换表达式，如 "value * 100"、"value.trim()" */
  transform?: string;
}

/**
 * Typed extraction fields keyed by the emitted field name.
 * @minProperties 1
 */
export interface ExtractFieldMap {
  [name: string]: ExtractField;
}

export interface ExtractAction extends BaseAction {
  action: 'extract';
  /** 结果变量名 */
  name: string;
  target: Target;
  /** 是否提取多个元素 */
  multiple?: boolean;
  fields: ExtractFieldMap;
  /** 提取为空时的 fallback 策略 */
  onEmpty?: 'skip' | 'sendEmpty' | 'retry' | 'fail';
  /** 用 JSON Schema 校验本次提取结果 */
  validate?: object;
}

export interface ExtractTextAction extends BaseAction {
  action: 'extractText';
  name: string;
  target: Target;
}

export interface ExtractAttributeAction extends BaseAction {
  action: 'extractAttribute';
  name: string;
  target: Target;
  attr: string;
}

export interface ExtractHtmlAction extends BaseAction {
  action: 'extractHtml';
  name: string;
  target: Target;
}

export interface ExtractJsonAction extends BaseAction {
  action: 'extractJson';
  name: string;
  target: Target;
  path?: string;
}

export interface ExtractTableAction extends BaseAction {
  action: 'extractTable';
  name: string;
  target: Target;
  /** 表头映射 */
  headers?: Record<string, string>;
  /** 是否包含表头 */
  includeHeader?: boolean;
}

export interface ExtractPageInfoAction extends BaseAction {
  action: 'extractPageInfo';
  name: string;
  fields: Record<string, { type: 'url' | 'title' | 'domain' | 'timestamp' | 'referrer' | 'userAgent' | 'viewport' }>;
}

export interface ScreenshotAction extends BaseAction {
  action: 'screenshot';
  name: string;
  target?: Target;
  type?: 'png' | 'jpeg';
  quality?: number;
  fullPage?: boolean;
}

export interface CaptureRequestAction extends BaseAction {
  action: 'captureRequest';
  name: string;
  /** 匹配 URL 模式 */
  urlPattern: string;
  /** 截取请求还是响应 */
  capture: 'request' | 'response' | 'both';
  /** 保留几个匹配结果 */
  limit?: number;
}

// ==================== 数据清洗与校验（核心） ====================

export interface TransformAction extends BaseAction {
  action: 'transform';
  /** 输入变量名 */
  from: string;
  /** 输出变量名 */
  name: string;
  /** 转换操作 */
  operations: Array<{
    /** 字段选择器，支持 JSONPath 风格，如 "items.*.price" */
    field?: string;
    /** 转换类型 */
    type: 'map' | 'filter' | 'regex' | 'replace' | 'trim' | 'number' | 'date' | 'jsonParse' | 'custom';
    /** 参数 */
    params?: Record<string, any>;
    /** 自定义脚本 */
    script?: string;
  }>;
}

export interface FilterAction extends BaseAction {
  action: 'filter';
  /** 输入变量名 */
  from: string;
  /** 输出变量名 */
  name: string;
  /** 过滤条件 */
  criteria: {
    /** 字段路径 */
    field?: string;
    /** 操作 */
    op: 'eq' | 'ne' | 'gt' | 'gte' | 'lt' | 'lte' | 'contains' | 'matches' | 'exists' | 'notEmpty';
    value?: any;
    /** 复杂条件用脚本 */
    script?: string;
  };
}

export interface DeduplicateAction extends BaseAction {
  action: 'deduplicate';
  /** 输入变量名 */
  from: string;
  /** 输出变量名 */
  name: string;
  /** 去重 key，支持多字段和模板 */
  keys: string[];
  /** 保留策略 */
  keep?: 'first' | 'last';
}

export interface MergeAction extends BaseAction {
  action: 'merge';
  /** 输入变量名列表 */
  from: string[];
  /** 输出变量名 */
  name: string;
  /** 合并策略 */
  strategy?: 'concat' | 'assign' | 'zip';
}

export interface ValidateDataAction extends BaseAction {
  action: 'validateData';
  /** 要校验的变量名 */
  from: string;
  /** JSON Schema */
  schema: object;
  /** 校验失败时的行为 */
  onInvalid?: 'warn' | 'fail';
}

export interface SaveSnapshotAction extends BaseAction {
  action: 'saveSnapshot';
  /** 快照名称 */
  name: string;
  /** 快照类型 */
  type?: 'html' | 'dom' | 'screenshot';
  /** 是否只保存 body */
  bodyOnly?: boolean;
}

// ==================== 生产级运维与恢复 ====================

export interface CheckpointAction extends BaseAction {
  action: 'checkpoint';
  /** 检查点名称 */
  name: string;
  /** 要持久化的变量列表 */
  preserve?: string[];
}

export interface FlushResultsAction extends BaseAction {
  action: 'flushResults';
  /** 是否阻塞等待发送完成 */
  awaitAck?: boolean;
  /** 发送超时 */
  timeout?: number;
}

export interface HeartbeatAction extends BaseAction {
  action: 'heartbeat';
  /** 心跳负载 */
  payload?: Record<string, any>;
}

export interface CleanupAction extends BaseAction {
  action: 'cleanup';
  /** 清理步骤，无论成功失败都会执行 */
  steps: Action[];
}

export interface CircuitBreakerAction extends BaseAction {
  action: 'circuitBreaker';
  /** 熔断器名称 */
  name: string;
  /** 错误阈值 */
  failureThreshold: number;
  /** 窗口大小（毫秒） */
  windowMs: number;
  /** 熔断后冷却时间 */
  cooldownMs: number;
  /** 熔断触发后的动作 */
  onOpen?: Action[];
  /**
   * 省略时仅检查门禁；true 报告一次失败；false 确认半开探测成功。
   * 关闭状态下的 false 为无操作。
   */
  record?: boolean;
}

export interface CheckQuotaAction extends BaseAction {
  action: 'checkQuota';
  /** 配额类型 */
  type: 'requestsPerMinute' | 'requestsPerHour' | 'dataVolume' | 'custom';
  /** 上限 */
  limit: number;
  /** 超出时的行为 */
  onExceeded?: 'wait' | 'fail' | 'skip';
  /** 等待冷却时间 */
  cooldownMs?: number;
}

export interface SetTagAction extends BaseAction {
  action: 'setTag';
  /** Legacy log metadata only; never appended to collection result payloads. */
  tags: Record<string, string>;
  /** Declared for compatibility; scope enforcement and resume are deferred. */
  scope?: 'step' | 'task' | 'global';
}

export interface LogMetricAction extends BaseAction {
  action: 'logMetric';
  name: string;
  /** 指标值；支持数字或模板变量 */
  value: number | string;
  unit?: string;
  tags?: Record<string, string>;
}

export interface AbortAction extends BaseAction {
  action: 'abort';
  /** 退出原因 */
  reason?: string;
  /** 是否先 flush 已采数据 */
  flushBeforeAbort?: boolean;
}

export interface RecoverAction extends BaseAction {
  action: 'recover';
  /** 从哪个 checkpoint 恢复 */
  checkpointName?: string;
  /** 恢复后执行的步骤 */
  steps?: Action[];
}

// ==================== 流程控制 ====================

export interface IfAction extends BaseAction {
  action: 'if';
  condition: Condition;
  then: Action[];
  else?: Action[];
}

export interface SwitchAction extends BaseAction {
  action: 'switch';
  /** 判断表达式，返回字符串 */
  expression: string;
  cases: Array<{ value: string; steps: Action[] }>;
  default?: Action[];
}

export interface LoopAction extends BaseAction {
  action: 'loop';
  type: 'fixedCount' | 'whileElementExists' | 'whileElementNotExists' | 'whileCondition' | 'forEach';
  /** fixedCount 时使用，支持数字或模板变量 */
  count?: number | string;
  /** whileElementExists / whileElementNotExists 时使用 */
  target?: Target;
  /** whileCondition 时使用 */
  condition?: Condition;
  /** forEach 时使用，指向一个数组变量 */
  items?: string;
  /** 循环变量名 */
  as?: string;
  /** 最大迭代次数，防止死循环 */
  maxIterations?: number;
  steps: Action[];
}

export interface RetryAction extends BaseAction {
  action: 'retry';
  steps: Action[];
  config: RetryConfig;
}

export interface BreakAction extends BaseAction {
  action: 'break' | 'continue';
  /** 跳出多层循环时指定 label */
  label?: string;
}

export interface ExitAction extends BaseAction {
  action: 'exit';
  /** 退出状态 */
  status?: 'success' | 'failure' | 'cancelled';
  message?: string;
}

export interface GroupAction extends BaseAction {
  action: 'group';
  steps: Action[];
}

export interface ParallelAction extends BaseAction {
  action: 'parallel';
  steps: Action[];
  /** 最大并发数 */
  maxConcurrency?: number;
  /** 是否任一失败就中止 */
  failFast?: boolean;
}

export interface SleepAction extends BaseAction {
  action: 'sleep';
  ms: number | [number, number];
}

// ==================== 页面级操作 ====================

export interface EvaluateAction extends BaseAction {
  action: 'evaluate';
  /** 在页面上下文执行的脚本 */
  script: string;
  /** 结果变量名 */
  name?: string;
  /** 传递给脚本的参数 */
  args?: any[];
  /**
   * 来源标记（遥测用）。录制器把 `executeJavascript` 事件转换为 `evaluate` 动作时
   * 自动写入 `source: 'recorded'`。不参与预检放行决策——放行只看 `trusted`。
   * 详见 docs/dsl-design.md §15.1.1。
   */
  source?: 'recorded' | string;
}

export interface SetStyleAction extends BaseAction {
  action: 'setStyle';
  target: Target;
  style: Record<string, string>;
}

export interface RemoveElementAction extends BaseAction {
  action: 'removeElement';
  target: Target;
}

export interface BlockRequestAction extends BaseAction {
  action: 'blockRequest' | 'unblockRequest';
  /** URL 匹配模式 */
  urlPattern: string | string[];
  /** 阻断的资源类型 */
  resourceTypes?: string[];
}

export interface CookieAction extends BaseAction {
  action: 'setCookie' | 'getCookie' | 'deleteCookie';
  name: string;
  value?: string;
  domain?: string;
  path?: string;
  expires?: number;
}

export interface LocalStorageAction extends BaseAction {
  action: 'setLocalStorage' | 'getLocalStorage' | 'removeLocalStorage' | 'setSessionStorage' | 'getSessionStorage' | 'removeSessionStorage';
  key: string;
  value?: any;
}

export interface SetAttributeAction extends BaseAction {
  action: 'setAttribute' | 'removeAttribute';
  target: Target;
  attr: string;
  value?: string;
}

export interface ScrollIntoViewAction extends BaseAction {
  action: 'scrollIntoView';
  target: Target;
  align?: 'start' | 'center' | 'end' | 'nearest';
}

// ==================== 浏览器级操作 ====================

export interface OpenTabAction extends BaseAction {
  action: 'openTab';
  url: string;
  /** 是否切换到新标签 */
  active?: boolean;
}

export interface CloseTabAction extends BaseAction {
  action: 'closeTab';
  /** 空表示当前标签 */
  tabId?: number | 'current';
}

export interface SwitchTabAction extends BaseAction {
  action: 'switchTab';
  /** 标签索引或 URL 匹配 */
  to: number | { urlPattern: string } | 'last' | 'next' | 'previous';
}

export interface HandleDialogAction extends BaseAction {
  action: 'handleDialog';
  type: 'alert' | 'confirm' | 'prompt';
  /** 对 confirm/prompt 是否接受 */
  accept: boolean;
  /** prompt 输入值 */
  text?: string;
}

export interface HandleDownloadAction extends BaseAction {
  action: 'handleDownload';
  /** 下载触发动作 */
  trigger: Action;
  /** 保存文件名 */
  filename?: string;
  /** 保存路径 */
  path?: string;
}

export interface SetUserAgentAction extends BaseAction {
  action: 'setUserAgent';
  userAgent: string;
}

export interface SetExtraHeadersAction extends BaseAction {
  action: 'setExtraHeaders';
  headers: Record<string, string>;
}

export interface SetLanguageAction extends BaseAction {
  action: 'setLanguage' | 'setTimezone';
  value: string;
}

// ==================== 认证、会话与人机协作 ====================

export interface RefreshSessionAction extends BaseAction {
  action: 'refreshSession';
  /** 刷新方式 */
  method?: 'reload' | 'requestNewToken' | 'evaluate';
  script?: string;
}

export interface RequestHumanAction extends BaseAction {
  action: 'requestHuman';
  /** 需要人类处理的任务类型 */
  type: 'captcha' | '2fa' | 'confirmation' | 'generic';
  /** 提示信息 */
  prompt?: string;
  /** 等待人类处理的最大时长 */
  timeout?: number;
  /** 人类处理完成后继续执行的步骤 */
  then?: Action[];
}

export interface SolveCaptchaAction extends BaseAction {
  action: 'solveCaptcha';
  /** 验证码图片/iframe 目标 */
  target: Target;
  /** 使用哪个打码服务或本地模型 */
  provider?: string;
  /** 是否请求人类 fallback */
  fallbackToHuman?: boolean;
}

// ==================== 输出与上报 ====================

export interface SendResultAction extends BaseAction {
  action: 'sendResult';
  payload: Record<string, any>;
  /** 是否立即发送，false 则缓存到任务结束时批量发送 */
  immediate?: boolean;
}

export interface SendLogAction extends BaseAction {
  action: 'sendLog';
  level: 'debug' | 'info' | 'warn' | 'error';
  message: string;
  extra?: Record<string, any>;
}

export interface SendScreenshotAction extends BaseAction {
  action: 'sendScreenshot';
  target?: Target;
  name?: string;
}

export interface SendHtmlAction extends BaseAction {
  action: 'sendHtml';
  target?: Target;
  name?: string;
}

export interface UpdateStatusAction extends BaseAction {
  action: 'updateStatus';
  status: 'pending' | 'running' | 'done' | 'failed' | 'paused' | 'waitingForHuman';
  message?: string;
}

export interface EmitEventAction extends BaseAction {
  action: 'emitEvent';
  event: string;
  payload?: Record<string, any>;
}

// ==================== Action 联合类型 ====================

export type Action =
  | ClickAction
  | DoubleClickAction
  | RightClickAction
  | MiddleClickAction
  | HoverAction
  | HoverClickAction
  | MoveMouseAction
  | PressAndHoldAction
  | DragAndDropAction
  | DragByAction
  | SlideAction
  | TypeAction
  | PasteAction
  | ClearAction
  | SelectAction
  | CheckAction
  | SelectRadioAction
  | TypeAndSelectAction
  | UploadFileAction
  | FocusAction
  | BlurAction
  | TabAction
  | PressKeyAction
  | KeyCombinationAction
  | ScrollToAction
  | ScrollByAction
  | ScrollToBottomAction
  | ScrollToTopAction
  | PageDownAction
  | NavigateAction
  | ReloadAction
  | GoBackAction
  | SetViewportAction
  | WaitForAction
  | WaitForTextAction
  | WaitForUrlAction
  | WaitForTimeoutAction
  | WaitForElementHiddenAction
  | WaitForNetworkIdleAction
  | WaitForFunctionAction
  | ReadPauseAction
  | ExtractAction
  | ExtractTextAction
  | ExtractAttributeAction
  | ExtractHtmlAction
  | ExtractJsonAction
  | ExtractTableAction
  | ExtractPageInfoAction
  | ScreenshotAction
  | CaptureRequestAction
  | TransformAction
  | FilterAction
  | DeduplicateAction
  | MergeAction
  | ValidateDataAction
  | SaveSnapshotAction
  | CheckpointAction
  | FlushResultsAction
  | HeartbeatAction
  | CleanupAction
  | CircuitBreakerAction
  | CheckQuotaAction
  | SetTagAction
  | LogMetricAction
  | AbortAction
  | RecoverAction
  | IfAction
  | SwitchAction
  | LoopAction
  | RetryAction
  | BreakAction
  | ExitAction
  | GroupAction
  | ParallelAction
  | SleepAction
  | EvaluateAction
  | SetStyleAction
  | RemoveElementAction
  | BlockRequestAction
  | CookieAction
  | LocalStorageAction
  | SetAttributeAction
  | ScrollIntoViewAction
  | OpenTabAction
  | CloseTabAction
  | SwitchTabAction
  | HandleDialogAction
  | HandleDownloadAction
  | SetUserAgentAction
  | SetExtraHeadersAction
  | SetLanguageAction
  | RefreshSessionAction
  | RequestHumanAction
  | SolveCaptchaAction
  | SendResultAction
  | SendLogAction
  | SendScreenshotAction
  | SendHtmlAction
  | UpdateStatusAction
  | EmitEventAction;

export type ActionType = Action['action'];

// ==================== 规则文件 ====================

export interface Rule {
  /** 规则唯一标识 */
  id: string;
  /** 规则版本 */
  version: string;
  /** 规则名称 */
  name: string;
  /** 匹配域名 */
  domain: string | string[];
  /** 匹配 URL 模式 */
  urlPattern?: string | string[];
  /** 是否启用 */
  enabled?: boolean;
  /** 优先级 */
  priority?: number;
  /** 入口 URL 模板 */
  entry?: string;
  /** 变量默认值 */
  variables?: Record<string, any>;
  /** 选择器别名 */
  selectors?: TargetMap;
  /** 全局 humanize */
  humanize?: Humanize;
  /** 全局重试 */
  retry?: number | RetryConfig;
  /** 失败时是否截图 */
  screenshotOnError?: boolean;
  /** 期望输出数据结构（JSON Schema），用于校验最终结果 */
  output?: object;
  /** output 校验失败的处理策略，默认 'fail' */
  outputOnInvalid?: 'warn' | 'fail';
  /** 数据发送策略 */
  sendPolicy?: {
    /** 批量发送阈值 */
    batchSize?: number;
    /** 定时 flush 周期 ms（setInterval），仅在 batchSize 启用缓冲时生效 */
    flushInterval?: number;
    /** 是否失败也发送已采数据 */
    sendOnFailure?: boolean;
  };
  /** 规则级总超时（毫秒） */
  timeout?: number;
  /** 规则级重试 */
  maxRetries?: number;
  /** 生命周期钩子 */
  hooks?: {
    /** 任务开始前执行（如登录、预热） */
    beforeAll?: Action[];
    /** 任务结束后执行（如登出、清理） */
    afterAll?: Action[];
    /** 任意步骤失败时执行 */
    onError?: Action[];
    /** 最终清理，无论成功失败都执行 */
    cleanup?: Action[];
  };
  /** 标签，用于分组、监控、权限 */
  tags?: Record<string, string>;
  /** 负责人 */
  owner?: string;
  /** 创建/更新时间 */
  createdAt?: string;
  updatedAt?: string;
  /** 动作序列 */
  steps: Action[];
}

export interface RuleRegistry {
  version: string;
  rules: Rule[];
}
