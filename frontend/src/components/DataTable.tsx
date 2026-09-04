import { useVirtualizer } from "@tanstack/react-virtual";
import {
  type ColumnDef,
  flexRender,
  getCoreRowModel,
  getFilteredRowModel,
  getSortedRowModel,
  useReactTable,
} from "@tanstack/react-table";
import { useRef, useState } from "react";

// Single shared table for every screen (build plan §3.4: "DataTable: single
// shared component wrapping TanStack Table ... every screen above is an
// instance of it with different column defs"). Sticky header, sortable
// columns, client-side global filter, optional row click, optional
// virtualization, optional multi-select — the data-dense/tabular contract
// this app is built around, at both single-device-review scale and
// fleet-review scale.
//
// virtualize is opt-in rather than always-on: Device Fleet/Jobs at a
// working-set size (tens of rows) don't need it, and a plain <table> with
// a real <tbody> is simpler to reason about than a virtualized one when
// row count doesn't demand it. Fleet Control (hundreds+ rows) turns it on.
interface SelectionProps {
  selectedIds: Set<string>;
  onToggle: (id: string) => void;
  onToggleAll: (checked: boolean) => void;
}

interface DataTableProps<T> {
  data: T[];
  columns: ColumnDef<T, any>[];
  globalFilter?: string;
  onRowClick?: (row: T) => void;
  selectedRowId?: string;
  getRowId?: (row: T) => string;
  emptyMessage?: string;
  virtualize?: boolean;
  rowHeight?: number;
  maxHeight?: string;
  selection?: SelectionProps;
}

export function DataTable<T>({
  data,
  columns,
  globalFilter,
  onRowClick,
  selectedRowId,
  getRowId,
  emptyMessage = "No rows.",
  virtualize = false,
  rowHeight = 33,
  maxHeight,
  selection,
}: DataTableProps<T>) {
  const [sorting, setSorting] = useState<{ id: string; desc: boolean }[]>([]);
  const scrollRef = useRef<HTMLDivElement>(null);

  const allColumns: ColumnDef<T, any>[] = selection
    ? [
        {
          id: "__select",
          header: () => (
            <input
              type="checkbox"
              aria-label="Select all rows"
              checked={data.length > 0 && data.every((d) => selection.selectedIds.has(getRowId!(d)))}
              ref={(el) => {
                if (!el) return;
                const someSelected = data.some((d) => selection.selectedIds.has(getRowId!(d)));
                const allSelected = data.length > 0 && data.every((d) => selection.selectedIds.has(getRowId!(d)));
                el.indeterminate = someSelected && !allSelected;
              }}
              onChange={(e) => selection.onToggleAll(e.target.checked)}
              onClick={(e) => e.stopPropagation()}
            />
          ),
          cell: ({ row }) => {
            const id = getRowId!(row.original);
            return (
              <input
                type="checkbox"
                aria-label={`Select row ${id}`}
                checked={selection.selectedIds.has(id)}
                onChange={() => selection.onToggle(id)}
                onClick={(e) => e.stopPropagation()}
              />
            );
          },
        },
        ...columns,
      ]
    : columns;

  const table = useReactTable({
    data,
    columns: allColumns,
    state: { sorting, globalFilter },
    onSortingChange: setSorting,
    getCoreRowModel: getCoreRowModel(),
    getSortedRowModel: getSortedRowModel(),
    getFilteredRowModel: getFilteredRowModel(),
  });

  const rows = table.getRowModel().rows;

  const virtualizer = useVirtualizer({
    count: rows.length,
    getScrollElement: () => scrollRef.current,
    estimateSize: () => rowHeight,
    overscan: 12,
    enabled: virtualize,
  });

  const virtualRows = virtualize ? virtualizer.getVirtualItems() : null;
  const totalSize = virtualize ? virtualizer.getTotalSize() : undefined;
  const paddingTop = virtualRows && virtualRows.length > 0 ? virtualRows[0].start : 0;
  const paddingBottom = virtualRows && virtualRows.length > 0 ? (totalSize ?? 0) - virtualRows[virtualRows.length - 1].end : 0;

  const bodyRows = virtualRows ? virtualRows.map((vr) => rows[vr.index]) : rows;

  return (
    <div className="table-wrap" ref={scrollRef} style={maxHeight ? { maxHeight } : undefined}>
      <table>
        <thead>
          {table.getHeaderGroups().map((hg) => (
            <tr key={hg.id}>
              {hg.headers.map((header) => {
                const sortable = header.column.getCanSort();
                const sorted = header.column.getIsSorted();
                const content = (
                  <>
                    {flexRender(header.column.columnDef.header, header.getContext())}
                    <span className="arrow">{sorted === "asc" ? " ▴" : sorted === "desc" ? " ▾" : ""}</span>
                  </>
                );
                // Sorting lives on a real <button> inside the <th> rather
                // than an onClick on the cell: that is what makes it
                // reachable by keyboard and announced as a control, and
                // aria-sort is what tells a screen reader the current
                // direction. .th-sort strips the button's own chrome so
                // the header still looks identical.
                return (
                  <th
                    key={header.id}
                    className={sorted ? "sorted" : ""}
                    aria-sort={sorted === "asc" ? "ascending" : sorted === "desc" ? "descending" : sortable ? "none" : undefined}
                    style={sortable ? undefined : { cursor: "default" }}
                  >
                    {sortable ? (
                      <button type="button" className="th-sort" onClick={header.column.getToggleSortingHandler()}>
                        {content}
                      </button>
                    ) : (
                      content
                    )}
                  </th>
                );
              })}
            </tr>
          ))}
        </thead>
        <tbody>
          {rows.length === 0 && (
            <tr>
              <td colSpan={allColumns.length} className="empty-cell">
                {emptyMessage}
              </td>
            </tr>
          )}
          {virtualize && paddingTop > 0 && (
            <tr>
              <td colSpan={allColumns.length} style={{ height: paddingTop, padding: 0, border: "none" }} />
            </tr>
          )}
          {bodyRows.map((row) => {
            const id = getRowId ? getRowId(row.original) : row.id;
            return (
              // A clickable row is the primary way into Device Detail, so
              // it has to be reachable without a mouse. The row keeps its
              // implicit `row` role — putting role="button" on a <tr>
              // would break the table semantics for screen readers — and
              // gains focus plus Enter/Space activation instead.
              <tr
                key={row.id}
                className={selectedRowId === id || selection?.selectedIds.has(id) ? "selected" : ""}
                onClick={onRowClick ? () => onRowClick(row.original) : undefined}
                tabIndex={onRowClick ? 0 : undefined}
                onKeyDown={
                  onRowClick
                    ? (e) => {
                        if (e.key !== "Enter" && e.key !== " ") return;
                        // Space would otherwise scroll the table.
                        e.preventDefault();
                        onRowClick(row.original);
                      }
                    : undefined
                }
                style={onRowClick ? { cursor: "pointer" } : undefined}
              >
                {row.getVisibleCells().map((cell) => (
                  <td key={cell.id}>{flexRender(cell.column.columnDef.cell, cell.getContext())}</td>
                ))}
              </tr>
            );
          })}
          {virtualize && paddingBottom > 0 && (
            <tr>
              <td colSpan={allColumns.length} style={{ height: paddingBottom, padding: 0, border: "none" }} />
            </tr>
          )}
        </tbody>
      </table>
    </div>
  );
}
