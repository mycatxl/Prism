import { ArrowLeft } from "lucide-react";
import { Link, useLocation, useNavigate } from "react-router-dom";

/** Keep the filtered list in history; direct visits have a normal list link. */
export function BackLink({ fallback, children }: { fallback: string; children: React.ReactNode }) {
  const { state } = useLocation();
  const navigate = useNavigate();
  const returnTo = typeof state?.returnTo === "string" && state.returnTo.split(/[?#]/)[0] === fallback
    ? state.returnTo as string : fallback;
  return <Link className="workspace-back-link" to={returnTo} onClick={(event) => {
    if (state?.fromList && window.history.state?.idx > 0 && event.button === 0 && !event.metaKey && !event.ctrlKey && !event.shiftKey && !event.altKey) {
      event.preventDefault();
      navigate(-1);
    }
  }}><ArrowLeft size={16} aria-hidden />{children}</Link>;
}
