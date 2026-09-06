import { Ident } from "@/components/Ident";
import { PageHeader, Skeleton } from "@/components/ui";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { crumbs } from "@/lib/crumbs";

/**
 * Layout rule 8: the placeholder is the loaded page's own structure. The
 * previous `TableSkeleton` reserved a three-column table where the result is a
 * breadcrumb trail, a definition card and a pipeline of cards, so the header
 * alone jumped 13.25px and everything below it reflowed on arrival.
 *
 * The parts whose geometry does not depend on the response are rendered for
 * real: the breadcrumbs (the name is in the URL), the header, and the tab list.
 * The columns are placeholders, since the environment count is what is in
 * flight; two is the smallest pipeline that shows the scroller.
 */
export function ApplicationHomeSkeleton({ name }: { name: string }) {
  return (
    <div aria-busy="true">
      <span className="sr-only">Loading {name}…</span>
      <PageHeader
        breadcrumbs={crumbs.application(name)}
        title={
          <span className="row-wrap">
            <Ident kind="app" value={name} tooltip={false} />
            {/* The status chip's box, reserved at its loaded height. */}
            <Skeleton width={84} height={22} />
          </span>
        }
        documentTitle={name}
        subtitle={<Skeleton width="42%" height="1em" />}
        actions={<Skeleton width={320} height={38} />}
      />
      <section className="card definition-card" aria-hidden>
        <div className="definition-grid">
          {["Release name", "Schema", "Contract"].map((label) => (
            <div key={label}>
              <span className="faint text-sm">{label}</span>
              <Skeleton width="70%" height={22} />
            </div>
          ))}
        </div>
        <div className="definition-alignment">
          <div className="between">
            <span className="faint text-sm">Alignment</span>
            <Skeleton width={72} height={30} />
          </div>
          <Skeleton width="30%" height="1em" />
        </div>
      </section>
      <Tabs value="pipeline" className="application-tabs">
        <TabsList variant="line" aria-label="Application views" className="mb-2">
          <TabsTrigger value="pipeline">Environments</TabsTrigger>
          <TabsTrigger value="matrix">Matrix</TabsTrigger>
        </TabsList>
      </Tabs>
      <div className="pipeline-scroll" aria-hidden>
        <div className="pipeline" data-columns={2}>
          {[0, 1].map((column) => (
            <section className="pipeline-column" key={column}>
              <header className="pipeline-head">
                <Skeleton width={140} height={22} />
                <Skeleton width={30} height={30} />
              </header>
              {["Values", "Release", "Subscribers"].map((section) => (
                <section className="pipeline-section" key={section}>
                  <h3 className="pipeline-section-title">{section}</h3>
                  <ul className="pipeline-rows">
                    {[0, 1].map((row) => (
                      <li className="pipeline-row" key={row}>
                        <Skeleton width={row === 0 ? "62%" : "48%"} height={22} />
                      </li>
                    ))}
                  </ul>
                </section>
              ))}
            </section>
          ))}
        </div>
      </div>
    </div>
  );
}
