-- Renames the fixed object-backend identifiers used before
-- FLICK_STORAGE_BACKENDS ('s3_object', 'r2_object') to configured backend
-- names in the flick_api database. Procedure, preconditions, and verification:
-- docs/runbook/storage-backend-rename.md.
--
-- Edit the one insert below so each legacy identifier maps to the name it now
-- has in FLICK_STORAGE_BACKENDS (lower-case letter, then up to 15 lower-case
-- letters or digits). Drop the 'r2_object' pair if that backend was never
-- enabled. Run as the postgres superuser (or the role owning the flick_api
-- tables) with every flick-api and flick-worker
-- replica stopped:
--
--   psql "$FLICK_DATABASE_URL" -v ON_ERROR_STOP=1 -f docs/runbook/storage-backend-rename.sql
begin;

create temporary table storage_backend_rename (old_name text primary key, new_name text not null) on commit drop;
insert into storage_backend_rename (old_name, new_name) values ('s3_object', 'oci'), ('r2_object', 'r2');

-- The legacy checks allow only the fixed identifiers, so they go first. This
-- is the same block flick-api's migration runs (internal/db/storage_backends.go).
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
update object_reconciliation_pending_v2 p set backend = m.new_name
	from storage_backend_rename m where p.backend = m.old_name;
update request_reconciliation_pending_v2 p set backend = m.new_name
	from storage_backend_rename m where p.backend = m.old_name;
-- Unpublished delete events carry the backend inside their JSON payload.
update outbox_events o
	set payload_json = replace(o.payload_json, '"backend":"' || m.old_name || '"', '"backend":"' || m.new_name || '"')
	from storage_backend_rename m
	where o.state <> 'published' and o.payload_json like '%"backend":"' || m.old_name || '"%';
-- Cursors are scan positions only; the API reseeds them under the new names.
delete from object_reconciliation_cursor_v2 where backend in (select old_name from storage_backend_rename);
delete from request_reconciliation_cursor_v2 where backend in (select old_name from storage_backend_rename);

commit;
