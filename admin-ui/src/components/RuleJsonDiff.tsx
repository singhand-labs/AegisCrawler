import { diffJson } from 'diff';

interface Props {
  baseline: object;
  enhanced: object;
}

export default function RuleJsonDiff({ baseline, enhanced }: Props) {
  const changes = diffJson(baseline, enhanced);
  return (
    <pre className="rule-json-diff">
      {changes.map((part, i) => (
        <span
          key={i}
          className={part.added ? 'diff-add' : part.removed ? 'diff-remove' : 'diff-neutral'}
        >
          {part.value}
        </span>
      ))}
    </pre>
  );
}
