output "ipv4" {
  description = "Public IPv4 address. Point the application DNS record at this address."
  value       = hcloud_server.app.ipv4_address
}

output "ipv6" {
  description = "Public IPv6 address."
  value       = hcloud_server.app.ipv6_address
}

output "server_id" {
  description = "Hetzner Cloud server ID."
  value       = hcloud_server.app.id
}
