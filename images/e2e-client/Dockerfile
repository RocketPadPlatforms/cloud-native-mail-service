# E2E client image (ADR 0004, decision 7): swaks for submission tests,
# openssl for protocol-level TLS checks, curl for the Mailpit API.
# Test-only; not part of the product.

ARG BASE_IMAGE=debian:trixie-slim@sha256:a72b4433de1cf7d6bae0c4cee74d0547f58601a643b3375f070c51600d63b6b2

FROM ${BASE_IMAGE}

RUN apt-get -o Acquire::Retries=5 update \
 && apt-get -o Acquire::Retries=5 install -y --no-install-recommends \
        ca-certificates curl openssl swaks libnet-ssleay-perl \
 && rm -rf /var/lib/apt/lists/*
