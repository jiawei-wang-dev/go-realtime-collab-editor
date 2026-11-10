output "instance_name" {
  value = google_compute_instance.demo.name
}

output "zone" {
  value = google_compute_instance.demo.zone
}

output "iap_ssh_command" {
  value = "gcloud compute ssh ${google_compute_instance.demo.name} --project=${var.project_id} --zone=${var.zone} --tunnel-through-iap"
}

output "fixed_hourly_cost_usd" {
  description = "List-price subtotal for e2-medium, one in-use external IPv4, and 20 GiB pd-standard; excludes variable egress and any free-tier offsets."
  value       = format("%.8f", local.fixed_hourly_usd)
}

output "fixed_daily_cost_usd" {
  value = format("%.8f", local.fixed_hourly_usd * 24)
}
