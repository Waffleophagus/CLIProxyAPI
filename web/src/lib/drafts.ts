// Form drafts edited against a server snapshot ("base"). A field is dirty
// when its draft value differs from the base it was edited from.

export function dirtyFields<T extends object>(
  draft: T,
  base: T,
  keys: readonly (keyof T)[] = Object.keys(base) as (keyof T)[]
): (keyof T)[] {
  return keys.filter((k) => draft[k] !== base[k]);
}

/**
 * Moves a draft onto a fresh server snapshot: dirty fields keep the user's
 * edit, clean fields take the new server value. Fields listed in reset take
 * the server value even if dirty (for example the card that was just saved).
 */
export function rebaseDraft<T extends object>(draft: T, base: T, server: T, reset: readonly (keyof T)[] = []): T {
  const next = { ...server };
  for (const k of dirtyFields(draft, base)) {
    if (!reset.includes(k)) next[k] = draft[k];
  }
  return next;
}

/** Restores the given fields of a draft to the base values. */
export function revertFields<T extends object>(draft: T, base: T, keys: readonly (keyof T)[]): T {
  const next = { ...draft };
  for (const k of keys) next[k] = base[k];
  return next;
}
