resource "google_compute_firewall" "public_rest" {
  name          = "${var.name_prefix}-public-rest"
  network       = google_compute_network.main.name
  direction     = "INGRESS"
  source_ranges = ["0.0.0.0/0"]
  target_tags   = ["${var.name_prefix}-app"]
  allow {
    protocol = "tcp"
    ports    = ["443"]
  }
}

resource "google_compute_firewall" "public_quic" {
  name          = "${var.name_prefix}-public-quic"
  network       = google_compute_network.main.name
  direction     = "INGRESS"
  source_ranges = ["0.0.0.0/0"]
  target_tags   = ["${var.name_prefix}-app"]
  allow {
    protocol = "udp"
    ports    = ["8443"]
  }
}

resource "google_compute_firewall" "health_checks" {
  name          = "${var.name_prefix}-health-checks"
  network       = google_compute_network.main.name
  direction     = "INGRESS"
  source_ranges = ["35.191.0.0/16", "130.211.0.0/22"]
  target_tags   = ["${var.name_prefix}-app"]
  allow {
    protocol = "tcp"
    ports    = ["443"]
  }
}

resource "google_compute_firewall" "internal" {
  name          = "${var.name_prefix}-internal"
  network       = google_compute_network.main.name
  direction     = "INGRESS"
  source_ranges = [google_compute_subnetwork.main.ip_cidr_range]
  allow {
    protocol = "tcp"
    ports    = ["2379", "2380", "9443"]
  }
}

resource "google_compute_firewall" "iap_ssh" {
  name          = "${var.name_prefix}-iap-ssh"
  network       = google_compute_network.main.name
  direction     = "INGRESS"
  source_ranges = ["35.235.240.0/20"]
  allow {
    protocol = "tcp"
    ports    = ["22"]
  }
}
