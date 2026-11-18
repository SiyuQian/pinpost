# Two buckets: attachment bytes the SDK PUTs directly, and the static site
# CloudFront serves.

resource "aws_s3_bucket" "attachments" {
  # Bucket names are global, hence the account id; they are also
  # lowercase-only, which is what stack_name's validation enforces.
  bucket = local.attachments_name

  # ponytail: no lifecycle rule for objects whose feedback submission never
  # arrived — S3 cannot see the claim, so expiring them needs object tagging
  # driven from the stream. The DynamoDB item does expire, so they are
  # invisible; add the tagging pass if the orphan bytes ever cost anything.
}

resource "aws_s3_bucket_public_access_block" "attachments" {
  bucket                  = aws_s3_bucket.attachments.id
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

resource "aws_s3_bucket_server_side_encryption_configuration" "attachments" {
  bucket = aws_s3_bucket.attachments.id
  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm = "AES256"
    }
  }
}

# The SDK PUTs here directly from a customer origin.
resource "aws_s3_bucket_cors_configuration" "attachments" {
  bucket = aws_s3_bucket.attachments.id
  cors_rule {
    allowed_methods = ["PUT"]
    allowed_origins = ["*"]
    allowed_headers = ["*"]
    max_age_seconds = 86400
  }
}

resource "aws_s3_bucket_notification" "attachments" {
  bucket = aws_s3_bucket.attachments.id
  lambda_function {
    lambda_function_arn = aws_lambda_function.scan.arn
    events              = ["s3:ObjectCreated:*"]
  }
  depends_on = [aws_lambda_permission.scan_from_s3]
}

resource "aws_s3_bucket" "site" {
  bucket_prefix = "${local.name}-site-"
}

resource "aws_s3_bucket_public_access_block" "site" {
  bucket                  = aws_s3_bucket.site.id
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

resource "aws_s3_bucket_policy" "site" {
  bucket = aws_s3_bucket.site.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { Service = "cloudfront.amazonaws.com" }
      Action    = "s3:GetObject"
      Resource  = "${aws_s3_bucket.site.arn}/*"
      Condition = {
        StringEquals = { "AWS:SourceArn" = aws_cloudfront_distribution.site.arn }
      }
    }]
  })
}
