import { Card, Space, InputNumber, Select, Switch, Button, Radio } from 'antd';
import type { Humanize } from '@/types/rule';

interface RangeInputProps {
  label: string;
  value?: [number, number];
  onChange: (value: [number, number]) => void;
  disabled?: boolean;
}

function RangeInput({ label, value, onChange, disabled }: RangeInputProps) {
  const from = value?.[0];
  const to = value?.[1];

  const handleChange = (index: 0 | 1, num: number | null) => {
    const next: [number, number] = index === 0 ? [num ?? 0, to ?? 0] : [from ?? 0, num ?? 0];
    onChange(next);
  };

  return (
    <div>
      <label>{label}</label>
      <Space>
        <InputNumber
          value={from}
          placeholder="最小值"
          disabled={disabled}
          onChange={(v) => handleChange(0, v)}
        />
        <span>~</span>
        <InputNumber
          value={to}
          placeholder="最大值"
          disabled={disabled}
          onChange={(v) => handleChange(1, v)}
        />
      </Space>
    </div>
  );
}

interface RandomOffsetFieldProps {
  value?: Humanize['randomOffset'];
  onChange: (value: Humanize['randomOffset']) => void;
  disabled?: boolean;
}

function RandomOffsetField({ value, onChange, disabled }: RandomOffsetFieldProps) {
  const enabled = value !== undefined;

  return (
    <Space direction="vertical">
      <label>随机偏移</label>
      <Switch
        checked={enabled}
        onChange={(checked) => onChange(checked ? 0 : undefined)}
        disabled={disabled}
      />
      {enabled && (
        <>
          <Radio.Group
            value={typeof value === 'object' ? 'xy' : 'number'}
            onChange={(e) => onChange(e.target.value === 'xy' ? { x: 0, y: 0 } : 0)}
            disabled={disabled}
          >
            <Radio value="number">单一数值</Radio>
            <Radio value="xy">XY 坐标</Radio>
          </Radio.Group>
          {typeof value === 'number' ? (
            <InputNumber
              value={value}
              disabled={disabled}
              onChange={(v) => onChange(v ?? 0)}
            />
          ) : (
            <Space>
              <InputNumber
                value={value.x}
                placeholder="X"
                disabled={disabled}
                onChange={(v) =>
                  onChange({
                    ...value,
                    x: v ?? 0,
                  })
                }
              />
              <InputNumber
                value={value.y}
                placeholder="Y"
                disabled={disabled}
                onChange={(v) =>
                  onChange({
                    ...value,
                    y: v ?? 0,
                  })
                }
              />
            </Space>
          )}
        </>
      )}
    </Space>
  );
}

export interface HumanizeInputProps {
  value?: Humanize;
  onChange: (value: Humanize | undefined) => void;
  disabled?: boolean;
}

function updateHumanize<K extends keyof Humanize>(
  value: Humanize | undefined,
  key: K,
  fieldValue: Humanize[K]
): Humanize {
  // Generic computed-property key cannot be inferred as a keyof Humanize entry,
  // so we assert the spread result to Humanize.
  return { ...(value || {}), [key]: fieldValue } as Humanize;
}

const MOUSE_PATH_OPTIONS: { value: Humanize['mousePath']; label: string }[] = [
  { value: 'linear', label: '线性' },
  { value: 'bezier', label: '贝塞尔' },
  { value: 'random', label: '随机' },
  { value: 'natural', label: '自然' },
];

export default function HumanizeInput({ value, onChange, disabled }: HumanizeInputProps) {
  const updateField = <K extends keyof Humanize>(key: K, fieldValue: Humanize[K]) => {
    onChange(updateHumanize(value, key, fieldValue));
  };

  return (
    <Card
      title="拟人化参数"
      extra={
        <Button autoInsertSpace={false} onClick={() => onChange(undefined)} disabled={disabled || !value}>
          清空
        </Button>
      }
    >
      <Space direction="vertical" style={{ width: '100%' }}>
        <RangeInput
          label="操作前延迟（毫秒）"
          value={value?.preDelay}
          onChange={(v) => updateField('preDelay', v)}
          disabled={disabled}
        />
        <RangeInput
          label="操作后延迟（毫秒）"
          value={value?.postDelay}
          onChange={(v) => updateField('postDelay', v)}
          disabled={disabled}
        />
        <RangeInput
          label="输入延迟（毫秒）"
          value={value?.typingDelay}
          onChange={(v) => updateField('typingDelay', v)}
          disabled={disabled}
        />
        <RangeInput
          label="鼠标速度"
          value={value?.mouseSpeed}
          onChange={(v) => updateField('mouseSpeed', v)}
          disabled={disabled}
        />
        <RangeInput
          label="滚动速度"
          value={value?.scrollSpeed}
          onChange={(v) => updateField('scrollSpeed', v)}
          disabled={disabled}
        />
        <RangeInput
          label="滚动暂停（毫秒）"
          value={value?.scrollPause}
          onChange={(v) => updateField('scrollPause', v)}
          disabled={disabled}
        />

        <div>
          <label>移动鼠标</label>
          <Switch
            checked={value?.moveMouse ?? false}
            onChange={(v) => updateField('moveMouse', v)}
            disabled={disabled}
          />
        </div>

        <div>
          <label>鼠标路径</label>
          <Select
            value={value?.mousePath}
            placeholder="选择鼠标路径"
            options={MOUSE_PATH_OPTIONS}
            disabled={disabled}
            onChange={(v) => updateField('mousePath', v)}
          />
        </div>

        <RandomOffsetField
          value={value?.randomOffset}
          onChange={(v) => updateField('randomOffset', v)}
          disabled={disabled}
        />

        <div>
          <label>输入错误率（0-1）</label>
          <InputNumber
            min={0}
            max={1}
            step={0.01}
            value={value?.typingMistakeRate}
            disabled={disabled}
            onChange={(v) => updateField('typingMistakeRate', v ?? 0)}
          />
        </div>

        <div>
          <label>自动纠错</label>
          <Switch
            checked={value?.typingErrorCorrection ?? false}
            onChange={(v) => updateField('typingErrorCorrection', v)}
            disabled={disabled}
          />
        </div>

        <div>
          <label>滚动步数</label>
          <InputNumber
            value={value?.scrollSteps}
            disabled={disabled}
            onChange={(v) => updateField('scrollSteps', v ?? 0)}
          />
        </div>

        <div>
          <label>摆动幅度</label>
          <InputNumber
            value={value?.wobble}
            disabled={disabled}
            onChange={(v) => updateField('wobble', v ?? 0)}
          />
        </div>

        <div>
          <label>自然模式</label>
          <Switch
            checked={value?.natural ?? false}
            onChange={(v) => updateField('natural', v)}
            disabled={disabled}
          />
        </div>
      </Space>
    </Card>
  );
}
