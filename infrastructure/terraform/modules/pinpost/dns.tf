# Custom domains are served from a Cloudflare zone that may be shared with
# other sites, so this module only ever creates records for its own
# hostnames — no apex, no www, no wildcard, no zone-level anything. The zone
# itself is not managed here; it is referenced by ID.
#
# Plain CNAMEs, not proxied: CloudFront serves the ACM certificate for these
# names and matches on the SNI host, so the browser has to reach it directly.
# (Proxying would also need the zone's SSL mode changed, which is zone-wide.)

resource "cloudflare_dns_record" "site" {
  for_each = toset(var.domain_names)

  zone_id = var.cloudflare_zone_id
  name    = each.value
  type    = "CNAME"
  content = aws_cloudfront_distribution.site.domain_name
  proxied = false
  ttl     = 1

  # Visible in the Cloudflare dashboard: whoever is about to flip this record
  # to proxied sees why not before they do it.
  comment = "Managed by Terraform (Pinpost). Keep DNS-only: CloudFront serves the ACM cert for this name."
}
