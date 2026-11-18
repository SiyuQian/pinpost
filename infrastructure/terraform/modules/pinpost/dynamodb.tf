# The single table. Every entity lives here; see the data model in the README.

resource "aws_dynamodb_table" "table" {
  name         = local.name
  billing_mode = "PAY_PER_REQUEST"
  hash_key     = "pk"
  range_key    = "sk"

  attribute {
    name = "pk"
    type = "S"
  }
  attribute {
    name = "sk"
    type = "S"
  }
  attribute {
    name = "gsi1pk"
    type = "S"
  }
  attribute {
    name = "gsi1sk"
    type = "S"
  }

  global_secondary_index {
    name            = "GSI1"
    hash_key        = "gsi1pk"
    range_key       = "gsi1sk"
    projection_type = "ALL"
  }

  # Sessions, idempotency keys, rate-limit counters, unclaimed attachments
  # and delivery records all expire on their own.
  ttl {
    attribute_name = "expires_at"
    enabled        = true
  }

  # The change stream is what replaced the outbox table and its worker.
  stream_enabled   = true
  stream_view_type = "NEW_AND_OLD_IMAGES"

  point_in_time_recovery {
    enabled = true
  }
}
