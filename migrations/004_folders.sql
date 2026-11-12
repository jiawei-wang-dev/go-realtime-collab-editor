CREATE TABLE IF NOT EXISTS folders (
    id VARCHAR(64) NOT NULL,
    owner_id VARCHAR(255) NOT NULL,
    parent_id VARCHAR(64) NULL,
    name VARCHAR(255) NOT NULL,
    parent_key VARCHAR(64) GENERATED ALWAYS AS (COALESCE(parent_id, '')) STORED,
    created_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    updated_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
    PRIMARY KEY (id),
    UNIQUE KEY uq_folders_owner_parent_name (owner_id, parent_key, name),
    KEY idx_folders_parent (parent_id),
    CONSTRAINT fk_folders_parent FOREIGN KEY (parent_id) REFERENCES folders(id) ON DELETE RESTRICT
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE IF NOT EXISTS folder_permissions (
    folder_id VARCHAR(64) NOT NULL,
    user_id VARCHAR(255) NOT NULL,
    role ENUM('write', 'read') NOT NULL,
    created_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    updated_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
    PRIMARY KEY (folder_id, user_id),
    KEY idx_folder_permissions_user (user_id),
    CONSTRAINT fk_folder_permissions_folder FOREIGN KEY (folder_id) REFERENCES folders(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

ALTER TABLE documents ADD COLUMN folder_id VARCHAR(64) NULL;
ALTER TABLE documents ADD COLUMN display_name VARCHAR(255) NULL;
ALTER TABLE documents ADD KEY idx_documents_folder (folder_id);
ALTER TABLE documents ADD CONSTRAINT fk_documents_folder FOREIGN KEY (folder_id) REFERENCES folders(id) ON DELETE RESTRICT;
