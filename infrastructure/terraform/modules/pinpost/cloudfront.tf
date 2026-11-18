# The single public entry point: static site by default, /v1/* to the API
# function's URL.

resource "aws_cloudfront_origin_access_control" "site" {
  name                              = "${local.name}-site"
  origin_access_control_origin_type = "s3"
  signing_behavior                  = "always"
  signing_protocol                  = "sigv4"
}

resource "aws_cloudfront_distribution" "site" {
  enabled             = true
  default_root_object = "index.html"
  aliases             = var.domain_names

  origin {
    origin_id                = "site"
    domain_name              = aws_s3_bucket.site.bucket_regional_domain_name
    origin_access_control_id = aws_cloudfront_origin_access_control.site.id
  }

  origin {
    origin_id = "api"
    # Function URLs are HTTPS-only and have no path prefix.
    domain_name = split("/", aws_lambda_function_url.api.function_url)[2]
    custom_origin_config {
      origin_protocol_policy = "https-only"
      origin_ssl_protocols   = ["TLSv1.2"]
      http_port              = 80
      https_port             = 443
    }
  }

  default_cache_behavior {
    target_origin_id       = "site"
    viewer_protocol_policy = "redirect-to-https"
    allowed_methods        = ["GET", "HEAD"]
    cached_methods         = ["GET", "HEAD"]
    # CachingOptimized
    cache_policy_id = "658327ea-f89d-4fab-a63d-7e88639e58f6"

    function_association {
      event_type   = "viewer-request"
      function_arn = aws_cloudfront_function.rewrite.arn
    }
  }

  ordered_cache_behavior {
    path_pattern           = "/v1/*"
    target_origin_id       = "api"
    viewer_protocol_policy = "redirect-to-https"
    allowed_methods        = ["GET", "HEAD", "OPTIONS", "PUT", "POST", "PATCH", "DELETE"]
    cached_methods         = ["GET", "HEAD"]
    # CachingDisabled + AllViewerExceptHostHeader: forwards the session cookie
    # and the write-key header, keeps the Function URL's host.
    cache_policy_id          = "4135ea2d-6df8-44a3-9df3-4b5a84be39ad"
    origin_request_policy_id = "b689b0a8-53d0-40ab-baf2-68738e2966ac"
  }

  restrictions {
    geo_restriction {
      restriction_type = "none"
    }
  }

  viewer_certificate {
    cloudfront_default_certificate = !local.has_domains
    acm_certificate_arn            = one(aws_acm_certificate_validation.site[*].certificate_arn)
    ssl_support_method             = local.has_domains ? "sni-only" : null
    minimum_protocol_version       = local.has_domains ? "TLSv1.2_2021" : null
  }
}

# S3 has no index documents behind an OAC origin — only the website endpoint
# does, and that one needs a public bucket. So `/admin/` asks S3 for the key
# `admin/`, which does not exist, and the answer is 403. Two lines of edge
# JavaScript fix both the directory URLs and the root, which would otherwise
# look for an `index.html` this bucket deliberately does not have at the top
# level.
resource "aws_cloudfront_function" "rewrite" {
  name    = "${local.name}-rewrite"
  runtime = "cloudfront-js-2.0"
  publish = true
  code    = <<-JS
    function handler(event) {
      var request = event.request;
      if (request.uri === '/') {
        return {
          statusCode: 302,
          statusDescription: 'Found',
          headers: { location: { value: '/admin/' } },
        };
      }
      if (request.uri.endsWith('/')) {
        request.uri += 'index.html';
      }
      return request;
    }
  JS
}
