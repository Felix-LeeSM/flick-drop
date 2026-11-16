-- Flick local development database bootstrap (compose.yaml mounts this into
-- docker-entrypoint-initdb.d). Mirrors the production layout: one PostgreSQL
-- instance, two logical databases, each owned by its least-privilege role, so
-- flick-api and flick-worker cannot read or write each other's schema
-- (docs/architecture/database-schema.md, "Ownership").
create role flick_api login password 'flick_api';
create role flick_worker login password 'flick_worker';

create database flick_api owner flick_api;
create database flick_worker owner flick_worker;
