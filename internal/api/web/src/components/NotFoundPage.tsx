import { ArrowLeft, FileQuestion } from "lucide-react";
import { Link } from "react-router-dom";
import { useI18n } from "../i18n";

export function NotFoundPage() {
  const { t } = useI18n();
  return <section className="workspace-empty"><FileQuestion size={30} /><h1>{t("页面不存在")}</h1><Link className="btn btn-secondary" to="/dashboard"><ArrowLeft size={16} />{t("返回工作台")}</Link></section>;
}
