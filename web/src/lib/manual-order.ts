/** Move only visible IDs; hidden records retain their full-list slots. */
export function reorderVisible(full: number[], visible: number[], source: number, target: number): number[] {
  const all = new Set(full);
  const selected = new Set(visible);
  if (all.size !== full.length || selected.size !== visible.length || visible.some(id => !all.has(id)) || !selected.has(source) || !selected.has(target)) {
    throw new Error("列表已变化，请刷新后重新排序");
  }
  if (source === target) return full;
  const ordered = full.filter(id => selected.has(id));
  const from = ordered.indexOf(source);
  const to = ordered.indexOf(target);
  ordered.splice(from, 1);
  ordered.splice(to, 0, source);
  let index = 0;
  return full.map(id => selected.has(id) ? ordered[index++] : id);
}
