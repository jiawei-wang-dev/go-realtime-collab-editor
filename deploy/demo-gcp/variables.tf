variable "project_id" {
  description = "Existing Free Trial GCP project. This configuration never changes billing."
  type        = string

  validation {
    condition     = length(trimspace(var.project_id)) > 0
    error_message = "project_id is required."
  }
}

variable "region" {
  description = "Compute Engine region used for the temporary demo."
  type        = string
  default     = "us-central1"
}

variable "zone" {
  description = "Compute Engine zone used for the temporary demo."
  type        = string
  default     = "us-central1-a"
}

variable "name_prefix" {
  description = "Prefix for the isolated demo resources."
  type        = string
  default     = "collab-public-demo"
}

variable "machine_type" {
  description = "Single VM size. e2-medium is the minimum recommended size for MySQL plus Go sandbox compilation."
  type        = string
  default     = "e2-medium"

  validation {
    condition     = var.machine_type == "e2-medium"
    error_message = "The reviewed demo design supports exactly e2-medium; recalculate cost before changing it."
  }
}

variable "boot_disk_gb" {
  description = "Standard persistent boot disk size."
  type        = number
  default     = 20

  validation {
    condition     = var.boot_disk_gb == 20
    error_message = "The reviewed cost estimate assumes exactly 20 GiB."
  }
}

variable "subnet_cidr" {
  description = "CIDR for the isolated single-VM VPC."
  type        = string
  default     = "10.88.0.0/28"
}
