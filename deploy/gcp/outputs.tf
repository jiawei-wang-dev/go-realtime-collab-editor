output "rest_ip" {
  description = "Regional REST load-balancer address. Create DNS and a certificate for this endpoint."
  value       = google_compute_address.rest.address
}

output "quic_node_endpoints" {
  description = "Stable per-owner QUIC endpoints published through etcd membership."
  value       = [for address in google_compute_address.app_public : "${address.address}:8443"]
}

output "cloud_sql_private_ip" {
  value     = google_sql_database_instance.mysql.private_ip_address
  sensitive = true
}

output "redis_host" {
  value     = google_redis_instance.sessions.host
  sensitive = true
}

output "mysql_password_secret" {
  value = google_secret_manager_secret.mysql_password.id
}

output "etcd_endpoints" {
  value     = local.etcd_endpoints
  sensitive = true
}
