PRAGMA foreign_keys = ON;

CREATE TABLE IF NOT EXISTS devices (
  id TEXT PRIMARY KEY,
  installation_id TEXT NOT NULL UNIQUE,
  public_key TEXT NOT NULL,
  status TEXT NOT NULL,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS licenses (
  id TEXT PRIMARY KEY,
  user_id TEXT NOT NULL,
  plan TEXT NOT NULL,
  status TEXT NOT NULL,
  expires_at TEXT,
  device_limit INTEGER NOT NULL DEFAULT 1,
  task_quota INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS catalog_items (
  id TEXT PRIMARY KEY,
  emby_item_id TEXT NOT NULL UNIQUE,
  media_type TEXT NOT NULL,
  title TEXT NOT NULL,
  library_id TEXT NOT NULL,
  size_bytes INTEGER NOT NULL DEFAULT 0,
  updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS tasks (
  id TEXT PRIMARY KEY,
  trace_id TEXT NOT NULL,
  user_id TEXT NOT NULL,
  device_id TEXT NOT NULL REFERENCES devices(id),
  catalog_item_id TEXT NOT NULL REFERENCES catalog_items(id),
  state TEXT NOT NULL,
  idempotency_key TEXT NOT NULL,
  target_cloud_dir TEXT NOT NULL,
  target_nas_dir TEXT NOT NULL,
  quota_units INTEGER NOT NULL DEFAULT 1,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  UNIQUE(user_id, device_id, idempotency_key)
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_tasks_active_dedupe
ON tasks(user_id, device_id, catalog_item_id, target_cloud_dir, target_nas_dir)
WHERE state IN (
  'CREATED',
  'POLICY_CHECKING',
  'WAITING_SHARE',
  'ACTION_REQUIRED',
  'SHARE_READY',
  'WAITING_CLIENT',
  'RECEIVING_TO_USER_CLOUD',
  'USER_CLOUD_READY',
  'COPYING_TO_NAS',
  'VERIFYING',
  'RETRY_WAIT',
  'PAUSED',
  'CANCEL_REQUESTED'
);

CREATE TABLE IF NOT EXISTS task_events (
  id TEXT PRIMARY KEY,
  task_id TEXT NOT NULL REFERENCES tasks(id),
  state TEXT NOT NULL,
  actor TEXT NOT NULL,
  reason TEXT,
  external_task_id TEXT,
  occurred_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS outbox_events (
  id TEXT PRIMARY KEY,
  topic TEXT NOT NULL,
  payload_json TEXT NOT NULL,
  created_at TEXT NOT NULL,
  dispatched_at TEXT
);

CREATE TABLE IF NOT EXISTS audit_events (
  id TEXT PRIMARY KEY,
  occurred_at TEXT NOT NULL,
  user_id TEXT,
  device_id TEXT,
  task_id TEXT,
  action TEXT NOT NULL,
  result TEXT NOT NULL,
  request_id TEXT,
  trace_id TEXT
);
