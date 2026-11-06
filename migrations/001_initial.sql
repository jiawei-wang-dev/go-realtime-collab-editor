CREATE TABLE IF NOT EXISTS documents (
    id VARCHAR(255) NOT NULL,
    owner_id VARCHAR(255) NOT NULL,
    content LONGTEXT NOT NULL,
    version BIGINT UNSIGNED NOT NULL DEFAULT 0,
    created_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    updated_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
    PRIMARY KEY (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE IF NOT EXISTS document_permissions (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    document_id VARCHAR(255) NOT NULL,
    user_id VARCHAR(255) NOT NULL,
    role ENUM('owner', 'write', 'read') NOT NULL,
    created_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    updated_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
    PRIMARY KEY (id),
    UNIQUE KEY uq_document_permissions_document_user (document_id, user_id),
    CONSTRAINT fk_document_permissions_document
        FOREIGN KEY (document_id) REFERENCES documents(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE IF NOT EXISTS operation_logs (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    document_id VARCHAR(255) NOT NULL,
    version BIGINT UNSIGNED NOT NULL,
    request_id VARCHAR(255) NOT NULL,
    user_id VARCHAR(255) NOT NULL,
    base_version BIGINT UNSIGNED NOT NULL,
    operation_kind ENUM('insert', 'delete') NOT NULL,
    position BIGINT NOT NULL,
    text LONGTEXT NOT NULL,
    delete_length BIGINT NOT NULL DEFAULT 0,
    created_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    PRIMARY KEY (id),
    UNIQUE KEY uq_operation_logs_document_version (document_id, version),
    UNIQUE KEY uq_operation_logs_document_request (document_id, request_id),
    CONSTRAINT fk_operation_logs_document
        FOREIGN KEY (document_id) REFERENCES documents(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE IF NOT EXISTS snapshots (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    document_id VARCHAR(255) NOT NULL,
    version BIGINT UNSIGNED NOT NULL,
    content LONGTEXT NOT NULL,
    created_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    PRIMARY KEY (id),
    UNIQUE KEY uq_snapshots_document_version (document_id, version),
    CONSTRAINT fk_snapshots_document
        FOREIGN KEY (document_id) REFERENCES documents(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
