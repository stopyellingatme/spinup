# Managed by spinup. Keep the provider lock file in version control.
terraform {
  required_version = "= 1.13.1"

  required_providers {
    hcloud = {
      source  = "hetznercloud/hcloud"
      version = "= 1.70.0"
    }
  }

  # spinup infra selects a separate state path for each environment during init.
  backend "local" {}
}

# The provider reads HCLOUD_TOKEN from the environment.
provider "hcloud" {}
