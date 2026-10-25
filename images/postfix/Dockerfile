# CNMS Postfix data-plane image (ADR 0004:
# docs/decisions/0004-data-plane-images-and-build-strategy.md).
#
# Postfix 3.11.7 is built from the official source tarball because Debian
# trixie (the base) only ships 3.10.13. The tarball is pinned by SHA512 and
# verified against Wietse's GPG signature (key in keys/wietse.pgp,
# fingerprint 622C 7C01 2254 C186 6774 69C5 0C0B 590E 80CA 15A7).
#
# Build:  docker build -t cnms/postfix:3.11.7 images/postfix
#
# Runtime contract (used by the CNMS operator):
#   - main container:  runs as root; /usr/local/bin/cnms-postfix-entrypoint.sh
#     runs the master with `postfix start-fg` (-s keeps stdout open so
#     postlogd writes the daemon logs to the pod stdout), polls it every
#     1s and stops it on SIGTERM. postfix(1) is superuser-only; the master
#     drops privileges to the postfix user (mail_owner) for all daemons
#     (master.cf unpriv).
#   - config:          /etc/postfix is a read-only mount point (ConfigMap).
#   - queue:           /var/spool/postfix (queue_directory) is a volume
#     mount; the pod fsGroup must match the postfix group UID (5001) so the
#     daemon processes (user postfix) can write the queue.
#   - validation:      `postfix check` (run as root against the mounted
#     config) is the config gate (init container).
#   - queue metrics:   /usr/local/bin/cnms-postfix-queue-metrics.sh serves
#     Prometheus text on 9150 (ADR 0005).

ARG BASE_IMAGE=debian:trixie-slim@sha256:62bd423aeee912639ca8ee681c2b762cb3e25137e0a92b20be455898a9f034ad

FROM ${BASE_IMAGE} AS builder

ARG POSTFIX_VERSION=3.11.7
ARG POSTFIX_SHA512=3b4ef7562a24bbab1d0ca361ac0af1037aa3d4ae65a7c825711de9d5d3ed88975aad63cb09991717de334f4382c29039b527a3e8d4e65c5a6d35efd3e782910f
ARG POSTFIX_MIRROR=https://postfix-mirror.horus-it.com/postfix-release

RUN apt-get update \
 && apt-get install -y --no-install-recommends \
        build-essential ca-certificates curl ed gpg gpg-agent libldap-dev m4 \
        liblmdb-dev libsqlite3-dev libssl-dev \
 && rm -rf /var/lib/apt/lists/*

COPY keys/wietse.pgp /tmp/wietse.pgp

RUN set -eux; \
    curl -fsSL -o /tmp/postfix.tar.gz \
        "${POSTFIX_MIRROR}/official/postfix-${POSTFIX_VERSION}.tar.gz"; \
    curl -fsSL -o /tmp/postfix.tar.gz.gpg2 \
        "${POSTFIX_MIRROR}/official/postfix-${POSTFIX_VERSION}.tar.gz.gpg2"; \
    echo "${POSTFIX_SHA512}  /tmp/postfix.tar.gz" | sha512sum -c -; \
    gpg --batch --no-default-keyring --keyring /tmp/keyring.gpg \
        --import /tmp/wietse.pgp; \
    gpg --batch --no-default-keyring --keyring /tmp/keyring.gpg \
        --verify /tmp/postfix.tar.gz.gpg2 /tmp/postfix.tar.gz; \
    tar -xzf /tmp/postfix.tar.gz -C /opt

# Postfix 3.11 builds with `make makefiles` (no ./configure). Flags per the
# shipped README_FILES/{TLS,SASL,LDAP,SQLITE,LMDB}_README:
#   -DUSE_TLS            TLS support (openssl)
#   -DUSE_SASL_AUTH      SMTP AUTH; the dovecot SASL type is built in
#                        (no external SASL library), matching ADR 0004.
#                        The double escaping is deliberate: makedefs writes
#                        CCARGS verbatim and the build shell consumes one
#                        level, leaving the quoted default for the compiler.
#   -DHAS_LMDB/SQLITE/LDAP static map clients for the directory phases
#   -DNO_DB                no Berkeley DB; LMDB is the map store
#   -DNO_NIS(-PLUS)        no NIS/Yellow Pages (unused in containers)
#
# Install parameters (make makefiles defaults, INSTALL 4.6.1): client
# binaries go to /usr/sbin, but mailq and newaliases default to /usr/bin
# (mailq_path/newaliases_path), so both are pinned to /usr/sbin.
RUN set -eux; \
    cd /opt/postfix-${POSTFIX_VERSION}; \
    make makefiles \
        "CCARGS=-DNO_DB -DNO_NIS -DNO_NISPLUS -DUSE_TLS -DUSE_SASL_AUTH -DDEF_SERVER_SASL_TYPE=\\\"dovecot\\\" -DHAS_LMDB -DHAS_SQLITE -DHAS_LDAP" \
        "AUXLIBS=-lssl -lcrypto" \
        "AUXLIBS_LMDB=-llmdb" \
        "AUXLIBS_SQLITE=-lsqlite3 -lpthread" \
        "AUXLIBS_LDAP=-lldap -llber" \
        "default_database_type=lmdb" \
        "default_cache_db_type=lmdb" \
        "command_directory=/usr/sbin" \
        "daemon_directory=/usr/libexec/postfix" \
        "data_directory=/var/lib/postfix" \
        "config_directory=/etc/postfix" \
        "newaliases_path=/usr/sbin/newaliases" \
        "mailq_path=/usr/sbin/mailq"; \
    make -j"$(nproc)"; \
    groupadd -g 5001 postfix; \
    groupadd -g 5002 postdrop; \
    useradd -u 5001 -g postfix -G postdrop -M -d /var/spool/postfix \
        -s /usr/sbin/nologin postfix; \
    mkdir -p /etc/postfix; \
    echo "# transient install-time config (the operator mounts the real one)" \
        > /etc/postfix/main.cf; \
    echo "# transient install-time master.cf (the operator mounts the real one)" \
        > /etc/postfix/master.cf; \
    make install POSTFIX_INSTALL_OPTS=-non-interactive; \
    test "$(/usr/sbin/postconf -h mail_version)" = "${POSTFIX_VERSION}"; \
    cp conf/aliases /etc/aliases; \
    /usr/sbin/newaliases

# Fail the build if any built binary is missing or misses a shared library.
RUN for b in /usr/libexec/postfix/master /usr/libexec/postfix/smtpd \
        /usr/libexec/postfix/smtp /usr/sbin/postfix /usr/sbin/postqueue \
        /usr/sbin/mailq /usr/sbin/newaliases /usr/sbin/postlog \
        /usr/sbin/sendmail; do \
        test -x "$b" || { echo "missing binary $b" >&2; exit 1; }; \
        if ldd "$b" | grep -q 'not found'; then \
            echo "missing libraries for $b:" >&2; \
            ldd "$b" | grep 'not found' >&2; \
            exit 1; \
        fi; \
    done

FROM ${BASE_IMAGE} AS runtime

# Runtime dependencies of the built binaries plus the queue-metrics sidecar
# tools (busybox httpd, jq; ADR 0005).
RUN apt-get update \
 && apt-get install -y --no-install-recommends \
        busybox jq libldap2 liblmdb0 libsqlite3-0 libssl3t64 \
 && rm -rf /var/lib/apt/lists/*

COPY --from=builder /usr/libexec/postfix /usr/libexec/postfix
COPY --from=builder \
    /usr/sbin/postfix \
    /usr/sbin/postconf \
    /usr/sbin/postmap \
    /usr/sbin/postqueue \
    /usr/sbin/postsuper \
    /usr/sbin/postdrop \
    /usr/sbin/postcat \
    /usr/sbin/postlog \
    /usr/sbin/sendmail \
    /usr/sbin/mailq \
    /usr/sbin/newaliases \
    /usr/sbin/postalias \
    /usr/sbin/
COPY cnms-postfix-entrypoint.sh cnms-postfix-queue-metrics.sh /usr/local/bin/
RUN chmod +x /usr/local/bin/cnms-postfix-entrypoint.sh \
         /usr/local/bin/cnms-postfix-queue-metrics.sh

# The postfix user (UID/GID 5001) runs all Postfix daemons: the root
# entrypoint starts the master, which drops privileges to mail_owner.
# postdrop (GID 5002) is the setgid group for queue file creation. The queue
# volume is group-owned via the pod fsGroup (5001). /var/lib/postfix
# (data_directory) and /var/mail (mail_spool_directory) are not
# volume-mounted and must exist in the image. /etc/aliases is the default
# alias_maps source (built as LMDB by newaliases at build time, matching
# default_database_type=lmdb). postfix-files lives in the meta_directory
# (default: the config directory); postfix check needs it.
COPY --from=builder /etc/aliases /etc/aliases.lmdb /etc/
# Single-source COPY: a multi-source COPY would flatten postfix-files to
# /etc/postfix-files.
COPY --from=builder /etc/postfix/postfix-files /etc/postfix/postfix-files
RUN groupadd -g 5001 postfix \
 && groupadd -g 5002 postdrop \
 && useradd -u 5001 -g postfix -G postdrop -M -d /var/spool/postfix \
    -s /usr/sbin/nologin postfix \
 && mkdir -p /etc/postfix /var/spool/postfix /var/lib/postfix /var/mail \
 && chown postfix:postfix /var/lib/postfix /var/mail \
 && chmod 755 /etc/postfix /var/spool/postfix

EXPOSE 25 587 9150
CMD ["/usr/local/bin/cnms-postfix-entrypoint.sh"]
