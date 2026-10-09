-- Sessions survive a restart. Managed Postgres suspends an idle database, and an unlogged table
-- comes back empty, so everyone was signed out after a short break. A session is touched at most
-- once a minute, so the write-ahead log it adds is small. The table stays out of the shadow log.
ALTER TABLE session SET LOGGED;
COMMENT ON TABLE session IS 'Signed-in sessions. Hard-deleted, not shadow-logged, but durable: they survive a restart.';
