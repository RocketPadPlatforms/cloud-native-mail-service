# E2E client image (ADR 0004, decision 7): swaks for submission tests,
# openssl for protocol-level TLS checks, curl for the Mailpit API.
# Test-only; not part of the product.

ARG BASE_IMAGE=debian:trixie-slim@sha256:62bd423aeee912639ca8ee681c2b762cb3e25137e0a92b20be455898a9f034ad

FROM ${BASE_IMAGE}

RUN apt-get update \
 && apt-get install -y --no-install-recommends \
        ca-certificates curl openssl swaks libnet-ssleay-perl \
 && rm -rf /var/lib/apt/lists/*
