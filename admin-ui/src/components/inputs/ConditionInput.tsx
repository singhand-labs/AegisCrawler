import { useMemo } from 'react';
import { Select, Input, InputNumber, Switch, Button, Space, Radio } from 'antd';
import type { Condition } from '@/types/rule';
import TargetInput from './TargetInput';

type ValueType = 'string' | 'number' | 'boolean';

function isValueType(v: unknown): v is ValueType {
  return v === 'string' || v === 'number' || v === 'boolean';
}

const CONDITION_TYPES: { value: string; label: string }[] = [
  { value: 'elementExists', label: '元素存在' },
  { value: 'elementNotExists', label: '元素不存在' },
  { value: 'elementVisible', label: '元素可见' },
  { value: 'elementHidden', label: '元素隐藏' },
  { value: 'textContains', label: '文本包含' },
  { value: 'textEquals', label: '文本等于' },
  { value: 'textMatches', label: '文本匹配' },
  { value: 'urlContains', label: 'URL 包含' },
  { value: 'urlMatches', label: 'URL 匹配' },
  { value: 'valueEquals', label: '值等于' },
  { value: 'jsTruthy', label: '脚本为真' },
  { value: 'networkIdle', label: '网络空闲' },
];

const TARGET_TYPES = ['elementExists', 'elementNotExists', 'elementVisible', 'elementHidden'];
// H-4: textMatches uses condition.pattern (the executor reads
// executor-utils.ts:394 `condition.pattern`), not condition.text. Writing to
// `text` made the executor build RegExp(undefined) which matches everything.
const TEXT_TYPES = ['textContains', 'textEquals'];
const PATTERN_TYPES = ['textMatches', 'urlContains', 'urlMatches'];
const VALUE_TYPES = ['valueEquals'];
const SCRIPT_TYPES = ['jsTruthy'];

interface ValueFieldProps {
  value?: string | number | boolean;
  onChange: (value: string | number | boolean | undefined) => void;
  disabled?: boolean;
}

function ValueField({ value, onChange, disabled }: ValueFieldProps) {
  const mode: ValueType = useMemo(() => {
    if (typeof value === 'boolean') return 'boolean';
    if (typeof value === 'number') return 'number';
    return 'string';
  }, [value]);

  const handleModeChange = (newMode: ValueType) => {
    const defaults: Record<ValueType, string | number | boolean> = {
      string: '',
      number: 0,
      boolean: false,
    };
    onChange(defaults[newMode]);
  };

  return (
    <Space direction="vertical" style={{ width: '100%' }}>
      <Radio.Group
        value={mode}
        onChange={(e) => {
          const v = e.target.value;
          if (isValueType(v)) handleModeChange(v);
        }}
        disabled={disabled}
        optionType="button"
        buttonStyle="solid"
      >
        <Radio.Button value="string">字符串</Radio.Button>
        <Radio.Button value="number">数字</Radio.Button>
        <Radio.Button value="boolean">布尔</Radio.Button>
      </Radio.Group>

      {mode === 'string' && (
        <Input
          value={typeof value === 'string' ? value : undefined}
          placeholder="输入字符串"
          disabled={disabled}
          onChange={(e) => onChange(e.target.value)}
        />
      )}
      {mode === 'number' && (
        <InputNumber
          value={typeof value === 'number' ? value : undefined}
          placeholder="输入数字"
          disabled={disabled}
          onChange={(v) => onChange(v ?? 0)}
        />
      )}
      {mode === 'boolean' && (
        <Switch
          checked={typeof value === 'boolean' ? value : false}
          onChange={(v) => onChange(v)}
          disabled={disabled}
          checkedChildren="是"
          unCheckedChildren="否"
        />
      )}
    </Space>
  );
}

export interface ConditionInputProps {
  value?: Condition;
  onChange: (value: Condition | undefined) => void;
  disabled?: boolean;
}

function patchCondition(value: Condition | undefined, patch: Partial<Condition>): Condition {
  // Spreading a possibly-undefined value leaves `type` potentially missing in TS's eyes,
  // so we assert to Condition; the caller always provides the required `type` when needed.
  return { ...value, ...patch } as Condition;
}

export default function ConditionInput({ value, onChange, disabled }: ConditionInputProps) {
  const type = value?.type ?? '';

  return (
    <Space direction="vertical" style={{ width: '100%' }}>
      <label htmlFor="condition-type">条件类型</label>
      <Select
        id="condition-type"
        value={type || undefined}
        placeholder="选择条件类型"
        disabled={disabled}
        options={CONDITION_TYPES}
        onChange={(t) => onChange(patchCondition(value, { type: t }))}
      />

      {TARGET_TYPES.includes(type) && (
        <TargetInput
          value={value?.target}
          onChange={(t) => onChange(patchCondition(value, { target: t }))}
          disabled={disabled}
        />
      )}

      {TEXT_TYPES.includes(type) && (
        <Input
          value={value?.text}
          placeholder="输入文本"
          disabled={disabled}
          onChange={(e) => onChange(patchCondition(value, { text: e.target.value }))}
        />
      )}

      {PATTERN_TYPES.includes(type) && (
        <Input
          value={value?.pattern}
          placeholder="输入匹配规则"
          disabled={disabled}
          onChange={(e) => onChange(patchCondition(value, { pattern: e.target.value }))}
        />
      )}

      {VALUE_TYPES.includes(type) && (
        <ValueField
          value={value?.value}
          onChange={(v) => onChange(patchCondition(value, { value: v }))}
          disabled={disabled}
        />
      )}

      {SCRIPT_TYPES.includes(type) && (
        <Input.TextArea
          value={value?.script}
          placeholder="返回布尔值的脚本"
          disabled={disabled}
          rows={3}
          onChange={(e) => onChange(patchCondition(value, { script: e.target.value }))}
        />
      )}

      <label htmlFor="condition-timeout">超时（毫秒）</label>
      <InputNumber
        id="condition-timeout"
        value={value?.timeout}
        placeholder="可选"
        disabled={disabled}
        onChange={(v) => onChange(patchCondition(value, { timeout: v ?? undefined }))}
      />

      <Button autoInsertSpace={false} onClick={() => onChange(undefined)} disabled={disabled || !value}>
        清空
      </Button>
    </Space>
  );
}
