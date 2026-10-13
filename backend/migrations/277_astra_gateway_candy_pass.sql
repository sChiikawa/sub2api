CREATE TABLE IF NOT EXISTS astra_gateway_candy_pass (
    account_id BIGINT NOT NULL,
    host VARCHAR(200) NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    passed BOOLEAN NOT NULL DEFAULT FALSE,
    PRIMARY KEY (account_id, host)
);
