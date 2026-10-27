-- +goose Up
-- Client-side exit orders for EVM swap positions, evaluated only while
-- 'oku swap orders watch' runs. Values are native-denominated decimals.
CREATE TABLE swap_orders (
 profile TEXT NOT NULL, order_id TEXT NOT NULL, agent TEXT NOT NULL,
 chain_id INTEGER NOT NULL, token TEXT NOT NULL, decimals INTEGER NOT NULL,
 amount TEXT NOT NULL, size TEXT NOT NULL,
 take_profit_pct TEXT NOT NULL DEFAULT '', stop_loss_pct TEXT NOT NULL DEFAULT '', trailing_stop_pct TEXT NOT NULL DEFAULT '',
 slippage_bps INTEGER NOT NULL,
 basis TEXT NOT NULL DEFAULT '', basis_source TEXT NOT NULL CHECK(basis_source IN ('journal','override','none')),
 state TEXT NOT NULL CHECK(state IN ('active','triggered','executing','filled','failed','cancelled')),
 peak_value TEXT NOT NULL DEFAULT '', last_price TEXT NOT NULL DEFAULT '', last_value TEXT NOT NULL DEFAULT '', last_quote_at TEXT NOT NULL DEFAULT '',
 trigger TEXT NOT NULL DEFAULT '' CHECK(trigger IN ('','take_profit','stop_loss','trailing_stop')),
 sell_intent_id TEXT NOT NULL DEFAULT '', needs_reconcile INTEGER NOT NULL DEFAULT 0, last_error TEXT NOT NULL DEFAULT '',
 attempts INTEGER NOT NULL DEFAULT 0,
 created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
 PRIMARY KEY(profile,order_id)
);
CREATE INDEX swap_orders_agent_state ON swap_orders(profile,agent,state);
-- +goose Down
DROP INDEX swap_orders_agent_state;
DROP TABLE swap_orders;
