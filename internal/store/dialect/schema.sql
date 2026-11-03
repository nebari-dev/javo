CREATE TABLE things (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    flag BOOLEAN NOT NULL,
    count BIGINT NOT NULL,
    doc TEXT NOT NULL,
    note TEXT,
    created_at BIGINT NOT NULL,
    seen_at BIGINT
);
