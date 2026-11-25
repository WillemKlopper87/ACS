import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { DataTable, type DataColumn } from "./DataTable";

// Every screen's table is this component, so what the TanStack Table upgrade
// could break silently is covered here: sorting, the global filter, row clicks
// and selection.
interface Row {
  id: string;
  name: string;
  count: number;
  seen: string;
}

const rows: Row[] = [
  { id: "a", name: "Charlie", count: 10, seen: "2026-11-03T08:00:00Z" },
  { id: "b", name: "alpha", count: 2, seen: "2026-11-01T08:00:00Z" },
  { id: "c", name: "Bravo", count: 33, seen: "2026-11-02T08:00:00Z" },
];

const columns: DataColumn<Row>[] = [
  { accessorKey: "name", header: "Name" },
  { accessorKey: "count", header: "Count" },
  { accessorKey: "seen", header: "Seen", cell: ({ getValue }) => String(getValue()).slice(0, 10) },
];

afterEach(cleanup);

const bodyNames = () =>
  within(screen.getAllByRole("rowgroup")[1])
    .getAllByRole("row")
    .map((r) => within(r).getAllByRole("cell")[0].textContent);

describe("DataTable", () => {
  it("renders headers and cells through the column definitions", () => {
    render(<DataTable data={rows} columns={columns} getRowId={(r) => r.id} />);
    expect(screen.getByRole("button", { name: /Name/ })).toBeTruthy();
    expect(bodyNames()).toEqual(["Charlie", "alpha", "Bravo"]);
    expect(screen.getByText("2026-11-01")).toBeTruthy(); // the custom cell renderer
  });

  it("sorts a numeric column largest first, then smallest first, and says so in aria-sort", () => {
    render(<DataTable data={rows} columns={columns} getRowId={(r) => r.id} />);
    const header = () => screen.getByRole("columnheader", { name: /Count/ });
    expect(header().getAttribute("aria-sort")).toBe("none");
    // Numbers start descending (the library's default, as before the upgrade). 33, 10, 2
    // is numeric order; as text it would be 33, 2, 10.
    fireEvent.click(within(header()).getByRole("button"));
    expect(bodyNames()).toEqual(["Bravo", "Charlie", "alpha"]);
    expect(header().getAttribute("aria-sort")).toBe("descending");
    fireEvent.click(within(header()).getByRole("button"));
    expect(bodyNames()).toEqual(["alpha", "Charlie", "Bravo"]);
    expect(header().getAttribute("aria-sort")).toBe("ascending");
  });

  it("sorts text without regard to case and dates by time", () => {
    render(<DataTable data={rows} columns={columns} getRowId={(r) => r.id} />);
    fireEvent.click(within(screen.getByRole("columnheader", { name: /Name/ })).getByRole("button"));
    expect(bodyNames()).toEqual(["alpha", "Bravo", "Charlie"]);
    fireEvent.click(within(screen.getByRole("columnheader", { name: /Seen/ })).getByRole("button"));
    expect(bodyNames()).toEqual(["alpha", "Bravo", "Charlie"]); // 11-01, 11-02, 11-03
  });

  it("filters rows with the global filter and shows the empty message when none match", () => {
    const { rerender } = render(<DataTable data={rows} columns={columns} globalFilter="ravo" getRowId={(r) => r.id} />);
    expect(bodyNames()).toEqual(["Bravo"]);
    rerender(<DataTable data={rows} columns={columns} globalFilter="zzz" emptyMessage="Nothing here." getRowId={(r) => r.id} />);
    expect(screen.getByText("Nothing here.")).toBeTruthy();
  });

  it("calls onRowClick with the row's data, by click and by Enter", () => {
    const onRowClick = vi.fn();
    render(<DataTable data={rows} columns={columns} onRowClick={onRowClick} getRowId={(r) => r.id} />);
    const bravo = screen.getByText("Bravo").closest("tr") as HTMLElement;
    fireEvent.click(bravo);
    expect(onRowClick).toHaveBeenLastCalledWith(rows[2]);
    fireEvent.keyDown(bravo, { key: "Enter" });
    expect(onRowClick).toHaveBeenCalledTimes(2);
  });

  it("adds a selection column that reports toggles", () => {
    const onToggle = vi.fn();
    const onToggleAll = vi.fn();
    render(
      <DataTable
        data={rows}
        columns={columns}
        getRowId={(r) => r.id}
        selection={{ selectedIds: new Set(["b"]), onToggle, onToggleAll }}
      />,
    );
    expect((screen.getByLabelText("Select row b") as HTMLInputElement).checked).toBe(true);
    expect((screen.getByLabelText("Select row a") as HTMLInputElement).checked).toBe(false);
    fireEvent.click(screen.getByLabelText("Select row a"));
    expect(onToggle).toHaveBeenCalledWith("a");
    fireEvent.click(screen.getByLabelText("Select all rows"));
    expect(onToggleAll).toHaveBeenCalledWith(true);
  });
});
