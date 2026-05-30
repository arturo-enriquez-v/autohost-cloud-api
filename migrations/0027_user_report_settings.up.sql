CREATE TABLE user_report_settings (
  user_id    UUID PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
  enabled    BOOLEAN  NOT NULL DEFAULT false,
  hour_utc   SMALLINT NOT NULL DEFAULT 8 CHECK (hour_utc >= 0 AND hour_utc <= 23),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
