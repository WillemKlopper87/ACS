import { useEffect, useState } from "react";
import { api } from "../api/client";
import type { Customer } from "../api/types";

// Shared customer list for the tenancy `customer_id` pickers scattered
// across device-groups/templates/policies/scheduled-jobs/rollouts — each
// screen used to call api.listCustomers() itself; centralized here so
// adding another picker doesn't mean another copy-pasted fetch.
export function useCustomers() {
  const [customers, setCustomers] = useState<Customer[]>([]);

  useEffect(() => {
    api.listCustomers().then(
      (r) => setCustomers(r.items),
      () => {}, // best-effort: pickers just fall back to an empty list
    );
  }, []);

  return customers;
}

export function customerName(customers: Customer[], id?: string | null): string {
  if (!id) return "—";
  return customers.find((c) => c.id === id)?.name ?? "—";
}
