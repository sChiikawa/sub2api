CREATE TABLE IF NOT EXISTS astra_gateway_pool (
    host VARCHAR(200) NOT NULL,
    account_id BIGINT NOT NULL DEFAULT 0,
    account_name VARCHAR(80) NOT NULL DEFAULT '',
    cookies TEXT NOT NULL,
    saved_at TIMESTAMPTZ NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    credential_expires_at TIMESTAMPTZ,
    PRIMARY KEY (host, account_id, cookies)
);

CREATE TABLE IF NOT EXISTS astra_gateway_turns (
    account_id BIGINT PRIMARY KEY,
    host VARCHAR(200) NOT NULL,
    cookie_key TEXT NOT NULL,
    until_at TIMESTAMPTZ NOT NULL,
    used_hosts TEXT NOT NULL DEFAULT ''
);
