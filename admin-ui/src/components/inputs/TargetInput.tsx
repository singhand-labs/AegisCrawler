import { useMemo } from 'react';
import { Radio, Input, InputNumber, Button, Space, Row, Col } from 'antd';
import type { Target } from '@/types/rule';

type TargetMode = 'selector' | 'xpath' | 'text' | 'ariaLabel' | 'role' | 'position' | '$ref';

const MODE_LABELS: Record<TargetMode, string> = {
  selector: 'CSS 选择器',
  xpath: 'XPath',
  text: '文本',
  ariaLabel: 'aria-label',
  role: 'role',
  position: '坐标',
  $ref: '引用',
};

const MODE_PLACEHOLDERS: Record<TargetMode, string> = {
  selector: '例如 .item .title',
  xpath: '例如 //div[@class="item"]',
  text: '例如 提交',
  ariaLabel: '例如 搜索',
  role: '例如 button',
  position: '',
  $ref: '例如 mainForm',
};

export interface TargetInputProps {
  value?: Target;
  onChange: (value: Target | undefined) => void;
  disabled?: boolean;
}

// Mode-specific fields; everything else (frame, shadowPath, index, etc.) is preserved
// when the active mode changes.
type TargetModeField = '$ref' | 'selector' | 'xpath' | 'text' | 'ariaLabel' | 'role' | 'position';

function preserveTargetFields(value: Target | undefined): Omit<Target, TargetModeField> {
  if (!value) return {};
  const { $ref, selector, xpath, text, ariaLabel, role, position, ...preserved } = value;
  return preserved;
}

function targetForMode(mode: TargetMode, base: Target | undefined): Target {
  const preserved = preserveTargetFields(base);
  if (mode === 'position') {
    return { ...preserved, position: { x: 0, y: 0 } };
  }
  return updateTargetString(base, mode, '');
}

function updateTargetString(
  value: Target | undefined,
  mode: Exclude<TargetMode, 'position'>,
  inputValue: string
): Target {
  const preserved = preserveTargetFields(value);
  switch (mode) {
    case 'selector':
      return { ...preserved, selector: inputValue };
    case 'xpath':
      return { ...preserved, xpath: inputValue };
    case 'text':
      return { ...preserved, text: inputValue };
    case 'ariaLabel':
      return { ...preserved, ariaLabel: inputValue };
    case 'role':
      return { ...preserved, role: inputValue };
    case '$ref':
      return { ...preserved, $ref: inputValue };
  }
}

function updateTargetPosition(value: Target | undefined, axis: 'x' | 'y', num: number): Target {
  const preserved = preserveTargetFields(value);
  return {
    ...preserved,
    position: {
      ...(value?.position ?? { x: 0, y: 0 }),
      [axis]: num,
    },
  };
}

function getStringValue(value: Target | undefined, mode: Exclude<TargetMode, 'position'>): string | undefined {
  switch (mode) {
    case 'selector':
      return value?.selector;
    case 'xpath':
      return value?.xpath;
    case 'text':
      return value?.text;
    case 'ariaLabel':
      return value?.ariaLabel;
    case 'role':
      return value?.role;
    case '$ref':
      return value?.$ref;
  }
}

function isTargetMode(v: unknown): v is TargetMode {
  // `v in MODE_LABELS` narrows the Ant Design Radio.Group value to a known TargetMode key.
  return typeof v === 'string' && v in MODE_LABELS;
}

export default function TargetInput({ value, onChange, disabled }: TargetInputProps) {
  const mode = useMemo<TargetMode | undefined>(() => {
    if (!value) {
      return undefined;
    }
    if (value.$ref !== undefined) return '$ref';
    if (value.selector !== undefined) return 'selector';
    if (value.xpath !== undefined) return 'xpath';
    if (value.text !== undefined) return 'text';
    if (value.ariaLabel !== undefined) return 'ariaLabel';
    if (value.role !== undefined) return 'role';
    if (value.position !== undefined) return 'position';
    return undefined;
  }, [value]);

  const handleModeChange = (newMode: TargetMode) => {
    onChange(targetForMode(newMode, value));
  };

  const handleStringChange = (field: Exclude<TargetMode, 'position'>, inputValue: string) => {
    onChange(updateTargetString(value, field, inputValue));
  };

  const handlePositionChange = (axis: 'x' | 'y', num: number | null) => {
    onChange(updateTargetPosition(value, axis, num ?? 0));
  };

  const radioOptions: { label: string; value: TargetMode }[] = Object.entries(MODE_LABELS).map(([k, label]) => ({
    label,
    // `Object.entries` returns `string` keys; we narrow to TargetMode because the keys are drawn from MODE_LABELS.
    value: k as TargetMode,
  }));

  return (
    <Space direction="vertical" style={{ width: '100%' }}>
      <Radio.Group
        value={mode}
        onChange={(e) => {
          const v = e.target.value;
          if (isTargetMode(v)) {
            handleModeChange(v);
          }
        }}
        disabled={disabled}
        optionType="button"
        buttonStyle="solid"
        options={radioOptions}
      />

      {mode && mode !== 'position' && (
        <Input
          id={`target-${mode}-field`}
          value={getStringValue(value, mode)}
          placeholder={MODE_PLACEHOLDERS[mode]}
          disabled={disabled}
          onChange={(e) => handleStringChange(mode, e.target.value)}
        />
      )}

      {mode === 'position' && (
        <Row gutter={8}>
          <Col>
            <label htmlFor="target-pos-x">X</label>
            <InputNumber
              id="target-pos-x"
              value={value?.position?.x}
              disabled={disabled}
              onChange={(v) => handlePositionChange('x', v)}
            />
          </Col>
          <Col>
            <label htmlFor="target-pos-y">Y</label>
            <InputNumber
              id="target-pos-y"
              value={value?.position?.y}
              disabled={disabled}
              onChange={(v) => handlePositionChange('y', v)}
            />
          </Col>
        </Row>
      )}

      <Button autoInsertSpace={false} onClick={() => onChange(undefined)} disabled={disabled || !value}>
        清空
      </Button>
    </Space>
  );
}
