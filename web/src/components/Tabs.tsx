export interface TabDef {
  id: string;
  label: string;
}

// The nav sits directly under the title, one tab per business line. No
// router: the parent just holds `active` in useState and swaps which view
// renders below.
export function Tabs({
  tabs,
  active,
  onChange,
}: {
  tabs: readonly TabDef[];
  active: string;
  onChange: (id: string) => void;
}) {
  return (
    <div className="tab-row" role="tablist">
      {tabs.map((t) => (
        <button
          key={t.id}
          type="button"
          role="tab"
          aria-selected={t.id === active}
          className={`tab-btn${t.id === active ? " active" : ""}`}
          onClick={() => onChange(t.id)}
        >
          {t.label}
        </button>
      ))}
    </div>
  );
}
