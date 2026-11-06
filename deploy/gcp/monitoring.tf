resource "google_monitoring_notification_channel" "email" {
  count        = var.alert_email == "" ? 0 : 1
  display_name = "Collab editor operations"
  type         = "email"
  labels = {
    email_address = var.alert_email
  }
  depends_on = [google_project_service.required]
}

resource "google_monitoring_uptime_check_config" "rest" {
  display_name = "Collab editor REST TCP"
  timeout      = "5s"
  period       = "60s"

  monitored_resource {
    type = "uptime_url"
    labels = {
      host       = google_compute_address.rest.address
      project_id = var.project_id
    }
  }

  tcp_check {
    port = 443
  }
}

resource "google_monitoring_alert_policy" "rest_unavailable" {
  display_name = "Collab editor REST unavailable"
  combiner     = "OR"
  conditions {
    display_name = "Uptime check failed"
    condition_threshold {
      filter          = "metric.type=\"monitoring.googleapis.com/uptime_check/check_passed\" AND resource.type=\"uptime_url\""
      comparison      = "COMPARISON_LT"
      threshold_value = 1
      duration        = "120s"
      aggregations {
        alignment_period   = "60s"
        per_series_aligner = "ALIGN_NEXT_OLDER"
      }
    }
  }
  notification_channels = google_monitoring_notification_channel.email[*].name
  depends_on            = [google_monitoring_uptime_check_config.rest]
}

resource "google_monitoring_alert_policy" "cloudsql_cpu" {
  display_name = "Collab editor Cloud SQL CPU high"
  combiner     = "OR"
  conditions {
    display_name = "CPU above 80 percent"
    condition_threshold {
      filter          = "metric.type=\"cloudsql.googleapis.com/database/cpu/utilization\" AND resource.type=\"cloudsql_database\""
      comparison      = "COMPARISON_GT"
      threshold_value = 0.8
      duration        = "300s"
      aggregations {
        alignment_period   = "60s"
        per_series_aligner = "ALIGN_MEAN"
      }
    }
  }
  notification_channels = google_monitoring_notification_channel.email[*].name
}

resource "google_monitoring_alert_policy" "redis_memory" {
  display_name = "Collab editor Redis memory high"
  combiner     = "OR"
  conditions {
    display_name = "Memory ratio above 80 percent"
    condition_threshold {
      filter          = "metric.type=\"redis.googleapis.com/stats/memory/usage_ratio\" AND resource.type=\"redis_instance\""
      comparison      = "COMPARISON_GT"
      threshold_value = 0.8
      duration        = "300s"
      aggregations {
        alignment_period   = "60s"
        per_series_aligner = "ALIGN_MEAN"
      }
    }
  }
  notification_channels = google_monitoring_notification_channel.email[*].name
}
