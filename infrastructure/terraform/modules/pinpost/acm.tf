# Certificate for the custom domains, plus the Cloudflare records that validate
# it. CloudFront only reads certificates from us-east-1. Publishing validation
# to the authoritative zone is what keeps auto-renewal working — a cert whose
# validation records are unreachable silently fails to renew.

resource "aws_acm_certificate" "site" {
  count    = local.has_domains ? 1 : 0
  provider = aws.us_east_1

  domain_name               = var.domain_names[0]
  subject_alternative_names = slice(var.domain_names, 1, length(var.domain_names))
  validation_method         = "DNS"

  lifecycle {
    create_before_destroy = true
  }
}

resource "cloudflare_dns_record" "cert_validation" {
  # Keyed by hostname, not by the validation record's own name: that name only
  # exists once ACM has issued, and for_each keys must be known at plan time —
  # keying on it makes the very first plan fail before anything is created.
  # One record per hostname is exact here because no two entries share a
  # validation record; that only happens with a wildcard SAN, which this module
  # never requests.
  for_each = local.has_domains ? toset(var.domain_names) : toset([])

  zone_id = var.cloudflare_zone_id
  name    = trimsuffix(local.cert_validation[each.key].resource_record_name, ".")
  type    = local.cert_validation[each.key].resource_record_type
  content = trimsuffix(local.cert_validation[each.key].resource_record_value, ".")
  proxied = false
  ttl     = 60
}

resource "aws_acm_certificate_validation" "site" {
  count    = local.has_domains ? 1 : 0
  provider = aws.us_east_1

  certificate_arn         = aws_acm_certificate.site[0].arn
  validation_record_fqdns = [for r in cloudflare_dns_record.cert_validation : r.name]
}
