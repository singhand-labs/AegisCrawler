import { useEffect, useMemo, useState } from 'react';
import {
  Alert,
  Button,
  Card,
  Input,
  InputNumber,
  Select,
  Space,
  Switch,
  Tag,
} from 'antd';
import type { Action, ActionType, Condition, Humanize, Target } from '@/types/rule';
import { getActionSummary } from '../utils/ruleToGraph';
import TargetInput from './inputs/TargetInput';
import ConditionInput from './inputs/ConditionInput';
import HumanizeInput from './inputs/HumanizeInput';

export interface ActionEditorPanelProps {
  action: Action;
  onChange: (action: Action) => void;
  onCancel?: () => void;
  disabled?: boolean;
}

const TARGET_ONLY_TYPES: ActionType[] = [
  'click',
  'doubleClick',
  'rightClick',
  'middleClick',
  'hover',
  'focus',
  'blur',
  'scrollTo',
  'scrollIntoView',
  'removeElement',
  'setStyle',
];

const VALUE_TARGET_TYPES: ActionType[] = ['type', 'paste'];

const INPUT_OPTION_TYPES: ActionType[] = [
  'clear',
  'select',
  'check',
  'selectRadio',
  'typeAndSelect',
  'uploadFile',
];

const URL_TYPES: ActionType[] = ['navigate', 'reload', 'openTab'];

const WAIT_TARGET_TIMEOUT_TYPES: ActionType[] = [
  'waitFor',
  'waitForElementHidden',
  'waitForElementVisible',
];

const MS_TYPES: ActionType[] = ['waitForTimeout', 'sleep'];

const EXTRACT_TYPES: ActionType[] = [
  'extract',
  'extractText',
  'extractAttribute',
  'extractHtml',
  'extractJson',
  'extractTable',
  'extractPageInfo',
];

const CONTAINER_TYPES: ActionType[] = ['if', 'switch', 'loop', 'group', 'retry', 'cleanup', 'parallel'];

const COVERED_TYPES: ActionType[] = [
  ...TARGET_ONLY_TYPES,
  ...VALUE_TARGET_TYPES,
  ...INPUT_OPTION_TYPES,
  ...URL_TYPES,
  ...WAIT_TARGET_TIMEOUT_TYPES,
  'waitForText',
  ...MS_TYPES,
  'waitForUrl',
  'waitForFunction',
  ...EXTRACT_TYPES,
  'screenshot',
  'evaluate',
  ...CONTAINER_TYPES,
];

const LOOP_TYPE_OPTIONS: { value: string; label: string }[] = [
  { value: 'while', label: '当条件满足（while）' },
  { value: 'until', label: '直到条件满足（until）' },
  { value: 'count', label: '固定次数（count）' },
  { value: 'forEach', label: '遍历（forEach）' },
];

function isCovered(type: ActionType): boolean {
  return COVERED_TYPES.includes(type);
}

function tagsToString(tags?: Record<string, string>): string {
  if (!tags) return '';
  return Object.entries(tags)
    .map(([k, v]) => `${k}=${v}`)
    .join('\n');
}

interface ParsedTags {
  tags?: Record<string, string>;
  error?: string;
}

function parseTags(input: string): ParsedTags {
  const lines = input.split(/\r?\n/);
  const tags: Record<string, string> = {};
  for (const line of lines) {
    const trimmed = line.trim();
    if (!trimmed) continue;
    const index = trimmed.indexOf('=');
    if (index === -1) {
      return { error: `标签行格式错误：${trimmed}` };
    }
    const key = trimmed.slice(0, index).trim();
    const value = trimmed.slice(index + 1).trim();
    if (!key) {
      return { error: '标签键不能为空' };
    }
    tags[key] = value;
  }
  return { tags };
}

function stringifyAction(action: Action): string {
  return JSON.stringify(action, null, 2);
}

function isTruthy<T>(value: T | undefined | null | '' | false): value is T {
  return Boolean(value);
}

export default function ActionEditorPanel({
  action,
  onChange,
  onCancel,
  disabled,
}: ActionEditorPanelProps) {
  const [initialAction, setInitialAction] = useState<Action>(action);
  const [draft, setDraft] = useState<Action>(action);
  const [isJson, setIsJson] = useState<boolean>(!isCovered(action.action));
  const [jsonString, setJsonString] = useState<string>(stringifyAction(action));
  const [jsonError, setJsonError] = useState<string | undefined>();
  const [tagsString, setTagsString] = useState<string>(tagsToString(action.tags));
  const [tagsError, setTagsError] = useState<string | undefined>();

  const { label } = useMemo(() => getActionSummary(draft), [draft]);

  // Reset internal state whenever the selected action changes.
  // We use JSON.stringify(action) as the effect identity so that value-equal
  // action objects (e.g. updated by the parent with the same id and type) still
  // trigger a refresh, while avoiding re-render loops that a raw object
  // dependency could cause when parents do not memoize the action prop.
  useEffect(() => {
    setInitialAction(action);
    setDraft(action);
    setIsJson(!isCovered(action.action));
    setJsonString(stringifyAction(action));
    setJsonError(undefined);
    setTagsString(tagsToString(action.tags));
    setTagsError(undefined);
  }, [JSON.stringify(action)]);

  const update = (patch: Partial<Action>) => {
    // Safe cast: Action extends Record<string, unknown>, so spreading a
    // Partial<Action> over the previous full Action preserves the full shape.
    setDraft((prev) => ({ ...prev, ...patch } as Action));
  };

  const updateTarget = (target: Target | undefined) => update({ target });
  const updateCondition = (condition: Condition | undefined) => update({ condition });
  const updateHumanize = (humanize: Humanize | undefined) => update({ humanize });

  const handleTagsChange = (value: string) => {
    setTagsString(value);
    if (!value.trim()) {
      setTagsError(undefined);
      update({ tags: undefined });
      return;
    }
    const parsed = parseTags(value);
    if (parsed.error) {
      setTagsError(parsed.error);
      return;
    }
    setTagsError(undefined);
    update({ tags: parsed.tags });
  };

  const handleJsonChange = (value: string) => {
    setJsonString(value);
    try {
      JSON.parse(value);
      setJsonError(undefined);
    } catch (err) {
      // Safe cast: JSON.parse throws a native Error instance.
      setJsonError(`JSON 格式错误：${(err as Error).message}`);
    }
  };

  const handleToggleJson = () => {
    if (isJson) {
      // Switching from JSON back to form is only possible for covered types.
      if (!isCovered(draft.action)) return;
      try {
        // Safe cast: parsing only succeeds here after JSON validation, and the
        // user is intentionally converting the JSON payload back to form mode.
        const parsed = JSON.parse(jsonString) as Action;
        // M-8: restore keys that were explicitly undefined in the previous
        // draft. JSON.stringify strips them, so a no-edit round-trip through
        // JSON mode would otherwise lose those keys, drifting the draft from
        // the original action's key set.
        const restored: Record<string, unknown> = { ...parsed };
        const prevDraft = draft as Record<string, unknown>;
        for (const key of Object.keys(prevDraft)) {
          if (prevDraft[key] === undefined && !(key in restored)) {
            restored[key] = undefined;
          }
        }
        setDraft(restored as Action);
        setTagsString(tagsToString(parsed.tags));
        setTagsError(undefined);
        setJsonError(undefined);
        setIsJson(false);
      } catch {
        // Parsing error already shown; stay in JSON mode.
      }
    } else {
      setJsonString(stringifyAction(draft));
      setJsonError(undefined);
      setIsJson(true);
    }
  };

  const handleReset = () => {
    setDraft(initialAction);
    setIsJson(!isCovered(initialAction.action));
    setJsonString(stringifyAction(initialAction));
    setJsonError(undefined);
    setTagsString(tagsToString(initialAction.tags));
    setTagsError(undefined);
  };

  const handleApply = () => {
    if (isJson) {
      try {
        // Safe cast: the apply button is disabled while JSON is invalid, so
        // reaching here means JSON.parse will succeed.
        const parsed = JSON.parse(jsonString) as Action;
        onChange(parsed);
      } catch {
        // Invalid JSON cannot be submitted because the button is disabled.
      }
    } else {
      onChange(draft);
    }
  };

  const applyDisabled =
    disabled || Boolean(tagsError) || (isJson && Boolean(jsonError));

  const renderContainerHint = () => (
    <Alert
      message="子步骤请在流程图中编辑"
      description="该动作为容器类型，仅可编辑自身参数。"
      type="info"
      showIcon
    />
  );

  const renderTargetField = () => (
    // Wrapping TargetInput in a <label> associates the "目标元素" caption with
    // the entire target control group for accessibility.
    <label>
      目标元素
      <TargetInput value={draft.target} onChange={updateTarget} disabled={disabled} />
    </label>
  );

  const renderValueField = (valueKey: 'value' | 'state' | 'files') => {
    if (valueKey === 'state') {
      return (
        <div>
          <label htmlFor="action-state">勾选状态</label>
          <Switch
            id="action-state"
            checked={Boolean(draft.state)}
            onChange={(checked) => update({ state: checked })}
            disabled={disabled}
            checkedChildren="是"
            unCheckedChildren="否"
          />
        </div>
      );
    }

    if (valueKey === 'files') {
      // Safe cast: uploadFile actions store file paths as string[].
      const files = (draft.files as string[] | undefined) ?? [];
      return (
        <div>
          <label htmlFor="action-files">文件路径（逗号分隔）</label>
          <Input
            id="action-files"
            value={files.join(', ')}
            placeholder="例如 /tmp/a.png, /tmp/b.png"
            disabled={disabled}
            onChange={(e) =>
              update({
                files: e.target.value
                  .split(',')
                  .map((f) => f.trim())
                  .filter(isTruthy),
              })
            }
          />
        </div>
      );
    }

    return (
      <div>
        <label htmlFor="action-value">输入内容</label>
        <Input.TextArea
          id="action-value"
          // Safe cast: in this branch `value` is the user-typed string payload.
          value={String((draft.value as string | undefined) ?? '')}
          rows={3}
          disabled={disabled}
          onChange={(e) => update({ value: e.target.value })}
        />
      </div>
    );
  };

  const renderTypeFields = () => {
    const type = draft.action;

    if (TARGET_ONLY_TYPES.includes(type)) {
      return renderTargetField();
    }

    if (VALUE_TARGET_TYPES.includes(type)) {
      return (
        <Space direction="vertical" style={{ width: '100%' }}>
          {renderTargetField()}
          {renderValueField('value')}
        </Space>
      );
    }

    if (type === 'clear') {
      return renderTargetField();
    }

    if (['select', 'selectRadio', 'typeAndSelect'].includes(type)) {
      return (
        <Space direction="vertical" style={{ width: '100%' }}>
          {renderTargetField()}
          {renderValueField('value')}
        </Space>
      );
    }

    if (type === 'check') {
      return (
        <Space direction="vertical" style={{ width: '100%' }}>
          {renderTargetField()}
          {renderValueField('state')}
        </Space>
      );
    }

    if (type === 'uploadFile') {
      return (
        <Space direction="vertical" style={{ width: '100%' }}>
          {renderTargetField()}
          {renderValueField('files')}
        </Space>
      );
    }

    if (URL_TYPES.includes(type)) {
      return (
        <div>
          <label htmlFor="action-url">URL</label>
          <Input
            id="action-url"
            // Safe cast: URL-type actions use `url` as a string.
            value={String((draft.url as string | undefined) ?? '')}
            placeholder="例如 https://example.com"
            disabled={disabled}
            onChange={(e) => update({ url: e.target.value })}
          />
        </div>
      );
    }

    if (WAIT_TARGET_TIMEOUT_TYPES.includes(type)) {
      return (
        <Space direction="vertical" style={{ width: '100%' }}>
          {renderTargetField()}
          <div>
            <label htmlFor="action-wait-timeout">等待超时（毫秒）</label>
            <InputNumber
              id="action-wait-timeout"
              style={{ width: '100%' }}
              value={draft.timeout}
              placeholder="可选"
              disabled={disabled}
              onChange={(v) => update({ timeout: v === null ? undefined : v })}
            />
          </div>
        </Space>
      );
    }

    if (type === 'waitForText') {
      return (
        <Space direction="vertical" style={{ width: '100%' }}>
          {renderTargetField()}
          <div>
            <label htmlFor="action-text">等待文本</label>
            <Input
              id="action-text"
              // Safe cast: waitForText stores the expected text as a string.
              value={String((draft.text as string | undefined) ?? '')}
              disabled={disabled}
              onChange={(e) => update({ text: e.target.value })}
            />
          </div>
        </Space>
      );
    }

    if (MS_TYPES.includes(type)) {
      return (
        <div>
          <label htmlFor="action-ms">等待时间（毫秒）</label>
          <InputNumber
            id="action-ms"
            style={{ width: '100%' }}
            // Safe cast: sleep/waitForTimeout actions use `ms` as a number.
            value={(draft.ms as number | undefined) ?? draft.timeout}
            disabled={disabled}
            onChange={(v) => update({ ms: v === null ? undefined : v })}
          />
        </div>
      );
    }

    if (type === 'waitForUrl') {
      return (
        <div>
          <label htmlFor="action-pattern">匹配规则</label>
          <Input
            id="action-pattern"
            // Safe cast: waitForUrl stores the URL pattern as a string.
            value={String((draft.pattern as string | undefined) ?? '')}
            disabled={disabled}
            onChange={(e) => update({ pattern: e.target.value })}
          />
        </div>
      );
    }

    if (type === 'waitForFunction') {
      return (
        <div>
          <label htmlFor="action-wait-script">脚本</label>
          <Input.TextArea
            id="action-wait-script"
            rows={4}
            // Safe cast: waitForFunction stores the predicate script as a string.
            value={String((draft.script as string | undefined) ?? '')}
            disabled={disabled}
            onChange={(e) => update({ script: e.target.value })}
          />
        </div>
      );
    }

    if (EXTRACT_TYPES.includes(type)) {
      return (
        <Space direction="vertical" style={{ width: '100%' }}>
          {renderTargetField()}
          <div>
            <label htmlFor="action-name">字段名</label>
            <Input
              id="action-name"
              // Safe cast: extract actions use `name` as the output field string.
              value={String((draft.name as string | undefined) ?? '')}
              disabled={disabled}
              onChange={(e) => update({ name: e.target.value })}
            />
          </div>
          {type === 'extractAttribute' && (
            <div>
              <label htmlFor="action-attribute">属性名</label>
              <Input
                id="action-attribute"
                // Safe cast: extractAttribute stores the attribute name as a string.
                value={String((draft.attribute as string | undefined) ?? '')}
                disabled={disabled}
                onChange={(e) => update({ attribute: e.target.value })}
              />
            </div>
          )}
          {type === 'extractTable' && (
            <div>
              <label htmlFor="action-headers">表头（逗号分隔）</label>
              <Input
                id="action-headers"
                // Safe cast: extractTable stores the headers expression as a string.
                value={String((draft.headers as string | undefined) ?? '')}
                disabled={disabled}
                onChange={(e) => update({ headers: e.target.value })}
              />
            </div>
          )}
        </Space>
      );
    }

    if (type === 'screenshot') {
      return (
        <Space direction="vertical" style={{ width: '100%' }}>
          {renderTargetField()}
          <div>
            <label htmlFor="action-name">截图名称</label>
            <Input
              id="action-name"
              // Safe cast: screenshot uses `name` as the screenshot name string.
              value={String((draft.name as string | undefined) ?? '')}
              disabled={disabled}
              onChange={(e) => update({ name: e.target.value })}
            />
          </div>
        </Space>
      );
    }

    if (type === 'evaluate') {
      return (
        <Space direction="vertical" style={{ width: '100%' }}>
          <div>
            <label htmlFor="action-eval-script">脚本</label>
            <Input.TextArea
              id="action-eval-script"
              rows={4}
              // Safe cast: evaluate stores the JS snippet as a string.
              value={String((draft.script as string | undefined) ?? '')}
              disabled={disabled}
              onChange={(e) => update({ script: e.target.value })}
            />
          </div>
          <div>
            <label htmlFor="action-name">结果字段名</label>
            <Input
              id="action-name"
              // Safe cast: evaluate uses `name` as the result field string.
              value={String((draft.name as string | undefined) ?? '')}
              disabled={disabled}
              onChange={(e) => update({ name: e.target.value })}
            />
          </div>
        </Space>
      );
    }

    if (type === 'if') {
      return (
        <Space direction="vertical" style={{ width: '100%' }}>
          <div>
            <label>分支条件</label>
            <ConditionInput
              value={draft.condition}
              onChange={updateCondition}
              disabled={disabled}
            />
          </div>
          {renderContainerHint()}
        </Space>
      );
    }

    if (type === 'loop') {
      // Safe cast: loop actions use `type` to choose the loop mode.
      const loopType = (draft.type as string | undefined) || 'count';
      return (
        <Space direction="vertical" style={{ width: '100%' }}>
          <div>
            <label htmlFor="action-loop-type">循环类型</label>
            <Select
              id="action-loop-type"
              style={{ width: '100%' }}
              value={loopType}
              options={LOOP_TYPE_OPTIONS}
              disabled={disabled}
              onChange={(v) => update({ type: v })}
            />
          </div>
          {loopType === 'count' && (
            <div>
              <label htmlFor="action-count">循环次数</label>
              <InputNumber
                id="action-count"
                style={{ width: '100%' }}
                // Safe cast: count loops store the iteration count as a number.
                value={(draft.count as number | undefined) ?? 1}
                min={0}
                disabled={disabled}
                onChange={(v) => update({ count: v === null ? undefined : v })}
              />
            </div>
          )}
          {loopType === 'forEach' && (
            <>
              {renderTargetField()}
              <div>
                <label htmlFor="action-as">迭代变量名</label>
                <Input
                  id="action-as"
                  // Safe cast: forEach loops store the iteration variable name as a string.
                  value={String((draft.as as string | undefined) ?? '')}
                  disabled={disabled}
                  onChange={(e) => update({ as: e.target.value })}
                />
              </div>
            </>
          )}
          {(loopType === 'while' || loopType === 'until') && (
            <div>
              <label>循环条件</label>
              <ConditionInput
                value={draft.condition}
                onChange={updateCondition}
                disabled={disabled}
              />
            </div>
          )}
          {renderContainerHint()}
        </Space>
      );
    }

    if (['switch', 'group', 'retry', 'cleanup', 'parallel'].includes(type)) {
      return renderContainerHint();
    }

    return null;
  };

  const renderCommonFields = () => {
    // Safe cast: loop actions use `type` to choose the loop mode.
    const loopType = (draft.type as string | undefined) || 'count';
    const hasOwnCondition =
      draft.action === 'if' || (draft.action === 'loop' && (loopType === 'while' || loopType === 'until'));

    return (
      <Space direction="vertical" style={{ width: '100%' }}>
        {!hasOwnCondition && (
          <div>
            <label>前置条件</label>
            <ConditionInput
              value={draft.condition}
              onChange={updateCondition}
              disabled={disabled}
            />
          </div>
        )}
        {/*
          For `if` and `loop` (while/until), the generic precondition editor is
          intentionally hidden because those action types already expose the
          same `condition` field as their native branch/loop condition. Editing
          both would duplicate the same data, so the native condition serves as
          the precondition for these actions.
        */}
        {hasOwnCondition && (
          <Alert
            message="前置条件已合并"
            description="该动作的类型专用条件（分支条件 / 循环条件）同时作为前置条件，无需重复配置。"
            type="info"
            showIcon
          />
        )}
        <HumanizeInput value={draft.humanize} onChange={updateHumanize} disabled={disabled} />
      <div>
        <label htmlFor="action-tags">
          标签（每行 key=value）
        </label>
        <Input.TextArea
          id="action-tags"
          rows={3}
          value={tagsString}
          placeholder="env=prod&#10;group=main"
          disabled={disabled}
          onChange={(e) => handleTagsChange(e.target.value)}
        />
        {tagsError && <Alert message={tagsError} type="error" showIcon style={{ marginTop: 8 }} />}
      </div>
    </Space>
  );
  };

  const renderJsonEditor = () => (
    <Space direction="vertical" style={{ width: '100%' }}>
      <label htmlFor="action-json">JSON 配置</label>
      <Input.TextArea
        id="action-json"
        rows={12}
        value={jsonString}
        disabled={disabled}
        onChange={(e) => handleJsonChange(e.target.value)}
      />
      {jsonError && <Alert message={jsonError} type="error" showIcon />}
    </Space>
  );

  return (
    <Card
      title={
        <Space>
          <span>{label}</span>
          <Tag>{draft.action}</Tag>
        </Space>
      }
      extra={
        <Space>
          {isCovered(draft.action) && (
            <Button autoInsertSpace={false} onClick={handleToggleJson} disabled={disabled}>
              {isJson ? '表单编辑' : 'JSON 编辑'}
            </Button>
          )}
          <Button autoInsertSpace={false} onClick={handleReset} disabled={disabled}>
            重置
          </Button>
          <Button autoInsertSpace={false} onClick={onCancel} disabled={disabled}>
            取消
          </Button>
          <Button autoInsertSpace={false} type="primary" onClick={handleApply} disabled={applyDisabled}>
            应用
          </Button>
        </Space>
      }
    >
      <Space direction="vertical" style={{ width: '100%' }}>
        <div>
          <label htmlFor="action-id">步骤 ID</label>
          <Input
            id="action-id"
            value={draft.id ?? ''}
            placeholder="可选"
            disabled={disabled}
            onChange={(e) => update({ id: e.target.value || undefined })}
          />
        </div>

        <div>
          <label htmlFor="action-description">说明</label>
          <Input.TextArea
            id="action-description"
            rows={2}
            value={draft.description ?? ''}
            disabled={disabled}
            onChange={(e) => update({ description: e.target.value || undefined })}
          />
        </div>

        <div>
          <label htmlFor="action-timeout">超时（毫秒）</label>
          <InputNumber
            id="action-timeout"
            style={{ width: '100%' }}
            value={draft.timeout}
            placeholder="可选"
            disabled={disabled}
            onChange={(v) => update({ timeout: v === null ? undefined : v })}
          />
        </div>

        <div>
          <label htmlFor="action-critical">关键步骤</label>
          <Switch
            id="action-critical"
            checked={draft.critical ?? false}
            onChange={(checked) => update({ critical: checked })}
            disabled={disabled}
            checkedChildren="是"
            unCheckedChildren="否"
          />
        </div>

        <div>
          <label htmlFor="action-checkpoint">设为检查点</label>
          <Switch
            id="action-checkpoint"
            checked={draft.checkpoint ?? false}
            onChange={(checked) => update({ checkpoint: checked })}
            disabled={disabled}
            checkedChildren="是"
            unCheckedChildren="否"
          />
        </div>

        {isJson ? renderJsonEditor() : (
          <>
            {renderTypeFields()}
            {renderCommonFields()}
          </>
        )}
      </Space>
    </Card>
  );
}
