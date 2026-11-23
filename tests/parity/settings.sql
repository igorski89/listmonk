-- Settings for the parity session, applied after install, before start.
UPDATE settings SET value = '"http://127.0.0.1:9123"' WHERE key = 'app.root_url';
UPDATE settings SET value = 'false' WHERE key = 'app.check_updates';
UPDATE settings SET value = 'true' WHERE key = 'privacy.individual_tracking';
UPDATE settings SET value = 'true' WHERE key = 'bounce.webhooks_enabled';
UPDATE settings SET value = 'true' WHERE key = 'bounce.enabled';
UPDATE settings SET value = '1000' WHERE key = 'app.message_rate';
UPDATE settings SET value = '1' WHERE key = 'app.concurrency';
UPDATE settings SET value = '[{"enabled":true, "exclude_from_default":false, "host":"127.0.0.1","port":51025,"auth_protocol":"none","username":"","password":"","hello_hostname":"","max_conns":1,"idle_timeout":"15s","wait_timeout":"5s","max_msg_retries":0,"tls_type":"none","tls_skip_verify":false,"email_headers":[]}]' WHERE key = 'smtp';
-- From upstream after v7.0.0-beta.2: the public subscription API (used by
-- the session's public sign-up) is off by default. No-op on beta.2.
UPDATE settings SET value = 'true' WHERE key = 'app.enable_public_subscription_api';
