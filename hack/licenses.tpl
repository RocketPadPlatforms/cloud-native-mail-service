{{ range . }}
## {{ .Name }}

- Version: {{ .Version }}
- License: {{ .LicenseName }}
- Source: {{ .LicenseURL }}

{{ end }}
