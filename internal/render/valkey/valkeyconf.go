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

package valkey

import (
	"fmt"
	"strings"

	"github.com/RocketPadPlatforms/cloud-native-mail-service/internal/render"
)

// valkeyConfPath is where the rendered configuration lives in the
// container. The image entrypoint starts the server with a `.conf`
// argument, so the container args are just this path (verified
// against the 9.1.2 image: the entrypoint chowns the data directory
// to the valkey user and drops privileges via setpriv).
const valkeyConfPath = "/etc/valkey/valkey.conf"

// renderValkeyConf renders valkey.conf. Verified against the running
// valkey/valkey:9.1.2 image: `bind * -::*` is accepted and listens
// on IPv4 and IPv6; with protected-mode off the server is reachable
// from another container on the network without auth; `logfile ""`
// logs to stdout; RDB snapshots (SAVE) land in `dir`.
func renderValkeyConf(req *render.RenderRequest) string {
	var b strings.Builder
	b.WriteString("# CNMS-rendered Valkey valkey.conf -- do not edit.\n")
	fmt.Fprintf(&b, "# Source: MailServer %s/%s (ADR 0004)\n\n",
		req.Server.Namespace, req.Server.Name)
	b.WriteString("# One dedicated Valkey instance per MailServer (principle 6):\n")
	b.WriteString("# rate-limit, reputation and Bayes state are never shared\n")
	b.WriteString("# between servers.\n")
	b.WriteString("# Internal state store: reached only from the Rspamd pods of\n")
	b.WriteString("# the same MailServer (ADR 0006); no auth by design.\n")
	fmt.Fprintf(&b, "port %d\n", render.PortValkey)
	b.WriteString("bind * -::*\n")
	b.WriteString("protected-mode no\n")
	b.WriteString("# Log to stdout (container log).\n")
	b.WriteString("logfile \"\"\n")
	b.WriteString("# Data directory: emptyDir by default, RWO PVC when\n")
	b.WriteString("# spec.valkey.persistence is enabled (ADR 0004). The image\n")
	b.WriteString("# entrypoint chowns the directory to the valkey user (999).\n")
	b.WriteString("dir /data\n")
	b.WriteString("# RDB snapshots (the built-in defaults, made explicit): with\n")
	b.WriteString("# persistence enabled they land on the volume; Valkey state\n")
	b.WriteString("# is excluded from backups by default (rebuildable except\n")
	b.WriteString("# trained Bayes data).\n")
	b.WriteString("save 3600 1 300 100 60 10000\n")
	return b.String()
}
