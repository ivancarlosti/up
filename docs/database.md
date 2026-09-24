# Up - Database

> MariaDB/MySQL only. `docker/docker-compose.yml` never creates a database
> service: it connects to `DB_HOST` (`host.docker.internal` by default) and
> manages its own schema with GORM `AutoMigrate` on every boot.
>
> `docker/docker-compose-bundle.yml` is the optional self-contained variant: it
> adds a `mariadb:11` service (volume `mariadb_data`, credentials taken from the
> same `docker/.env`) and is addressed by its compose **service name**, i.e.
> `DB_HOST=mariadb`. Everything below applies to both.

## 1. Connection

| Variable | Default | Notes |
|---|---|---|
| `DB_HOST` | `host.docker.internal` | `127.0.0.1` when the backend runs directly on the host; `mariadb` (the compose service name) with `docker/docker-compose-bundle.yml` |
| `DB_PORT` | `3306` | |
| `DB_DATABASE` | `up` | must exist; Up creates the tables, not the schema |
| `DB_USERNAME` | `up` | needs DDL rights (CREATE/ALTER/INDEX/DROP on the schema) |
| `DB_PASSWORD` | `secret` | `@` and `/` must be URL encoded in the DSN |
| `DB_SSL` | `false` | `true` adds `tls=true` to the DSN (requires a valid server certificate) |

The DSN built by `internal/config/helpers.go` is:

```
user:pass@tcp(host:port)/database?charset=utf8mb4&parseTime=true&loc=UTC&timeout=10s&readTimeout=30s&writeTimeout=30s
```

- `parseTime=true` makes the driver return `time.Time` (GORM requirement).
- `loc=UTC` keeps every timestamp in UTC; the UI converts to the browser zone.
- Connection pool: 30 open / 5 idle connections, 30 min max lifetime.
- Boot retries for up to 90 s with exponential backoff (1 s -> 8 s), so the
  container can start together with its database.

Minimal provisioning on the database server:

```sql
CREATE DATABASE up CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
CREATE USER 'up'@'%' IDENTIFIED BY 'secret';
GRANT ALL PRIVILEGES ON up.* TO 'up'@'%';
FLUSH PRIVILEGES;
```

## 2. Tables at a glance

| Table | Purpose | Rows scale |
|---|---|---|
| `settings` | key/value runtime configuration shared by every node | tiny |
| `monitors` | probe definitions (type specific options in a JSON column) | tens |
| `monitor_notifications` | monitor <-> channel links | tens |
| `monitor_groups` | named collections of monitors (`uuid` + unique name) | tens |
| `monitor_group_members` | monitor <-> group links (a monitor can be in many) | tens |
| `monitor_templates` | reusable monitor blueprints (uuid + unique name) | tens |
| `monitor_certificates` | TLS certificate read by the probe + notification memory | tens |
| `monitor_domains` | registrable domain expiration (manual / RDAP / WHOIS) + notification memory | tens |
| `whois_parsers` | per-TLD rules that read an expiry date from a raw WHOIS response | few |
| `monitor_states` | aggregated status per monitor (transition detection, cluster wide) | one per monitor |
| `heartbeats` | one row per check per node (the big table) | millions |
| `notifications` | SMTP / Webhook channels | few |
| `notification_logs` | delivery history (success/failure + reason); kept forever unless `NOTIFICATION_LOG_RETENTION_DAYS` is set | thousands |
| `notification_locks` | de-duplication lock for ANY_WITH_LOCK; pruned after 24 h | thousands (60 s window) |
| `nodes` | cluster members, liveness and role | few |
| `cluster_settings` | failure / node-unavailable / sender strategies (single row) | 1 |
| `sync_peers` | this node's own view of its peers: liveness, `online_since` (the settle input), the outbox cursor and the last manifest result | few |
| `sync_outbox` | the change log of this node (one row per local write to a synchronised entity) | thousands, pruned after `CLUSTER_SYNC_TOMBSTONE_DAYS` |
| `sync_objects` | uuid -> local row mapping, the applied revision and the tombstones | one per synchronised row |
| `sync_conflicts` | a concurrent edit that lost the last-writer-wins merge | few |
| `sync_pending_links` | a reference that arrived before its target (retried, never pruned) | few |
| `sync_dead_letters` | a change that could not be applied, past the attempt cap it is skipped | few |
| `peer_votes` | the latest verdict of each peer per monitor uuid (federated voting input) | nodes x monitors |
| `status_pages` | public status pages | few |
| `status_page_monitors` | monitor selection and ordering per page | tens |
| `status_page_groups` | monitor groups included in a page (membership driven) | tens |
| `api_tokens` | public API bearer tokens (hashed) | few |
| `ip_rules` | allow/deny list by scope | few |

Every table that federated clustering has to synchronise (`monitors`,
`status_pages`, `monitor_groups`, `monitor_templates`) carries three extra
columns: `uuid`, `origin_node_id` and `revision` (see
[clustering-modes.md](clustering-modes.md)). The `uuid` is the global
identity of a row - the auto-increment `id` is only meaningful inside one
database - and `origin_node_id` / `revision` are the two inputs of the
last-writer-wins merge. They are filled by the `BeforeCreate` hook of the model
(`uuid`, `revision`) and by `database.Backfill` (`origin_node_id`). The
synchronisation tables above are created by `AutoMigrate` on **every** install,
but only a node running `CLUSTER_MODE=federated` writes and reads them; in the
default `shared` mode they stay empty.

### Expiration columns and tables

`monitors` gained, next to the certificate switches, the domain counterpart:
`domain_watch`, `domain_notify`, `domain_warn_days` and the nullable
`domain_expires_at` (the manually typed date).

`monitor_domains` mirrors `monitor_certificates`:
`monitor_id` (PK), `domain`, `registrar`, `expires_at`, `source`
(`manual`/`rdap`/`whois`), `status` (`ok`/`not_found`/`unsupported`/`error`),
`error`, `days_left`, `checked_at`, `checked_by_node`, `notified_days` and
`last_notified_day`. The last two are the notification memory and are preserved
by every rewrite, exactly like the certificate table.

`whois_parsers` stores one rule per TLD: `tld` (unique, e.g. `br` or `com.br`),
`server` (optional registry override), `expiry_regex` (RE2 with a capture group),
`date_layouts` (`;` separated Go layouts), `not_found_pattern`, `min_interval_ms`
(per-registry rate limit override), `enabled` and `note`.

**Every value that comes from a remote server is clipped before the insert.**
The columns are dimensioned for the widest realistic answer and the observation
is trimmed to fit instead of being rejected: `monitor_certificates.dns_names` is
a `TEXT` (a CDN certificate lists hundreds of subject alternative names, well
past a `varchar(500)`), a heartbeat message is trimmed to 500 characters, and
the domain/registrar/error values are bounded. Without that rule a single
oversized payload fails the whole insert with `Data too long for column` and
costs the observation *and* its notification (this is exactly what happened to a
`*.google.com` certificate before `dns_names` was widened).

## 3. Complete DDL (dumped from a live instance running MariaDB 11.8)

```sql
CREATE TABLE `api_tokens` (
  `id` bigint(20) unsigned NOT NULL AUTO_INCREMENT,
  `name` varchar(150) NOT NULL,
  `prefix` varchar(24) NOT NULL,
  `token_hash` varchar(64) NOT NULL,
  `scopes` varchar(255) NOT NULL DEFAULT 'read',
  `expires_at` datetime(3) DEFAULT NULL,
  `last_used_at` datetime(3) DEFAULT NULL,
  `last_used_ip` varchar(64) DEFAULT NULL,
  `revoked_at` datetime(3) DEFAULT NULL,
  `created_at` datetime(3) DEFAULT NULL,
  PRIMARY KEY (`id`),
  KEY `idx_api_tokens_prefix` (`prefix`),
  KEY `idx_api_tokens_token_hash` (`token_hash`)
) ENGINE=InnoDB AUTO_INCREMENT=2 DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_uca1400_ai_ci

CREATE TABLE `cluster_settings` (
  `id` bigint(20) unsigned NOT NULL AUTO_INCREMENT,
  `failure_strategy` varchar(32) NOT NULL DEFAULT 'ALL_NODES_FAIL',
  `node_unavailable_strategy` varchar(32) NOT NULL DEFAULT 'IGNORE',
  `notification_sender` varchar(32) NOT NULL DEFAULT 'ANY_WITH_LOCK',
  `updated_at` datetime(3) DEFAULT NULL,
  PRIMARY KEY (`id`)
) ENGINE=InnoDB AUTO_INCREMENT=2 DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_uca1400_ai_ci

CREATE TABLE `heartbeats` (
  `id` bigint(20) unsigned NOT NULL AUTO_INCREMENT,
  `monitor_id` bigint(20) unsigned NOT NULL,
  `node_id` varchar(64) DEFAULT NULL,
  `status` bigint(20) NOT NULL,
  `latency_ms` bigint(20) DEFAULT NULL,
  `status_code` bigint(20) DEFAULT NULL,
  `message` varchar(500) DEFAULT NULL,
  `important` tinyint(1) NOT NULL DEFAULT 0,
  `created_at` datetime(3) DEFAULT NULL,
  PRIMARY KEY (`id`),
  KEY `idx_hb_monitor_time` (`monitor_id`,`created_at`),
  KEY `idx_hb_monitor_node` (`monitor_id`,`node_id`,`created_at`),
  KEY `idx_heartbeats_status` (`status`)
) ENGINE=InnoDB AUTO_INCREMENT=1636 DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_uca1400_ai_ci

CREATE TABLE `ip_rules` (
  `id` bigint(20) unsigned NOT NULL AUTO_INCREMENT,
  `cidr` varchar(64) NOT NULL,
  `action` varchar(10) NOT NULL,
  `scope` varchar(20) NOT NULL DEFAULT 'all',
  `note` varchar(255) DEFAULT NULL,
  `enabled` tinyint(1) NOT NULL DEFAULT 1,
  `created_at` datetime(3) DEFAULT NULL,
  `updated_at` datetime(3) DEFAULT NULL,
  PRIMARY KEY (`id`),
  KEY `idx_ip_rules_action` (`action`),
  KEY `idx_ip_rules_scope` (`scope`)
) ENGINE=InnoDB AUTO_INCREMENT=3 DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_uca1400_ai_ci

CREATE TABLE `monitor_notifications` (
  `monitor_id` bigint(20) unsigned NOT NULL,
  `notification_id` bigint(20) unsigned NOT NULL,
  `on_down` tinyint(1) NOT NULL DEFAULT 1,
  `on_up` tinyint(1) NOT NULL DEFAULT 1,
  PRIMARY KEY (`monitor_id`,`notification_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_uca1400_ai_ci

CREATE TABLE `monitor_states` (
  `monitor_id` bigint(20) unsigned NOT NULL AUTO_INCREMENT,
  `status` varchar(20) NOT NULL DEFAULT 'unknown',
  `changed_at` datetime(3) DEFAULT NULL,
  `notified` varchar(20) DEFAULT NULL,
  `notified_at` datetime(3) DEFAULT NULL,
  `last_message` varchar(500) DEFAULT NULL,
  `last_latency` bigint(20) DEFAULT NULL,
  `last_check_at` datetime(3) DEFAULT NULL,
  `updated_at` datetime(3) DEFAULT NULL,
  PRIMARY KEY (`monitor_id`)
) ENGINE=InnoDB AUTO_INCREMENT=11 DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_uca1400_ai_ci

CREATE TABLE `monitor_certificates` (
  `monitor_id` bigint(20) unsigned NOT NULL,
  `subject` varchar(255) DEFAULT NULL,
  `issuer` varchar(255) DEFAULT NULL,
  `serial` varchar(120) DEFAULT NULL,
  `not_before` datetime(3) DEFAULT NULL,
  `not_after` datetime(3) DEFAULT NULL,
  `dns_names` text DEFAULT NULL,
  `days_left` bigint(20) DEFAULT NULL,
  `captured_at` datetime(3) DEFAULT NULL,
  `captured_by_node` varchar(64) DEFAULT NULL,
  `notified_days` varchar(120) DEFAULT NULL,
  `last_notified_day` bigint(20) DEFAULT NULL,
  PRIMARY KEY (`monitor_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_uca1400_ai_ci

CREATE TABLE `monitor_templates` (
  `id` bigint(20) unsigned NOT NULL AUTO_INCREMENT,
  `uuid` varchar(36) NOT NULL,
  `origin_node_id` varchar(64) DEFAULT NULL,
  `revision` bigint(20) NOT NULL DEFAULT 1,
  `name` varchar(150) NOT NULL,
  `description` varchar(500) DEFAULT NULL,
  `type` varchar(20) NOT NULL,
  `config` json DEFAULT NULL,
  `defaults` json DEFAULT NULL,
  `created_at` datetime(3) DEFAULT NULL,
  `updated_at` datetime(3) DEFAULT NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `idx_monitor_templates_uuid` (`uuid`),
  UNIQUE KEY `idx_monitor_templates_name` (`name`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_uca1400_ai_ci

CREATE TABLE `monitor_groups` (
  `id` bigint(20) unsigned NOT NULL AUTO_INCREMENT,
  `uuid` varchar(36) NOT NULL,
  `origin_node_id` varchar(64) DEFAULT NULL,
  `revision` bigint(20) NOT NULL DEFAULT 1,
  `name` varchar(150) NOT NULL,
  `description` varchar(500) DEFAULT NULL,
  `color` varchar(20) DEFAULT NULL,
  `sort_order` bigint(20) NOT NULL DEFAULT 0,
  `created_at` datetime(3) DEFAULT NULL,
  `updated_at` datetime(3) DEFAULT NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `idx_monitor_groups_uuid` (`uuid`),
  UNIQUE KEY `idx_monitor_groups_name` (`name`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_uca1400_ai_ci

CREATE TABLE `monitor_group_members` (
  `group_id` bigint(20) unsigned NOT NULL,
  `monitor_id` bigint(20) unsigned NOT NULL,
  `sort_order` bigint(20) NOT NULL DEFAULT 0,
  PRIMARY KEY (`group_id`,`monitor_id`),
  KEY `idx_monitor_group_members_group_id` (`group_id`),
  KEY `idx_monitor_group_members_monitor_id` (`monitor_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_uca1400_ai_ci

CREATE TABLE `monitors` (
  `id` bigint(20) unsigned NOT NULL AUTO_INCREMENT,
  `uuid` varchar(36) DEFAULT NULL,
  `origin_node_id` varchar(64) DEFAULT NULL,
  `revision` bigint(20) NOT NULL DEFAULT 1,
  `name` varchar(200) NOT NULL,
  `type` varchar(20) NOT NULL,
  `active` tinyint(1) NOT NULL DEFAULT 1,
  `description` varchar(500) DEFAULT NULL,
  `interval_seconds` bigint(20) NOT NULL DEFAULT 60,
  `retries` bigint(20) NOT NULL DEFAULT 0,
  `retries_interval_seconds` bigint(20) NOT NULL DEFAULT 60,
  `timeout_seconds` bigint(20) NOT NULL DEFAULT 10,
  `resend_interval_seconds` bigint(20) NOT NULL DEFAULT 0,
  `upside_down` tinyint(1) NOT NULL DEFAULT 0,
  `run_on` varchar(20) NOT NULL DEFAULT 'all',
  `node_id` varchar(64) DEFAULT NULL,
  `tags` varchar(255) DEFAULT NULL,
  `config` longtext CHARACTER SET utf8mb4 COLLATE utf8mb4_bin DEFAULT NULL CHECK (json_valid(`config`)),
  `created_at` datetime(3) DEFAULT NULL,
  `updated_at` datetime(3) DEFAULT NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `idx_monitors_uuid` (`uuid`),
  KEY `idx_monitors_type` (`type`)
) ENGINE=InnoDB AUTO_INCREMENT=11 DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_uca1400_ai_ci

CREATE TABLE `nodes` (
  `id` bigint(20) unsigned NOT NULL AUTO_INCREMENT,
  `name` varchar(150) NOT NULL,
  `node_id` varchar(64) NOT NULL,
  `api_url` varchar(255) DEFAULT NULL,
  `private_key_hash` varchar(128) DEFAULT NULL,
  `last_heartbeat` datetime(3) DEFAULT NULL,
  `status` varchar(20) NOT NULL DEFAULT 'offline',
  `is_primary` tinyint(1) NOT NULL DEFAULT 0,
  `created_at` datetime(3) DEFAULT NULL,
  `updated_at` datetime(3) DEFAULT NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `idx_nodes_node_id` (`node_id`)
) ENGINE=InnoDB AUTO_INCREMENT=7 DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_uca1400_ai_ci

CREATE TABLE `notification_locks` (
  `id` bigint(20) unsigned NOT NULL AUTO_INCREMENT,
  `monitor_id` bigint(20) unsigned NOT NULL,
  `event` varchar(24) NOT NULL,
  `bucket` bigint(20) NOT NULL,
  `node_id` varchar(64) DEFAULT NULL,
  `created_at` datetime(3) DEFAULT NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `idx_notification_lock` (`monitor_id`,`event`,`bucket`)
) ENGINE=InnoDB AUTO_INCREMENT=8 DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_uca1400_ai_ci

CREATE TABLE `notification_logs` (
  `id` bigint(20) unsigned NOT NULL AUTO_INCREMENT,
  `notification_id` bigint(20) unsigned NOT NULL,
  `monitor_id` bigint(20) unsigned DEFAULT NULL,
  `event` varchar(24) NOT NULL,
  `success` tinyint(1) NOT NULL DEFAULT 0,
  `error` varchar(1000) DEFAULT NULL,
  `duration_ms` bigint(20) DEFAULT NULL,
  `node_id` varchar(64) DEFAULT NULL,
  `created_at` datetime(3) DEFAULT NULL,
  PRIMARY KEY (`id`),
  KEY `idx_notification_logs_notification_id` (`notification_id`),
  KEY `idx_notification_logs_monitor_id` (`monitor_id`)
) ENGINE=InnoDB AUTO_INCREMENT=17 DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_uca1400_ai_ci

CREATE TABLE `notifications` (
  `id` bigint(20) unsigned NOT NULL AUTO_INCREMENT,
  `name` varchar(150) NOT NULL,
  `type` varchar(20) NOT NULL,
  `active` tinyint(1) NOT NULL DEFAULT 1,
  `is_default` tinyint(1) NOT NULL DEFAULT 0,
  `resend_interval_seconds` bigint(20) NOT NULL DEFAULT 0,
  `config` longtext CHARACTER SET utf8mb4 COLLATE utf8mb4_bin DEFAULT NULL CHECK (json_valid(`config`)),
  `created_at` datetime(3) DEFAULT NULL,
  `updated_at` datetime(3) DEFAULT NULL,
  PRIMARY KEY (`id`),
  KEY `idx_notifications_type` (`type`)
) ENGINE=InnoDB AUTO_INCREMENT=3 DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_uca1400_ai_ci

CREATE TABLE `settings` (
  `setting_key` varchar(64) NOT NULL,
  `value` text DEFAULT NULL,
  `updated_at` datetime(3) DEFAULT NULL,
  PRIMARY KEY (`setting_key`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_uca1400_ai_ci

CREATE TABLE `status_page_monitors` (
  `id` bigint(20) unsigned NOT NULL AUTO_INCREMENT,
  `status_page_id` bigint(20) unsigned NOT NULL,
  `monitor_id` bigint(20) unsigned NOT NULL,
  `display_name` varchar(200) DEFAULT NULL,
  `group_name` varchar(120) DEFAULT NULL,
  `sort_order` bigint(20) NOT NULL DEFAULT 0,
  `show_uptime` tinyint(1) NOT NULL DEFAULT 1,
  `show_chart` tinyint(1) NOT NULL DEFAULT 1,
  PRIMARY KEY (`id`),
  UNIQUE KEY `idx_status_page_monitor` (`status_page_id`,`monitor_id`)
) ENGINE=InnoDB AUTO_INCREMENT=5 DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_uca1400_ai_ci

CREATE TABLE `status_pages` (
  `id` bigint(20) unsigned NOT NULL AUTO_INCREMENT,
  `uuid` varchar(36) DEFAULT NULL,
  `origin_node_id` varchar(64) DEFAULT NULL,
  `revision` bigint(20) NOT NULL DEFAULT 1,
  `slug` varchar(120) NOT NULL,
  `title` varchar(200) NOT NULL,
  `description` varchar(500) DEFAULT NULL,
  `footer_text` varchar(500) DEFAULT NULL,
  `theme` varchar(20) NOT NULL DEFAULT 'system',
  `is_public` tinyint(1) NOT NULL DEFAULT 1,
  `show_uptime` tinyint(1) NOT NULL DEFAULT 1,
  `show_charts` tinyint(1) NOT NULL DEFAULT 1,
  `show_tags` tinyint(1) NOT NULL DEFAULT 0,
  `custom_css` text DEFAULT NULL,
  `created_at` datetime(3) DEFAULT NULL,
  `updated_at` datetime(3) DEFAULT NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `idx_status_pages_slug` (`slug`),
  UNIQUE KEY `idx_status_pages_uuid` (`uuid`)
) ENGINE=InnoDB AUTO_INCREMENT=2 DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_uca1400_ai_ci
```

Notes on the generated DDL:

- `settings.setting_key` is named that way because `key` is a reserved word in
  MySQL/MariaDB and would need backticks in every query.
- `ip_rules.cidr` is pinned with an explicit column tag: the default snake_case
  conversion would produce `c_id_r`.
- `monitors.config` is a JSON column holding one flat `MonitorConfig` struct
  (`internal/models/monitor_config.go`), serialized by GORM with
  `serializer:json`.
- Every timestamp is stored in UTC (`loc=UTC` in the DSN).
- `monitors.uuid` and `status_pages.uuid` are **nullable** while
  `monitor_groups.uuid` and `monitor_templates.uuid` are `NOT NULL`. That is not
  an oversight: the first two columns were added to a table that already had
  rows, and MySQL/MariaDB reject a unique index over several empty strings. A
  NULL is not compared by a unique index, so `AutoMigrate` can add the column and
  the index in one pass, and `database.Backfill` fills the NULLs right after it
  (see [clustering-modes.md](clustering-modes.md)). The `BeforeCreate`
  hook of both models means a row created by the application is never NULL.
- `origin_node_id` and `revision` are appended at the **end** of an existing
  table by `AutoMigrate`, while a fresh install creates them right after `id`.
  The column order has no functional meaning; the DDL above is from a fresh
  install.

### 3.1 Synchronisation tables (federated mode)

`AutoMigrate` creates these on **every** instance; only a node running
`CLUSTER_MODE=federated` writes and reads them (see
[clustering-modes.md](clustering-modes.md)).

| Table | Columns |
|---|---|
| `sync_outbox` | `id`, `entity`, `uuid`, `action` (`upsert`/`delete`), `origin_node_id` (the editor), `revision`, `payload` (JSON, null on delete), `payload_hash`, `created_at`, `updated_at` (the version timestamp the merge compares) |
| `sync_objects` | `uuid` (PK), `entity`, `local_id`, `origin_node_id`, `revision`, `deleted_at` (tombstone), `payload` (last applied wire body), `updated_at` |
| `sync_peers` | `peer_node_id` (PK), `peer_name`, `peer_api_url`, `peer_version`, `protocol_version`, `status`, `last_seen_at`, `last_success_at`, `online_since`, `last_change_id`, `last_manifest_at`, `last_manifest_ok`, `last_error`, `last_error_at`, `updated_at` |
| `sync_conflicts` | `id`, `entity`, `uuid`, `kept_origin`, `kept_revision`, `lost_origin`, `lost_revision`, `detected_at` |
| `sync_pending_links` | `entity` + `uuid` + `kind` + `missing_uuid` (unique), the reference that arrived before its target, with `attempts`, `last_attempt_at`, `created_at` |
| `sync_dead_letters` | `peer_node_id` + `change_id` (unique), `entity`, `uuid`, `payload_hash`, `attempts`, `skipped`, `last_error`, `created_at`, `updated_at` |
| `peer_votes` | `monitor_uuid` + `node_id` (unique), `status`, `latency_ms`, `message`, `important`, `checked_at`, `fetched_at` |

## 4. `monitors.config` (JSON) fields

| Field | Applies to | Meaning |
|---|---|---|
| `url`, `method`, `encoding`, `body`, `headers[]` | http, keyword | request definition (`headers` is a `[{key,value}]` list) |
| `auth_type`, `basic_user`, `basic_pass`, `bearer_token` | http, keyword | `none` / `basic` / `bearer` |
| `ignore_tls`, `max_redirects`, `cache_buster`, `accepted_status_codes` | http, keyword | TLS bypass, redirect limit, random cache buster parameter, `200-299,301` ranges |
| `keyword`, `invert_keyword`, `case_sensitive` | keyword | response body match options |
| `host`, `port`, `send`, `expect` | tcp | connect, optional payload and expected answer |
| `hostname`, `resolver_server`, `record_type`, `expected_value`, `invert_check` | dns | A/AAAA/CNAME/MX/TXT/NS/SOA query through a chosen resolver |

## 5. Queries the application runs

The dashboard avoids N+1 queries; these are the statements behind it.

**24h/7d/30d windows + latency, one query for the whole list**
(`internal/services/stats.go`):

```sql
SELECT monitor_id,
       SUM(CASE WHEN status = 1 AND created_at >= ? THEN 1 ELSE 0 END)   AS up_24h,
       SUM(CASE WHEN created_at >= ? THEN 1 ELSE 0 END)                  AS total_24h,
       SUM(CASE WHEN status = 0 AND created_at >= ? THEN 1 ELSE 0 END)   AS down_24h,
       SUM(CASE WHEN status = 2 AND created_at >= ? THEN 1 ELSE 0 END)   AS pending_24h,
       SUM(CASE WHEN status = 1 AND created_at >= ? THEN 1 ELSE 0 END)   AS up_7d,
       SUM(CASE WHEN created_at >= ? THEN 1 ELSE 0 END)                  AS total_7d,
       SUM(CASE WHEN status = 1 AND created_at >= ? THEN 1 ELSE 0 END)   AS up_30d,
       SUM(CASE WHEN created_at >= ? THEN 1 ELSE 0 END)                  AS total_30d,
       AVG(CASE WHEN status = 1 AND created_at >= ? THEN latency_ms END) AS avg_ms_24h,
       MIN(CASE WHEN created_at >= ? THEN latency_ms END)                AS min_ms_24h,
       MAX(CASE WHEN created_at >= ? THEN latency_ms END)                AS max_ms_24h
FROM heartbeats
WHERE created_at >= ? AND monitor_id IN (?)
GROUP BY monitor_id;
```

**Latest heartbeat per monitor** (`internal/services/stats_series.go`):

```sql
SELECT h.* FROM heartbeats h
JOIN (SELECT monitor_id, MAX(id) AS max_id
      FROM heartbeats WHERE monitor_id IN (?) GROUP BY monitor_id) latest
  ON latest.max_id = h.id;
```

**Latest heartbeat per monitor and node** (cluster voting and the per-node
breakdown in the UI):

```sql
SELECT h.* FROM heartbeats h
JOIN (SELECT monitor_id, node_id, MAX(id) AS max_id
      FROM heartbeats WHERE monitor_id IN (?) GROUP BY monitor_id, node_id) latest
  ON latest.max_id = h.id
ORDER BY h.monitor_id, h.node_id;
```

**Retention** (`HEARTBEAT_RETENTION_DAYS > 0`, executed every 6 h):

```sql
DELETE FROM heartbeats WHERE created_at < ?;
```

Choosing the window: a monitor stores one row per check **per node**, so a 60 s
interval produces ~1 440 rows/day and a cluster of two nodes doubles that. A 90
day window keeps the charts of the dashboard complete while bounding the table
(10 monitors on 2 nodes ≈ 2.6 M rows instead of growing forever); `0` keeps every
heartbeat, which is the default.
