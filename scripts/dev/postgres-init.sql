-- Mirrors production: two logical databases, each owned by its least-privilege role.
create role flick_api login password 'flick_api';
create role flick_worker login password 'flick_worker';

create database flick_api owner flick_api;
create database flick_worker owner flick_worker;
