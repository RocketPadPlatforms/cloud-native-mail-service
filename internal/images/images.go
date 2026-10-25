/*
Copyright 2026 Rocket Pad Platforms GmbH.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package images pins the data-plane and test images used by CNMS
// (ADR 0004, section 6): every image reference in the operator and
// the e2e suite is one of these constants.
//
// Upstream images are pinned by version tag AND digest and are
// updated by the Renovate regex managers in renovate.json.
// CNMS-built images (Postfix, E2EClient) are pinned by version tag
// only: their digest changes with every local build, and e2e always
// rebuilds them from images/ (make docker-build-data-plane); their
// tags are synced with the Dockerfiles in the same PR (ADR 0004
// consequences).
package images

// Upstream images (version tag + digest).
const (
	// Dovecot is the IMAP/LMTP/SASL engine (ADR 0001).
	Dovecot = "dovecot/dovecot:2.4.5@sha256:43c14def08a57f25107593cbcf64a0a6fbae84a7b9de7aa038513c9b21e91914"
	// Rspamd is the filtering and DKIM engine (ADR 0001).
	Rspamd = "rspamd/rspamd:4.2.3@sha256:47e00f62105f8a60009a30a590ebcf3bfd0b992d45468222d34afa5ca2103690"
	// Valkey is the per-MailServer Rspamd state store (ADR 0001).
	Valkey = "valkey/valkey:9.1.2@sha256:418652cfb58ef879d4978c33553735d7147016032d5aefaa14c828e611eb9dfd"
	// RedisExporter exports the Valkey metrics (ADR 0005).
	RedisExporter = "oliver006/redis_exporter:v1.96.0@sha256:3d57b9eb142a62ffc600063606a41cd471cd53d81bed10be5b56b3a8918f2c26"
	// Mailpit is the e2e SMTP sink (ADR 0004, section 7).
	Mailpit = "axllent/mailpit:v1.32.0@sha256:0790692be935ea4420d085694f7985b1f692ca6a8771ab0e0bd8ba75ac180498"
)

// CNMS-built images (version tag only, see package docs).
const (
	// Postfix is the SMTP engine built from the upstream source
	// tarball (images/postfix).
	Postfix = "cnms/postfix:3.11.7"
	// E2EClient is the e2e SMTP client (swaks, openssl, curl;
	// images/e2e-client).
	E2EClient = "cnms/e2e-client:3.11.7"
)
