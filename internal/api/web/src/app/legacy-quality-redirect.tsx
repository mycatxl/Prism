import { Navigate, useLocation } from "react-router-dom";

/** `/quality` predates the nodes page's view tabs; the parameters are carried across. */
export function LegacyQualityRedirect() {
  const location = useLocation();
  const previous = new URLSearchParams(location.search);
  const next = new URLSearchParams({ view: "exits" });
  for (const [oldKey, newKey] of [
    ["ip", "quality_ip"],
    ["q", "quality_q"],
    ["page", "quality_page"],
  ]) {
    const value = previous.get(oldKey);
    if (value) next.set(newKey, value);
  }
  return <Navigate to={"/nodes?" + next.toString()} replace />;
}
