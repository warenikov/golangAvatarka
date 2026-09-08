-- +goose Up
CREATE TABLE avatars (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id           VARCHAR(255) NOT NULL,
    file_name         VARCHAR(255) NOT NULL,
    mime_type         VARCHAR(100) NOT NULL,
    size_bytes        BIGINT       NOT NULL,
    width             INT,
    height            INT,
    s3_key            VARCHAR(500) NOT NULL,
    thumbnail_s3_keys JSONB        NOT NULL DEFAULT '{}'::jsonb,
    upload_status     VARCHAR(50)  NOT NULL DEFAULT 'uploading',
    processing_status VARCHAR(50)  NOT NULL DEFAULT 'pending',
    created_at        TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at        TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    deleted_at        TIMESTAMPTZ
);

CREATE INDEX idx_avatars_user_id
    ON avatars (user_id, created_at DESC)
    WHERE deleted_at IS NULL;

CREATE INDEX idx_avatars_status
    ON avatars (upload_status, processing_status);

-- +goose Down
DROP INDEX IF EXISTS idx_avatars_status;
DROP INDEX IF EXISTS idx_avatars_user_id;
DROP TABLE IF EXISTS avatars;
