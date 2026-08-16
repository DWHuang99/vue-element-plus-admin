CREATE TABLE IF NOT EXISTS departments (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    parent_id BIGINT REFERENCES departments(id) ON DELETE RESTRICT,
    department_name VARCHAR(100) NOT NULL,
    status BOOLEAN NOT NULL DEFAULT TRUE,
    remark TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS departments_parent_id_idx ON departments(parent_id);

INSERT INTO departments (department_name, status, remark)
SELECT '总公司', TRUE, ''
WHERE NOT EXISTS (SELECT 1 FROM departments);
