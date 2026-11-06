resource "random_password" "mysql" {
  length  = 32
  special = false
}

resource "google_secret_manager_secret" "mysql_password" {
  secret_id = "${var.name_prefix}-mysql-password"
  replication {
    auto {}
  }
  depends_on = [google_project_service.required]
}

resource "google_secret_manager_secret_version" "mysql_password" {
  secret      = google_secret_manager_secret.mysql_password.id
  secret_data = random_password.mysql.result
}

resource "google_sql_database_instance" "mysql" {
  name             = "${var.name_prefix}-mysql"
  region           = var.region
  database_version = "MYSQL_8_4"

  settings {
    tier              = "db-custom-2-7680"
    availability_type = "REGIONAL"
    disk_type         = "PD_SSD"
    disk_size         = 50
    disk_autoresize   = true

    backup_configuration {
      enabled                        = true
      binary_log_enabled             = true
      transaction_log_retention_days = 7
    }

    ip_configuration {
      ipv4_enabled    = false
      private_network = google_compute_network.main.id
      ssl_mode        = "ENCRYPTED_ONLY"
    }

    maintenance_window {
      day          = 7
      hour         = 15
      update_track = "stable"
    }

    user_labels = var.labels
  }

  deletion_protection = true
  depends_on          = [google_service_networking_connection.private_services]
}

resource "google_sql_database" "application" {
  name     = "collab_editor"
  instance = google_sql_database_instance.mysql.name
}

resource "google_sql_user" "application" {
  name     = "collab"
  instance = google_sql_database_instance.mysql.name
  password = random_password.mysql.result
}

resource "google_redis_instance" "sessions" {
  name                    = "${var.name_prefix}-redis"
  region                  = var.region
  tier                    = "STANDARD_HA"
  memory_size_gb          = 5
  redis_version           = "REDIS_7_2"
  authorized_network      = google_compute_network.main.id
  connect_mode            = "PRIVATE_SERVICE_ACCESS"
  transit_encryption_mode = "SERVER_AUTHENTICATION"
  auth_enabled            = true
  redis_configs = {
    "maxmemory-policy" = "noeviction"
  }
  labels     = var.labels
  depends_on = [google_service_networking_connection.private_services]
}

resource "google_secret_manager_secret" "redis_ca" {
  secret_id = "${var.name_prefix}-redis-ca"
  replication {
    auto {}
  }
}

resource "google_secret_manager_secret_version" "redis_ca" {
  secret      = google_secret_manager_secret.redis_ca.id
  secret_data = google_redis_instance.sessions.server_ca_certs[0].cert
}

resource "google_secret_manager_secret" "redis_auth" {
  secret_id = "${var.name_prefix}-redis-auth"
  replication {
    auto {}
  }
}

resource "google_secret_manager_secret_version" "redis_auth" {
  secret      = google_secret_manager_secret.redis_auth.id
  secret_data = google_redis_instance.sessions.auth_string
}
