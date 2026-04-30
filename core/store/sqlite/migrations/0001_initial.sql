-- 0001_initial.sql — initial Phase 2 schema for the peersh signaling server
-- store. Adds users, devices, psk_records, pairings, sessions tables.
--
-- Phase 2 only writes to users, devices, psk_records, and pairings. The
-- sessions table is created now to keep the migration set monotonic; Phase 5
-- / 6 will start writing to it.

CREATE TABLE IF NOT EXISTS users (
    id            TEXT NOT NULL PRIMARY KEY,
    auth_provider INTEGER NOT NULL,
    created_at    TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS devices (
    id            TEXT NOT NULL PRIMARY KEY,
    public_key    BLOB NOT NULL,
    owner_user_id TEXT NOT NULL,
    kind          INTEGER NOT NULL,
    display_name  TEXT NOT NULL DEFAULT '',
    created_at    TEXT NOT NULL,
    last_seen_at  TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_devices_owner ON devices(owner_user_id);

CREATE TABLE IF NOT EXISTS psk_records (
    user_id       TEXT NOT NULL PRIMARY KEY,
    secret        BLOB NOT NULL,
    display_label TEXT NOT NULL DEFAULT '',
    created_at    TEXT NOT NULL,
    revoked_at    TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS pairings (
    user_id          TEXT NOT NULL,
    mobile_device_id TEXT NOT NULL,
    host_device_id   TEXT NOT NULL,
    created_at       TEXT NOT NULL,
    last_used_at     TEXT NOT NULL,
    PRIMARY KEY (user_id, mobile_device_id, host_device_id)
);
CREATE INDEX IF NOT EXISTS idx_pairings_user ON pairings(user_id);

CREATE TABLE IF NOT EXISTS sessions (
    id               TEXT NOT NULL PRIMARY KEY,
    user_id          TEXT NOT NULL,
    mobile_device_id TEXT NOT NULL,
    host_device_id   TEXT NOT NULL,
    state            INTEGER NOT NULL,
    created_at       TEXT NOT NULL,
    last_active_at   TEXT NOT NULL,
    idle_deadline_at TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_sessions_user ON sessions(user_id);
