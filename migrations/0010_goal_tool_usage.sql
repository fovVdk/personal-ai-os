-- Phase 12: persist actual Core-observed Tool Calls in the per-Goal autonomy budget ledger.
-- Phase 12：在每 Goal 自主预算账本中持久化 Go Core 实际观察到的 Tool Call 数量。
ALTER TABLE model_usage
    ADD COLUMN tool_calls integer NOT NULL DEFAULT 0 CHECK (tool_calls >= 0);
