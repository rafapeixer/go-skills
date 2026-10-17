-- Server-side backstops for the role the service logs in as. They apply to
-- sessions started after the change, also through PgBouncer, which opens its
-- server connections as this role.
ALTER ROLE app SET statement_timeout = '5s';
ALTER ROLE app SET idle_in_transaction_session_timeout = '10s';
-- PostgreSQL 17 and later; older servers reject the unknown parameter.
ALTER ROLE app SET transaction_timeout = '30s';
