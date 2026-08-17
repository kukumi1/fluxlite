import { useState } from "react";

interface Props {
  value: number;
  onChange: (value: number) => void;
  required?: boolean;
}

/**
 * 数字输入框，允许输入过程中出现空值。
 *
 * 直接把 `Number(e.target.value)` 写回状态会有个后果：清空输入框时 `Number("")`
 * 等于 0，状态变成 0，输入框又把 "0" 渲染回来 —— 那个 0 就再也删不掉了，只能
 * 把光标挪到它前面去改。
 *
 * 所以编辑中的文本单独存在本组件里，只有解析得出数字时才向上报。空串是编辑的
 * 中间状态，不是数字 0。
 */
export function NumberField({ value, onChange, required }: Props) {
  const [draft, setDraft] = useState(String(value));
  // 记住自己上报过的值，用来区分「外部换了一份表单数据」和「刚才那次输入的回声」。
  // 少了这个区分，输入 "007" 会在上报 7 之后被回写成 "7"，光标当场跳位。
  const [reported, setReported] = useState(value);

  if (value !== reported) {
    setReported(value);
    setDraft(String(value));
  }

  return (
    <input
      type="number"
      value={draft}
      required={required}
      onChange={(e) => {
        const text = e.target.value;
        setDraft(text);
        const n = Number(text);
        if (text !== "" && Number.isFinite(n)) {
          setReported(n);
          onChange(n);
        }
      }}
      onBlur={() => {
        // 离开时仍是空的就还原成当前值，免得留下一个看着像已清空、
        // 实际提交的却是旧数字的输入框。
        if (draft === "" || !Number.isFinite(Number(draft))) {
          setDraft(String(value));
        }
      }}
    />
  );
}
