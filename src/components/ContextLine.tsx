import "../styles/context.css";
import type { ActionContext } from "../state/selectors";
import { addDays, clock, hourOf, ymd } from "../lib/time";

const MONTHS = [
  "Jan",
  "Feb",
  "Mar",
  "Apr",
  "May",
  "Jun",
  "Jul",
  "Aug",
  "Sep",
  "Oct",
  "Nov",
  "Dec",
];
const DOW = ["Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"];

/** A date the way a person says it: Today · Tomorrow · Thu 11 Sep (with the hour when there is one). */
function dayLabel(iso: string, withHour: boolean): string {
  // a bare yyyy-mm-dd is a local day, not midnight UTC
  const d = iso.length === 10 ? new Date(iso + "T00:00:00") : new Date(iso);
  const day = ymd(d);
  const hour = withHour ? ` ${clock(hourOf(d))}` : "";
  if (day === ymd(new Date())) return `Today${hour}`;
  if (day === ymd(addDays(new Date(), 1))) return `Tomorrow${hour}`;
  return `${DOW[d.getDay()]} ${d.getDate()} ${MONTHS[d.getMonth()]}${hour}`;
}

const WHY_VIA: Record<NonNullable<ActionContext["whySource"]>, string> = {
  requirement: "requirement",
  ask: "the ask",
  project: "project outcome",
  area: "area standard",
};

/**
 * Why · when · where, in one line under an action. Each answer is shown where
 * there is one; where there is not, the gap is named ("no why yet") and — when
 * `onClarify` is given — is a button that opens the item to fill it in. An
 * inherited why (from the project or area) reads quieter than the item's own.
 */
export function ContextLine({
  ctx,
  onClarify,
  compact,
  ownWhyOnly,
}: {
  ctx: ActionContext;
  onClarify?: () => void;
  compact?: boolean;
  ownWhyOnly?: boolean;
}) {
  // under a project's own summary the inherited why is already on screen: say only what is the item's own
  const showWhy = !(
    ownWhyOnly &&
    (ctx.whySource === "project" || ctx.whySource === "area")
  );
  const overdue = ctx.dueAt != null && ctx.dueAt.slice(0, 10) < ymd(new Date());
  const gap = (label: string) =>
    onClarify ? (
      <button
        type="button"
        className="ctx__gap"
        onClick={(e) => {
          e.stopPropagation();
          onClarify();
        }}
      >
        {label}
      </button>
    ) : (
      <span className="ctx__gap">{label}</span>
    );

  return (
    <div className={"ctx" + (compact ? " ctx--compact" : "")}>
      {showWhy && (
        <span className="ctx__part ctx__part--why">
          <span className="ctx__key">Why</span>
          {ctx.why ? (
            <span
              className={
                "ctx__val" +
                (ctx.whySource === "project" || ctx.whySource === "area"
                  ? " is-inherited"
                  : "")
              }
              title={`From the ${WHY_VIA[ctx.whySource!]}`}
            >
              {ctx.why}
            </span>
          ) : (
            gap("no why yet")
          )}
        </span>
      )}
      <span className="ctx__part">
        <span className="ctx__key">When</span>
        {ctx.scheduledAt || ctx.dueAt ? (
          <span className="ctx__val">
            {ctx.scheduledAt && dayLabel(ctx.scheduledAt, true)}
            {ctx.scheduledAt && ctx.dueAt && " · "}
            {ctx.dueAt && (
              <span className={overdue ? "ctx__overdue" : undefined}>
                due {dayLabel(ctx.dueAt, false)}
              </span>
            )}
          </span>
        ) : (
          gap("not placed")
        )}
      </span>
      <span className="ctx__part">
        <span className="ctx__key">Where</span>
        {ctx.place || ctx.tool ? (
          <span className="ctx__val">
            {ctx.place}
            {ctx.place && ctx.tool && " · "}
            {ctx.tool && (
              <a
                className="ctx__tool"
                href={ctx.tool.url}
                target="_blank"
                rel="noreferrer noopener"
                onClick={(e) => e.stopPropagation()}
              >
                {ctx.tool.label} ↗
              </a>
            )}
          </span>
        ) : (
          gap("nowhere yet")
        )}
      </span>
    </div>
  );
}
