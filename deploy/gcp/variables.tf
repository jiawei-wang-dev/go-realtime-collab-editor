variable "project_id" {
  description = "Google Cloud project ID."
  type        = string
}

variable "region" {
  description = "Deployment region."
  type        = string
  default     = "australia-southeast1"
}

variable "zones" {
  description = "Three zones used for application and etcd nodes."
  type        = list(string)
  default     = ["australia-southeast1-a", "australia-southeast1-b", "australia-southeast1-c"]

  validation {
    condition     = length(var.zones) == 3
    error_message = "Exactly three zones are required."
  }
}

variable "name_prefix" {
  type    = string
  default = "collab"
}

variable "app_image" {
  description = "Immutable Artifact Registry image reference, preferably pinned by digest."
  type        = string
}

variable "app_machine_type" {
  type    = string
  default = "e2-standard-2"
}

variable "etcd_machine_type" {
  type    = string
  default = "e2-medium"
}

variable "app_node_count" {
  type    = number
  default = 3

  validation {
    condition     = var.app_node_count >= 3 && var.app_node_count <= 9
    error_message = "app_node_count must be between 3 and 9."
  }
}

variable "runtime_env_secret_id" {
  description = "Existing Secret Manager secret containing non-TLS application environment variables."
  type        = string
}

variable "app_tls_bundle_secret_ids" {
  description = "Per-node Secret Manager secrets containing gzip tar archives of certs/."
  type        = list(string)
}

variable "app_node_incarnations" {
  description = "Per-node immutable incarnations encoded in each internal mTLS certificate URI SAN."
  type        = list(string)
}

variable "etcd_tls_bundle_secret_id" {
  description = "Secret Manager secret containing the etcd CA and node certificates."
  type        = string
}

variable "alert_email" {
  description = "Optional email address for the monitoring notification channel."
  type        = string
  default     = ""
}

variable "labels" {
  type = map(string)
  default = {
    application = "collab-editor"
    managed_by  = "terraform"
  }
}
