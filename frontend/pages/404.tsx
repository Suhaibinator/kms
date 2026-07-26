import Link from "next/link";
import { PageTitle } from "@/components/ui";

// Next's built-in 404 renders a light-themed page, which reads as a broken app
// inside a dark console. This one matches the shell and carries a title.
export default function NotFoundPage() {
  return (
    <div className="auth-wrap">
      <PageTitle title="Page not found" />
      <div className="auth-card">
        <div className="auth-brand">
          <div className="logo">K</div>
          <div>
            <div style={{ fontWeight: 700, fontSize: 18 }}>Page not found</div>
            <div className="faint text-sm">404</div>
          </div>
        </div>
        <p className="muted text-sm mb-16">
          That page does not exist. It may have been renamed, or the link may be
          out of date.
        </p>
        <Link className="btn btn-primary btn-block" href="/">
          Back to overview
        </Link>
      </div>
    </div>
  );
}
