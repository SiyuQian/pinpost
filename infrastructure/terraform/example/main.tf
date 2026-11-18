# Example environment root. Copy this directory once per environment (for
# example staging/ and production/) so each keeps its own state; a plan in one
# can then never touch the other.
#
# Every resource lives in ../modules/pinpost; a root only decides which account
# and state it writes to, and what the stack is called.

module "pinpost" {
  source    = "../modules/pinpost"
  providers = { aws.us_east_1 = aws.us_east_1 }

  domain_names       = var.domain_names
  cloudflare_zone_id = var.cloudflare_zone_id

  stack_name            = var.stack_name
  google_client_id      = var.google_client_id
  google_allowed_emails = var.google_allowed_emails

  seed_admin_email     = var.seed_admin_email
  seed_admin_password  = var.seed_admin_password
  cors_allowed_origins = var.cors_allowed_origins
}
