import { useCallback, useEffect, useMemo, useState } from "react";
import { useSearchParams } from "react-router-dom";

/**
 * List state that lives in the URL.
 *
 * A console is shared: an operator pastes a filtered link into a ticket, and the
 * next person opens exactly that view. Every list therefore keeps its search,
 * sort, page and filters in the query string — refresh keeps them, the back
 * button walks them, and a filter change always returns to the first page.
 */
export type SortDir = "asc" | "desc";

export type TableStateOptions = {
  /** Rows per page. Default 50, the API's own default. */
  limit?: number;
  /** Column to sort by on first load, if the API supports one. */
  sort?: string;
  dir?: SortDir;
  /** Filter defaults; each becomes its own query parameter. */
  filters?: Record<string, string>;
};

export type TableState = {
  search: string;
  setSearch: (value: string) => void;
  sort: string;
  dir: SortDir;
  toggleSort: (id: string) => void;
  setSort: (id: string, dir?: SortDir) => void;
  offset: number;
  setOffset: (value: number) => void;
  limit: number;
  setLimit: (value: number) => void;
  filters: Record<string, string>;
  setFilter: (name: string, value: string) => void;
  reset: () => void;
  isFiltered: boolean;
  /** Ready-to-spread API parameters: no empty values, offset and limit numeric. */
  query: Record<string, string | number>;
};

const SEARCH_PARAM = "q";
const SORT_PARAM = "sort";
const DIR_PARAM = "dir";
const OFFSET_PARAM = "offset";
const LIMIT_PARAM = "limit";

export function useTableState(options: TableStateOptions = {}): TableState {
  const { limit: initialLimit = 50, sort: initialSort = "", dir: initialDir = "desc", filters: initialFilters = {} } = options;
  const [params, setParams] = useSearchParams();

  // The input is typed at keystroke speed; the URL — and therefore the request —
  // is written once the operator pauses.
  const [search, setSearchState] = useState(() => params.get(SEARCH_PARAM) ?? "");

  const patch = useCallback(
    (mutate: (next: URLSearchParams) => void, resetPage: boolean) => {
      setParams(
        (prev) => {
          const next = new URLSearchParams(prev);
          mutate(next);
          if (resetPage) next.delete(OFFSET_PARAM);
          return next;
        },
        { replace: true },
      );
    },
    [setParams],
  );

  useEffect(() => {
    const value = search.trim();
    const timer = setTimeout(() => {
      setParams(
        (prev) => {
          if ((prev.get(SEARCH_PARAM) ?? "") === value) return prev;
          const next = new URLSearchParams(prev);
          if (value) next.set(SEARCH_PARAM, value);
          else next.delete(SEARCH_PARAM);
          next.delete(OFFSET_PARAM);
          return next;
        },
        { replace: true },
      );
    }, 300);
    return () => clearTimeout(timer);
  }, [search, setParams]);

  const setSearch = useCallback((value: string) => setSearchState(value), []);

  const sort = params.get(SORT_PARAM) ?? initialSort;
  const dir: SortDir = (params.get(DIR_PARAM) ?? initialDir) === "asc" ? "asc" : "desc";
  const offset = Number(params.get(OFFSET_PARAM) ?? 0) || 0;
  const limit = Number(params.get(LIMIT_PARAM) ?? initialLimit) || initialLimit;

  const setSort = useCallback(
    (id: string, nextDir?: SortDir) => {
      patch((next) => {
        next.set(SORT_PARAM, id);
        next.set(DIR_PARAM, nextDir ?? "asc");
      }, true);
    },
    [patch],
  );

  const toggleSort = useCallback(
    (id: string) => {
      if (id !== sort) {
        setSort(id, "asc");
        return;
      }
      setSort(id, dir === "asc" ? "desc" : "asc");
    },
    [dir, setSort, sort],
  );

  const setOffset = useCallback(
    (value: number) => {
      patch((next) => {
        if (value > 0) next.set(OFFSET_PARAM, String(value));
        else next.delete(OFFSET_PARAM);
      }, false);
    },
    [patch],
  );

  const setLimit = useCallback(
    (value: number) => {
      patch((next) => {
        if (value !== initialLimit) next.set(LIMIT_PARAM, String(value));
        else next.delete(LIMIT_PARAM);
      }, true);
    },
    [initialLimit, patch],
  );

  const setFilter = useCallback(
    (name: string, value: string) => {
      patch((next) => {
        if (value) next.set(name, value);
        else next.delete(name);
      }, true);
    },
    [patch],
  );

  const filters = useMemo(() => {
    const values: Record<string, string> = {};
    for (const name of Object.keys(initialFilters)) {
      const value = params.get(name);
      if (value) values[name] = value;
    }
    return values;
    // `initialFilters` is a literal at every call site; its keys are what matter.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [params, Object.keys(initialFilters).join("|")]);

  const reset = useCallback(() => {
    setSearchState("");
    setParams(
      (prev) => {
        const next = new URLSearchParams();
        const kept = prev.get(LIMIT_PARAM);
        if (kept) next.set(LIMIT_PARAM, kept);
        return next;
      },
      { replace: true },
    );
  }, [setParams]);

  const query = useMemo(() => {
    const values: Record<string, string | number> = { limit, offset };
    if (search.trim()) values.q = search.trim();
    if (sort) {
      values.sort = sort;
      values.dir = dir;
    }
    for (const [name, value] of Object.entries(filters)) values[name] = value;
    return values;
  }, [dir, filters, limit, offset, search, sort]);

  const isFiltered =
    search.trim().length > 0 ||
    Object.values(filters).some(Boolean) ||
    sort !== initialSort ||
    offset > 0;

  return {
    search,
    setSearch,
    sort,
    dir,
    toggleSort,
    setSort,
    offset,
    setOffset,
    limit,
    setLimit,
    filters,
    setFilter,
    reset,
    isFiltered,
    query,
  };
}
