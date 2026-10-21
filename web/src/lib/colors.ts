// Categorical colors follow the entity, not its rank: a key keeps its slot for
// the whole session, so filtering never repaints the survivors. Slots are
// handed out in fixed palette order; past eight keys the tail shares the
// neutral "other" color (charts fold extra series into "Other" anyway).
const assigned = new Map<string, number>();

export const OTHER_COLOR = 'var(--series-other)';

export function seriesColor(key: string): string {
  if (key === '__other__') return OTHER_COLOR;
  let slot = assigned.get(key);
  if (slot === undefined) {
    slot = assigned.size;
    assigned.set(key, slot);
  }
  return slot < 8 ? `var(--series-${slot + 1})` : OTHER_COLOR;
}
