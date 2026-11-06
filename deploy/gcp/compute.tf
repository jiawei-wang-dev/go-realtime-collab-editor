locals {
  artifact_registry_host = split("/", var.app_image)[0]
  etcd_names             = [for index in range(3) : "${var.name_prefix}-etcd-${index + 1}"]
  etcd_endpoints         = join(",", [for address in google_compute_address.etcd_private : "https://${address.address}:2379"])
  etcd_initial_cluster   = join(",", [for index, address in google_compute_address.etcd_private : "${local.etcd_names[index]}=https://${address.address}:2380"])
}

resource "google_compute_address" "etcd_private" {
  count        = 3
  name         = "${local.etcd_names[count.index]}-private"
  region       = var.region
  address_type = "INTERNAL"
  subnetwork   = google_compute_subnetwork.main.id
}

resource "google_compute_disk" "etcd" {
  count  = 3
  name   = "${local.etcd_names[count.index]}-data"
  zone   = var.zones[count.index]
  type   = "pd-balanced"
  size   = 20
  labels = var.labels
}

resource "google_compute_instance" "etcd" {
  count        = 3
  name         = local.etcd_names[count.index]
  zone         = var.zones[count.index]
  machine_type = var.etcd_machine_type
  tags         = ["${var.name_prefix}-etcd"]
  labels       = var.labels

  boot_disk {
    initialize_params {
      image = "cos-cloud/cos-stable"
      size  = 10
      type  = "pd-balanced"
    }
  }

  attached_disk {
    source      = google_compute_disk.etcd[count.index].id
    device_name = "etcd-data"
  }

  network_interface {
    subnetwork = google_compute_subnetwork.main.id
    network_ip = google_compute_address.etcd_private[count.index].address
  }

  metadata = {
    enable-oslogin = "TRUE"
    startup-script = templatefile("${path.module}/templates/etcd-startup.sh.tftpl", {
      node_name            = local.etcd_names[count.index]
      private_ip           = google_compute_address.etcd_private[count.index].address
      initial_cluster      = local.etcd_initial_cluster
      tls_bundle_secret_id = var.etcd_tls_bundle_secret_id
    })
  }

  service_account {
    email  = google_service_account.etcd.email
    scopes = ["cloud-platform"]
  }

  shielded_instance_config {
    enable_secure_boot          = true
    enable_vtpm                 = true
    enable_integrity_monitoring = true
  }

  lifecycle {
    prevent_destroy = true
  }

  depends_on = [
    google_project_iam_member.etcd_roles,
    google_secret_manager_secret_iam_member.etcd,
  ]
}

resource "google_compute_address" "app_public" {
  count        = var.app_node_count
  name         = "${var.name_prefix}-app-${count.index + 1}-public"
  region       = var.region
  address_type = "EXTERNAL"
}

resource "google_compute_address" "app_private" {
  count        = var.app_node_count
  name         = "${var.name_prefix}-app-${count.index + 1}-private"
  region       = var.region
  address_type = "INTERNAL"
  subnetwork   = google_compute_subnetwork.main.id
}

resource "google_compute_instance" "app" {
  count        = var.app_node_count
  name         = "${var.name_prefix}-app-${count.index + 1}"
  zone         = var.zones[count.index % length(var.zones)]
  machine_type = var.app_machine_type
  tags         = ["${var.name_prefix}-app"]
  labels       = var.labels

  boot_disk {
    initialize_params {
      image = "cos-cloud/cos-stable"
      size  = 20
      type  = "pd-balanced"
    }
  }

  network_interface {
    subnetwork = google_compute_subnetwork.main.id
    network_ip = google_compute_address.app_private[count.index].address
    access_config {
      nat_ip = google_compute_address.app_public[count.index].address
    }
  }

  metadata = {
    enable-oslogin = "TRUE"
    startup-script = templatefile("${path.module}/templates/app-startup.sh.tftpl", {
      runtime_env_secret_id     = var.runtime_env_secret_id
      tls_bundle_secret_id      = var.app_tls_bundle_secret_ids[count.index]
      redis_ca_secret_id        = google_secret_manager_secret.redis_ca.secret_id
      mysql_password_secret_id  = google_secret_manager_secret.mysql_password.secret_id
      redis_auth_secret_id      = google_secret_manager_secret.redis_auth.secret_id
      artifact_registry_host    = local.artifact_registry_host
      app_image                 = var.app_image
      node_id                   = "${var.name_prefix}-app-${count.index + 1}"
      node_incarnation          = var.app_node_incarnations[count.index]
      quic_endpoint             = "${google_compute_address.app_public[count.index].address}:8443"
      internal_rpc_endpoint     = "https://${google_compute_address.app_private[count.index].address}:9443"
      cloud_sql_connection_name = google_sql_database_instance.mysql.connection_name
      redis_host                = google_redis_instance.sessions.host
      redis_port                = google_redis_instance.sessions.port
      etcd_endpoints            = local.etcd_endpoints
    })
  }

  service_account {
    email  = google_service_account.app.email
    scopes = ["cloud-platform"]
  }

  shielded_instance_config {
    enable_secure_boot          = true
    enable_vtpm                 = true
    enable_integrity_monitoring = true
  }

  depends_on = [
    google_compute_instance.etcd,
    google_project_iam_member.app_roles,
    google_secret_manager_secret_iam_member.app,
    google_secret_manager_secret_version.mysql_password,
    google_secret_manager_secret_version.redis_auth,
    google_secret_manager_secret_version.redis_ca,
  ]

  lifecycle {
    precondition {
      condition     = length(var.app_tls_bundle_secret_ids) == var.app_node_count && length(var.app_node_incarnations) == var.app_node_count
      error_message = "Provide one TLS bundle secret and one incarnation for each application node."
    }
  }
}

resource "google_compute_instance_group" "app" {
  count = 3
  name  = "${var.name_prefix}-app-${count.index + 1}-group"
  zone  = var.zones[count.index]
  instances = [
    for index, instance in google_compute_instance.app : instance.self_link
    if index % length(var.zones) == count.index
  ]
  named_port {
    name = "https"
    port = 443
  }
}

resource "google_compute_health_check" "app" {
  name                = "${var.name_prefix}-app-tcp"
  timeout_sec         = 3
  check_interval_sec  = 10
  healthy_threshold   = 2
  unhealthy_threshold = 3
  tcp_health_check {
    port = 443
  }
}

resource "google_compute_region_backend_service" "rest" {
  name                  = "${var.name_prefix}-rest"
  region                = var.region
  protocol              = "TCP"
  load_balancing_scheme = "EXTERNAL"
  health_checks         = [google_compute_health_check.app.id]

  dynamic "backend" {
    for_each = google_compute_instance_group.app
    content {
      group = backend.value.self_link
    }
  }
}

resource "google_compute_address" "rest" {
  name         = "${var.name_prefix}-rest-public"
  region       = var.region
  address_type = "EXTERNAL"
}

resource "google_compute_forwarding_rule" "rest" {
  name                  = "${var.name_prefix}-rest"
  region                = var.region
  ip_address            = google_compute_address.rest.id
  ip_protocol           = "TCP"
  port_range            = "443"
  load_balancing_scheme = "EXTERNAL"
  backend_service       = google_compute_region_backend_service.rest.id
}
