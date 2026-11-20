-- Procedure and verification: docs/runbook/storage-backend-rename.md. Edit the insert below to map
-- each legacy identifier to its FLICK_STORAGE_BACKENDS name, then run with every flick-api and
-- flick-worker replica stopped:
--   psql "$FLICK_DATABASE_URL" -v ON_ERROR_STOP=1 -f docs/runbook/storage-backend-rename.sql
begin;

create temporary table storage_backend_rename (old_name text primary key, new_name text not null) on commit drop;
insert into storage_backend_rename (old_name, new_name) values ('s3_object', 'oci'), ('r2_object', 'r2');

-- Same block as internal/db/storage_backends.go:dropLegacyStorageBackendChecks.
do $$
declare c record;
begin
	for c in
		select conrelid::regclass as tbl, conname
		from pg_constraint
		where contype = 'c'
			and conrelid in ('secrets'::regclass, 'requests'::regclass,
				'object_reconciliation_cursor_v2'::regclass, 'request_reconciliation_cursor_v2'::regclass)
			and pg_get_constraintdef(oid) like '%s3_object%'
	loop
		execute format('alter table %s drop constraint %I', c.tbl, c.conname);
	end loop;
end $$;

update secrets s set storage_backend = m.new_name
	from storage_backend_rename m where s.storage_backend = m.old_name;
update requests r set storage_backend = m.new_name
	from storage_backend_rename m where r.storage_backend = m.old_name;
-- Unpublished delete events carry the backend inside their JSON payload.
update outbox_events o
	set payload_json = replace(o.payload_json, '"backend":"' || m.old_name || '"', '"backend":"' || m.new_name || '"')
	from storage_backend_rename m
	where o.state <> 'published' and o.payload_json like '%"backend":"' || m.old_name || '"%';
-- No current worker accepts a backend-less object delete; reconciliation re-finds the object.
delete from outbox_events
	where state <> 'published'
		and payload_json like '%"kind":"delete_oci_object"%' and payload_json not like '%"backend":%';
-- Renaming a pending claim would let its queued delete's ack miss it and block the key forever.
delete from object_reconciliation_cursor_v2 where backend in (select old_name from storage_backend_rename);
delete from request_reconciliation_cursor_v2 where backend in (select old_name from storage_backend_rename);
delete from object_reconciliation_pending_v2 where backend in (select old_name from storage_backend_rename);
delete from request_reconciliation_pending_v2 where backend in (select old_name from storage_backend_rename);

commit;
