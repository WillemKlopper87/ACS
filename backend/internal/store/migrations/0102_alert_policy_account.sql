-- A tenant-scoped alert policy named its account by ID alone. Account
-- IDs are unique per customer (0098, 0099), so one policy applied to
-- every customer's account of that ID. The policy now also holds the
-- account's registry key.
--
-- A policy whose ID names exactly one account today is tied to it. One
-- whose ID names several accounts, or none yet, keeps a NULL key and
-- goes on matching by ID, as before: which account was meant cannot be
-- told from the row.
ALTER TABLE alert_policies ADD COLUMN account_key UUID REFERENCES bss_accounts(id) ON DELETE CASCADE;

UPDATE alert_policies p
   SET account_key = a.id
  FROM bss_accounts a
 WHERE p.scope = 'tenant'
   AND a.account_id = p.tenant_id
   AND (SELECT count(*) FROM bss_accounts b WHERE b.account_id = p.tenant_id) = 1;
