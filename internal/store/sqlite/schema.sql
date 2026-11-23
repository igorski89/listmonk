-- listmonk schema for SQLite. Baseline: upstream schema.sql as of tag
-- v7.0.0-beta.2 (recorded as schema version v7.0.0, like upstream's
-- install). Upstream's v7.0.0 migration may still change before the final
-- release: on rebase, diff it and add an idempotent SQLite twin.
-- Translation rules:
--   SERIAL -> INTEGER PRIMARY KEY AUTOINCREMENT (ids are never reused, as
--   with Postgres sequences); uuid -> TEXT; enums -> TEXT + CHECK;
--   JSONB -> TEXT (JSON); arrays -> TEXT (JSON array); timestamps ->
--   TIMESTAMP text in UTC "YYYY-MM-DD HH:MM:SS.fff+00:00", which sorts in
--   time order. Materialized views are computed live (no tables).

-- subscribers
CREATE TABLE subscribers (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    uuid            TEXT NOT NULL UNIQUE,
    email           TEXT NOT NULL UNIQUE,
    name            TEXT NOT NULL,
    attribs         TEXT NOT NULL DEFAULT '{}',
    status          TEXT NOT NULL DEFAULT 'enabled' CHECK (status IN ('enabled', 'disabled', 'blocklisted')),

    created_at      TIMESTAMP DEFAULT (strftime('%Y-%m-%d %H:%M:%f+00:00', 'now')),
    updated_at      TIMESTAMP DEFAULT (strftime('%Y-%m-%d %H:%M:%f+00:00', 'now'))
);
CREATE UNIQUE INDEX idx_subs_email ON subscribers(LOWER(email));
CREATE INDEX idx_subs_status ON subscribers(status);
CREATE INDEX idx_subs_id_status ON subscribers(id, status);
CREATE INDEX idx_subs_created_at ON subscribers(created_at);
CREATE INDEX idx_subs_updated_at ON subscribers(updated_at);

-- lists
CREATE TABLE lists (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    uuid            TEXT NOT NULL UNIQUE,
    name            TEXT NOT NULL,
    type            TEXT NOT NULL CHECK (type IN ('public', 'private', 'temporary')),
    optin           TEXT NOT NULL DEFAULT 'single' CHECK (optin IN ('single', 'double')),
    status          TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'archived')),
    tags            TEXT NOT NULL DEFAULT '[]',
    description     TEXT NOT NULL DEFAULT '',

    created_at      TIMESTAMP DEFAULT (strftime('%Y-%m-%d %H:%M:%f+00:00', 'now')),
    updated_at      TIMESTAMP DEFAULT (strftime('%Y-%m-%d %H:%M:%f+00:00', 'now'))
);
CREATE INDEX idx_lists_type ON lists(type);
CREATE INDEX idx_lists_optin ON lists(optin);
CREATE INDEX idx_lists_status ON lists(status);
CREATE INDEX idx_lists_name ON lists(name);
CREATE INDEX idx_lists_created_at ON lists(created_at);
CREATE INDEX idx_lists_updated_at ON lists(updated_at);

CREATE TABLE subscriber_lists (
    subscriber_id      INTEGER NOT NULL REFERENCES subscribers(id) ON DELETE CASCADE ON UPDATE CASCADE,
    list_id            INTEGER NOT NULL REFERENCES lists(id) ON DELETE CASCADE ON UPDATE CASCADE,
    meta               TEXT NOT NULL DEFAULT '{}',
    status             TEXT NOT NULL DEFAULT 'unconfirmed' CHECK (status IN ('unconfirmed', 'confirmed', 'unsubscribed')),

    created_at         TIMESTAMP DEFAULT (strftime('%Y-%m-%d %H:%M:%f+00:00', 'now')),
    updated_at         TIMESTAMP DEFAULT (strftime('%Y-%m-%d %H:%M:%f+00:00', 'now')),

    PRIMARY KEY(subscriber_id, list_id)
);
CREATE INDEX idx_sub_lists_sub_id ON subscriber_lists(subscriber_id);
CREATE INDEX idx_sub_lists_list_id ON subscriber_lists(list_id);
CREATE INDEX idx_sub_lists_status ON subscriber_lists(status);
CREATE INDEX idx_sub_lists_created_at ON subscriber_lists(created_at);
CREATE INDEX idx_sub_lists_unsubscribed_updated_at ON subscriber_lists(updated_at) WHERE status = 'unsubscribed';

-- templates
CREATE TABLE templates (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    name            TEXT NOT NULL,
    type            TEXT NOT NULL DEFAULT 'campaign' CHECK (type IN ('campaign', 'campaign_visual', 'tx')),
    subject         TEXT NOT NULL,
    body            TEXT NOT NULL,
    body_source     TEXT NULL,
    is_default      BOOLEAN NOT NULL DEFAULT false,

    created_at      TIMESTAMP DEFAULT (strftime('%Y-%m-%d %H:%M:%f+00:00', 'now')),
    updated_at      TIMESTAMP DEFAULT (strftime('%Y-%m-%d %H:%M:%f+00:00', 'now'))
);
CREATE UNIQUE INDEX idx_templates_default ON templates (is_default) WHERE is_default = true;

-- campaigns
CREATE TABLE campaigns (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    uuid             TEXT NOT NULL UNIQUE,
    name             TEXT NOT NULL,
    subject          TEXT NOT NULL,
    from_email       TEXT NOT NULL,
    body             TEXT NOT NULL,
    body_source      TEXT NULL,
    altbody          TEXT NULL,
    content_type     TEXT NOT NULL DEFAULT 'richtext' CHECK (content_type IN ('richtext', 'html', 'plain', 'markdown', 'visual')),
    send_at          TIMESTAMP NULL,
    headers          TEXT NOT NULL DEFAULT '[]',
    attribs          TEXT NOT NULL DEFAULT '{}',
    status           TEXT NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'running', 'scheduled', 'paused', 'cancelled', 'finished')),
    tags             TEXT NOT NULL DEFAULT '[]',
    type             TEXT DEFAULT 'regular' CHECK (type IN ('regular', 'optin')),
    messenger        TEXT NOT NULL,
    template_id      INTEGER NULL REFERENCES templates(id) ON DELETE SET NULL,

    to_send            INTEGER NOT NULL DEFAULT 0,
    sent               INTEGER NOT NULL DEFAULT 0,
    max_subscriber_id  INTEGER NOT NULL DEFAULT 0,
    last_subscriber_id INTEGER NOT NULL DEFAULT 0,

    archive             BOOLEAN NOT NULL DEFAULT false,
    archive_slug        TEXT NULL UNIQUE,
    archive_template_id INTEGER NULL REFERENCES templates(id) ON DELETE SET NULL,
    archive_meta        TEXT NOT NULL DEFAULT '{}',

    started_at       TIMESTAMP NULL,
    created_at       TIMESTAMP DEFAULT (strftime('%Y-%m-%d %H:%M:%f+00:00', 'now')),
    updated_at       TIMESTAMP DEFAULT (strftime('%Y-%m-%d %H:%M:%f+00:00', 'now'))
);
CREATE INDEX idx_camps_status ON campaigns(status);
CREATE INDEX idx_camps_name ON campaigns(name);
CREATE INDEX idx_camps_created_at ON campaigns(created_at);
CREATE INDEX idx_camps_updated_at ON campaigns(updated_at);

CREATE TABLE campaign_lists (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    campaign_id  INTEGER NOT NULL REFERENCES campaigns(id) ON DELETE CASCADE ON UPDATE CASCADE,
    list_id      INTEGER NULL REFERENCES lists(id) ON DELETE SET NULL ON UPDATE CASCADE,
    list_name    TEXT NOT NULL DEFAULT ''
);
CREATE UNIQUE INDEX idx_camp_lists ON campaign_lists (campaign_id, list_id);
CREATE INDEX idx_camp_lists_camp_id ON campaign_lists(campaign_id);
CREATE INDEX idx_camp_lists_list_id ON campaign_lists(list_id);

CREATE TABLE campaign_views (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    campaign_id      INTEGER NOT NULL REFERENCES campaigns(id) ON DELETE CASCADE ON UPDATE CASCADE,
    subscriber_id    INTEGER NULL REFERENCES subscribers(id) ON DELETE SET NULL ON UPDATE CASCADE,
    created_at       TIMESTAMP DEFAULT (strftime('%Y-%m-%d %H:%M:%f+00:00', 'now'))
);
CREATE INDEX idx_views_camp_id ON campaign_views(campaign_id);
CREATE INDEX idx_views_subscriber_id ON campaign_views(subscriber_id);
CREATE INDEX idx_views_date ON campaign_views(created_at);

-- media
CREATE TABLE media (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    uuid             TEXT NOT NULL UNIQUE,
    provider         TEXT NOT NULL DEFAULT '',
    filename         TEXT NOT NULL,
    content_type     TEXT NOT NULL DEFAULT 'application/octet-stream',
    thumb            TEXT NOT NULL,
    tags             TEXT NOT NULL DEFAULT '[]',
    meta             TEXT NOT NULL DEFAULT '{}',
    created_at       TIMESTAMP DEFAULT (strftime('%Y-%m-%d %H:%M:%f+00:00', 'now'))
);
CREATE INDEX idx_media_filename ON media(provider, filename);

CREATE TABLE campaign_media (
    campaign_id  INTEGER REFERENCES campaigns(id) ON DELETE CASCADE ON UPDATE CASCADE,
    media_id     INTEGER NULL REFERENCES media(id) ON DELETE SET NULL ON UPDATE CASCADE,
    filename     TEXT NOT NULL DEFAULT ''
);
CREATE UNIQUE INDEX idx_camp_media_id ON campaign_media (campaign_id, media_id);
CREATE INDEX idx_camp_media_camp_id ON campaign_media(campaign_id);

-- links
CREATE TABLE links (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    uuid             TEXT NOT NULL UNIQUE,
    url              TEXT NOT NULL UNIQUE,
    created_at       TIMESTAMP DEFAULT (strftime('%Y-%m-%d %H:%M:%f+00:00', 'now'))
);

CREATE TABLE link_clicks (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    campaign_id      INTEGER NULL REFERENCES campaigns(id) ON DELETE CASCADE ON UPDATE CASCADE,
    link_id          INTEGER NOT NULL REFERENCES links(id) ON DELETE CASCADE ON UPDATE CASCADE,
    subscriber_id    INTEGER NULL REFERENCES subscribers(id) ON DELETE SET NULL ON UPDATE CASCADE,
    created_at       TIMESTAMP DEFAULT (strftime('%Y-%m-%d %H:%M:%f+00:00', 'now'))
);
CREATE INDEX idx_clicks_camp_id ON link_clicks(campaign_id);
CREATE INDEX idx_clicks_link_id ON link_clicks(link_id);
CREATE INDEX idx_clicks_sub_id ON link_clicks(subscriber_id);
CREATE INDEX idx_clicks_date ON link_clicks(created_at);

-- settings
-- Fork: SQLite migration steps applied (internal/store/sqlite/migrations.go).
CREATE TABLE schema_migrations (
    name        TEXT NOT NULL PRIMARY KEY,
    applied_at  TIMESTAMP NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f+00:00', 'now'))
);

CREATE TABLE settings (
    key             TEXT NOT NULL PRIMARY KEY,
    value           TEXT NOT NULL DEFAULT '{}',
    updated_at      TIMESTAMP DEFAULT (strftime('%Y-%m-%d %H:%M:%f+00:00', 'now'))
);
INSERT INTO settings (key, value) VALUES
    ('app.site_name', '"Mailing list"'),
    ('app.root_url', '"http://localhost:9000"'),
    ('app.favicon_url', '""'),
    ('app.from_email', '["listmonk <noreply@listmonk.yoursite.com>"]'),
    ('app.logo_url', '""'),
    ('app.concurrency', '10'),
    ('app.message_rate', '10'),
    ('app.batch_size', '1000'),
    ('app.max_send_errors', '1000'),
    ('app.message_sliding_window', 'false'),
    ('app.message_sliding_window_duration', '"1h"'),
    ('app.message_sliding_window_rate', '10000'),
    ('app.cache_slow_queries', 'false'),
    ('app.cache_slow_queries_interval', '"0 3 * * *"'),
    ('app.enable_public_archive', 'true'),
    ('app.enable_public_subscription_page', 'true'),
    ('app.show_optin_page', 'true'),
    ('app.enable_public_archive_rss_content', 'true'),
    ('app.send_optin_confirmation', 'true'),
    ('app.check_updates', 'true'),
    ('app.notify_emails', '[]'),
    ('app.lang', '"en"'),
    ('privacy.individual_tracking', 'false'),
    ('privacy.disable_tracking', 'false'),
    ('privacy.unsubscribe_header', 'true'),
    ('privacy.allow_blocklist', 'true'),
    ('privacy.allow_export', 'true'),
    ('privacy.allow_wipe', 'true'),
    ('privacy.allow_preferences', 'true'),
    ('privacy.exportable', '["profile", "subscriptions", "campaign_views", "link_clicks"]'),
    ('privacy.domain_blocklist', '[]'),
    ('privacy.domain_allowlist', '[]'),
    ('privacy.record_optin_ip', 'false'),
    ('security.captcha', '{"altcha": {"enabled": false, "complexity": 300000}, "hcaptcha": {"enabled": false, "key": "", "secret": ""}}'),
    ('security.oidc', '{"enabled": false, "provider_url": "", "redirect_url": "", "provider_name": "", "client_id": "", "client_secret": "", "auto_create_users": false, "auto_sync_roles": false, "logout": false, "auto_login": false, "verify_email": true, "default_user_role_id": null, "default_list_role_id": null, "roles": []}'),
    ('security.trusted_urls', '[]'),
    ('security.session_duration', '"7d"'),
    ('upload.provider', '"filesystem"'),
    ('upload.max_file_size', '5000'),
    ('upload.extensions', '["jpg","jpeg","png","gif","svg","*"]'),
    ('upload.filesystem.upload_path', '"uploads"'),
    ('upload.filesystem.upload_uri', '"/uploads"'),
    ('upload.s3.url', '"https://ap-south-1.s3.amazonaws.com"'),
    ('upload.s3.public_url', '""'),
    ('upload.s3.aws_access_key_id', '""'),
    ('upload.s3.aws_secret_access_key', '""'),
    ('upload.s3.aws_default_region', '"ap-south-1"'),
    ('upload.s3.bucket', '""'),
    ('upload.s3.bucket_domain', '""'),
    ('upload.s3.bucket_path', '"/"'),
    ('upload.s3.bucket_type', '"public"'),
    ('upload.s3.expiry', '"167h"'),
    ('smtp',
        '[{"enabled":true, "exclude_from_default":false, "host":"smtp.yoursite.com","port":25,"auth_protocol":"cram","username":"username","password":"password","hello_hostname":"","max_conns":10,"idle_timeout":"15s","wait_timeout":"5s","max_msg_retries":2,"msg_retry_delay":"10ms","tls_type":"STARTTLS","tls_skip_verify":false,"email_headers":[], "from_addresses":[]},
          {"enabled":false, "exclude_from_default":false, "host":"smtp.gmail.com","port":465,"auth_protocol":"login","username":"username@gmail.com","password":"password","hello_hostname":"","max_conns":10,"idle_timeout":"15s","wait_timeout":"5s","max_msg_retries":2,"msg_retry_delay":"10ms","tls_type":"TLS","tls_skip_verify":false,"email_headers":[], "from_addresses":[]}]'),
    ('messengers', '[]'),
    ('bounce.enabled', 'false'),
    ('bounce.webhooks_enabled', 'false'),
    ('bounce.actions', '{"soft": {"count": 2, "action": "none"}, "hard": {"count": 1, "action": "blocklist"}, "complaint" : {"count": 1, "action": "blocklist"}}'),
    ('bounce.ses', '{"enabled": false, "topic_arns": []}'),
    ('bounce.azure', '{"enabled": false, "shared_secret": "", "shared_secret_header": ""}'),
    ('bounce.sendgrid', '{"enabled": false, "key": ""}'),
    ('bounce.postmark', '{"enabled": false, "username": "", "password": ""}'),
    ('bounce.forwardemail', '{"enabled": false, "key": ""}'),
    ('bounce.lettermint', '{"enabled": false, "key": ""}'),
    ('bounce.mailboxes',
        '[{"enabled":false, "type": "pop", "host":"pop.yoursite.com","port":995,"auth_protocol":"userpass","username":"username","password":"password","return_path": "bounce@listmonk.yoursite.com","scan_interval":"15m","tls_enabled":true,"tls_skip_verify":false}]'),
    ('appearance.admin.custom_css', '""'),
    ('appearance.admin.custom_js', '""'),
    ('appearance.public.custom_css', '""'),
    ('appearance.public.custom_js', '""'),
    ('maintenance.db', '{"vacuum": false, "vacuum_cron_interval": "0 2 * * *"}');

-- bounces
CREATE TABLE bounces (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    subscriber_id    INTEGER NOT NULL REFERENCES subscribers(id) ON DELETE CASCADE ON UPDATE CASCADE,
    campaign_id      INTEGER NULL REFERENCES campaigns(id) ON DELETE SET NULL ON UPDATE CASCADE,
    type             TEXT NOT NULL DEFAULT 'hard' CHECK (type IN ('soft', 'hard', 'complaint')),
    source           TEXT NOT NULL DEFAULT '',
    meta             TEXT NOT NULL DEFAULT '{}',
    created_at       TIMESTAMP DEFAULT (strftime('%Y-%m-%d %H:%M:%f+00:00', 'now'))
);
CREATE INDEX idx_bounces_sub_id ON bounces(subscriber_id);
CREATE INDEX idx_bounces_camp_id ON bounces(campaign_id);
CREATE INDEX idx_bounces_source ON bounces(source);
CREATE INDEX idx_bounces_date ON bounces(created_at);

-- roles
CREATE TABLE roles (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    type             TEXT NOT NULL DEFAULT 'user' CHECK (type IN ('user', 'list')),
    parent_id        INTEGER NULL REFERENCES roles(id) ON DELETE CASCADE ON UPDATE CASCADE,
    list_id          INTEGER NULL REFERENCES lists(id) ON DELETE CASCADE ON UPDATE CASCADE,
    permissions      TEXT NOT NULL DEFAULT '[]',
    name             TEXT NULL,
    created_at       TIMESTAMP DEFAULT (strftime('%Y-%m-%d %H:%M:%f+00:00', 'now')),
    updated_at       TIMESTAMP DEFAULT (strftime('%Y-%m-%d %H:%M:%f+00:00', 'now'))
);
CREATE UNIQUE INDEX idx_roles ON roles (parent_id, list_id);
CREATE UNIQUE INDEX idx_roles_name ON roles (type, name) WHERE name IS NOT NULL;

-- users
CREATE TABLE users (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    username         TEXT NOT NULL UNIQUE,
    password_login   BOOLEAN NOT NULL DEFAULT false,
    password         TEXT NULL,
    email            TEXT NOT NULL UNIQUE,
    name             TEXT NOT NULL,
    avatar           TEXT NULL,
    type             TEXT NOT NULL DEFAULT 'user' CHECK (type IN ('user', 'api')),
    user_role_id     INTEGER NOT NULL REFERENCES roles(id) ON DELETE RESTRICT,
    list_role_id     INTEGER NULL REFERENCES roles(id) ON DELETE SET NULL,
    status           TEXT NOT NULL DEFAULT 'disabled' CHECK (status IN ('enabled', 'disabled')),
    twofa_type       TEXT NOT NULL DEFAULT 'none' CHECK (twofa_type IN ('none', 'totp')),
    twofa_key        TEXT NULL,
    loggedin_at      TIMESTAMP NULL,
    created_at       TIMESTAMP DEFAULT (strftime('%Y-%m-%d %H:%M:%f+00:00', 'now')),
    updated_at       TIMESTAMP DEFAULT (strftime('%Y-%m-%d %H:%M:%f+00:00', 'now'))
);

-- user sessions
CREATE TABLE sessions (
    id          TEXT NOT NULL PRIMARY KEY,
    data        TEXT NOT NULL DEFAULT '{}',
    created_at  TIMESTAMP DEFAULT (strftime('%Y-%m-%d %H:%M:%f+00:00', 'now'))
);
CREATE INDEX idx_sessions ON sessions (id, created_at);
