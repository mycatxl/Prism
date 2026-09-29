import { useEffect, useState } from "react";

const QUERY = "(prefers-reduced-motion: reduce)";

function currentPreference(): boolean {
  return typeof window !== "undefined" && window.matchMedia(QUERY).matches;
}

/**
 * A stated preference for less motion.
 *
 * The charts consult this instead of animating unconditionally: an entrance
 * animation is a state change only if the operator has not asked for stillness,
 * and a wall display that is left on all day should never keep moving.
 */
export function useReducedMotion(): boolean {
  const [reduced, setReduced] = useState(currentPreference);

  useEffect(() => {
    const media = window.matchMedia(QUERY);
    const onChange = () => setReduced(media.matches);
    onChange();
    media.addEventListener("change", onChange);
    return () => media.removeEventListener("change", onChange);
  }, []);

  return reduced;
}