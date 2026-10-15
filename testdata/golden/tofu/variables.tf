variable "server_type" {
  description = "Hetzner Cloud server type. Use an x86 type for the default deployment images."
  type        = string
  default     = "cx23"
}

variable "location" {
  description = "Hetzner Cloud location for the server and volume."
  type        = string
  default     = "fsn1"
}

variable "image" {
  description = "Server image with cloud-init support."
  type        = string
  default     = "ubuntu-24.04"
}

variable "ssh_public_key_path" {
  description = "Local path to the deploy user's public SSH key."
  type        = string
  default     = "~/.ssh/id_ed25519.pub"
}

variable "volume_size" {
  description = "Data volume size in GiB."
  type        = number
  default     = 20

  validation {
    condition     = var.volume_size >= 10 && floor(var.volume_size) == var.volume_size
    error_message = "The volume size must be an integer of at least 10 GiB."
  }
}

variable "name_prefix" {
  description = "Prefix for the infrastructure resource names. Use a unique prefix for each environment."
  type        = string
  default     = "todo"
}
