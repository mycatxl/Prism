export function readMigratedValue(
  storage: Storage,
  key: string,
  legacyKeys: readonly string[],
): string | null {
  const current = storage.getItem(key);
  if (current !== null) return current;

  for (const legacyKey of legacyKeys) {
    const previous = storage.getItem(legacyKey);
    if (previous === null) continue;
    try {
      storage.setItem(key, previous);
      for (const name of legacyKeys) storage.removeItem(name);
    } catch {
      // An existing preference can still be used when storage is full.
    }
    return previous;
  }
  return null;
}

export function removeStoredValues(storage: Storage, keys: readonly string[]) {
  for (const key of keys) {
    try {
      storage.removeItem(key);
    } catch {
      // Continue attempting the remaining keys when one operation is blocked.
    }
  }
}
