locals {
  ssh_public_key = trimspace(file(pathexpand(var.ssh_public_key_path)))
  # Quote the key as a YAML string, including any comment supplied by the operator.
  user_data = replace(
    file("${path.module}/../../cloud-init/user-data.yaml"),
    "- SPINUP_DEPLOY_SSH_KEY",
    "- ${jsonencode(local.ssh_public_key)}"
  )
}

resource "hcloud_ssh_key" "deploy" {
  name       = "${var.name_prefix}-deploy"
  public_key = local.ssh_public_key
}

resource "hcloud_firewall" "app" {
  name = "${var.name_prefix}-web"

  rule {
    direction  = "in"
    protocol   = "tcp"
    port       = "22"
    source_ips = ["0.0.0.0/0", "::/0"]
  }

  rule {
    direction  = "in"
    protocol   = "tcp"
    port       = "80"
    source_ips = ["0.0.0.0/0", "::/0"]
  }

  rule {
    direction  = "in"
    protocol   = "tcp"
    port       = "443"
    source_ips = ["0.0.0.0/0", "::/0"]
  }

  rule {
    direction  = "in"
    protocol   = "udp"
    port       = "443"
    source_ips = ["0.0.0.0/0", "::/0"]
  }
}

resource "hcloud_server" "app" {
  name         = var.name_prefix
  server_type  = var.server_type
  location     = var.location
  image        = var.image
  ssh_keys     = [hcloud_ssh_key.deploy.id]
  firewall_ids = [hcloud_firewall.app.id]
  user_data    = local.user_data

  public_net {
    ipv4_enabled = true
    ipv6_enabled = true
  }
}

resource "hcloud_volume" "data" {
  name     = "${var.name_prefix}-data"
  size     = var.volume_size
  location = var.location
  format   = "ext4"
}

resource "hcloud_volume_attachment" "data" {
  volume_id = hcloud_volume.data.id
  server_id = hcloud_server.app.id
  automount = true
}
