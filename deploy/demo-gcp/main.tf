locals {
  required_services = toset([
    "compute.googleapis.com",
    "iap.googleapis.com",
  ])

  # Public list prices for us-central1, checked 2026-11-10. Network egress is
  # usage-based and deliberately excluded from this fixed-resource subtotal.
  vm_hourly_usd              = 0.03350571
  external_ipv4_hourly_usd   = 0.005
  pd_standard_gib_hourly_usd = 0.000054795
  fixed_hourly_usd           = local.vm_hourly_usd + local.external_ipv4_hourly_usd + (var.boot_disk_gb * local.pd_standard_gib_hourly_usd)
}

resource "google_project_service" "required" {
  for_each           = local.required_services
  project            = var.project_id
  service            = each.value
  disable_on_destroy = false
}

resource "google_compute_network" "demo" {
  name                    = "${var.name_prefix}-network"
  auto_create_subnetworks = false
  depends_on              = [google_project_service.required]
}

resource "google_compute_subnetwork" "demo" {
  name          = "${var.name_prefix}-subnet"
  region        = var.region
  network       = google_compute_network.demo.id
  ip_cidr_range = var.subnet_cidr
}

resource "google_compute_firewall" "iap_ssh" {
  name      = "${var.name_prefix}-iap-ssh"
  network   = google_compute_network.demo.name
  direction = "INGRESS"
  priority  = 1000

  source_ranges = ["35.235.240.0/20"]
  target_tags   = ["${var.name_prefix}-iap-ssh"]

  allow {
    protocol = "tcp"
    ports    = ["22"]
  }
}

data "google_compute_image" "debian" {
  family  = "debian-12"
  project = "debian-cloud"
}

resource "google_compute_instance" "demo" {
  name                      = "${var.name_prefix}-vm"
  zone                      = var.zone
  machine_type              = var.machine_type
  allow_stopping_for_update = true
  deletion_protection       = false
  tags                      = ["${var.name_prefix}-iap-ssh"]

  labels = {
    application = "collab-editor"
    environment = "temporary-demo"
    managed-by  = "terraform"
  }

  boot_disk {
    auto_delete = true
    initialize_params {
      image = data.google_compute_image.debian.self_link
      size  = var.boot_disk_gb
      type  = "pd-standard"
    }
  }

  network_interface {
    subnetwork = google_compute_subnetwork.demo.id

    # An ephemeral address is required only for outbound Docker image pulls and
    # the outbound Cloudflare Quick Tunnel. No public ingress firewall exists.
    access_config {
      network_tier = "STANDARD"
    }
  }

  metadata = {
    enable-oslogin         = "TRUE"
    block-project-ssh-keys = "TRUE"
  }

  metadata_startup_script = templatefile("${path.module}/startup.sh.tftpl", {})

  scheduling {
    automatic_restart   = true
    on_host_maintenance = "MIGRATE"
    provisioning_model  = "STANDARD"
  }

  shielded_instance_config {
    enable_secure_boot          = true
    enable_vtpm                 = true
    enable_integrity_monitoring = true
  }

  depends_on = [google_project_service.required]
}
