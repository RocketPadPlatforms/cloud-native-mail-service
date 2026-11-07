//go:build e2e

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

package e2e

import (
	"fmt"

	"github.com/RocketPadPlatforms/cloud-native-mail-service/internal/images"
)

// The two directory fixtures of the e2e suite (ADR 0015, section 9): an
// OpenLDAP and a PostgreSQL server in the data plane namespace, seeded
// with the same accounts, each with its MailDirectory. The MailServer
// under test uses the PostgreSQL one; the directory specs switch it to
// the LDAP one and back.
const (
	// e2eSQLDirectory and e2eLDAPDirectory are the MailDirectory names.
	e2eSQLDirectory  = "sql-e2e"
	e2eLDAPDirectory = "ldap-e2e"

	// e2eAlias is a further address of e2eUser in the directories; it
	// delivers into the mailbox of e2eMailbox and is no login.
	e2eAlias = "sales@" + e2eDomain
	// e2eOtherUser is a second account of both directories.
	e2eOtherUser     = "bob"
	e2eOtherPassword = "bob-pass-456"
	// e2eDisabledUser is an LDAP account that mailEnabled=FALSE and
	// the activeFilter switch off.
	e2eDisabledUser    = "carol"
	e2eDisabledMailbox = e2eDisabledUser + "@" + e2eDomain
)

// e2eDirectoryFixturesTemplate holds the directory fixtures. The two %s
// are the OpenLDAP and the PostgreSQL images (pinned in internal/images,
// ADR 0015, section 9); the file must contain no other percent sign.
//
// TLS: cert-manager issues a CA of its own and a server certificate per
// fixture, and the MailDirectories verify against the CA of the
// certificate Secret (ca.crt), as the defaults of the API require.
//
// The NetworkPolicy allows the pods of the MailServer to reach the
// fixtures: the egress policy of the data plane (ADR 0006) allows DNS and
// the pods of the server only, so a directory needs an allow of its own.
// It also allows port 9999: an outage of a directory is its Service pointing
// there, where nothing listens, so connections are refused at once. (A
// Service without endpoints would make the policy drop the packets, and a
// lookup would hang instead.)
//
// OpenLDAP (osixia/openldap 2.6, runs as user 911): TLS comes from
// OPENLDAP_BOOTSTRAP_TLS and the default certificate paths; the LDIF
// mounted over the "custom" directory is loaded at the first start; the
// read-only account is the lookup bind (it reads every attribute but the
// password, which the users prove by binding). Only the attributes that
// the default access rules list (mail, uid) are readable by users, so
// without the read-only account mailAlias and mailEnabled stay hidden.
// The "_HASHED" variable is documented to hold a hash; a clear text
// value is stored as it is and works for a bind.
//
// PostgreSQL: the server key must belong to the postgres user with mode
// 0600, which a Secret mount cannot give, so an init container copies the
// files. The initdb scripts create the relations and the lookup role.
const e2eDirectoryFixturesTemplate = `
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: directory-ca
  namespace: cnms-e2e
spec:
  isCA: true
  commonName: directory-ca
  secretName: directory-ca
  issuerRef:
    kind: Issuer
    name: selfsigned-e2e
---
apiVersion: cert-manager.io/v1
kind: Issuer
metadata:
  name: directory-ca
  namespace: cnms-e2e
spec:
  ca:
    secretName: directory-ca
---
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: ldap-tls
  namespace: cnms-e2e
spec:
  secretName: ldap-tls
  dnsNames:
    - ldap
    - ldap.cnms-e2e
    - ldap.cnms-e2e.svc
    - ldap.cnms-e2e.svc.cluster.local
  issuerRef:
    kind: Issuer
    name: directory-ca
---
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: sql-tls
  namespace: cnms-e2e
spec:
  secretName: sql-tls
  dnsNames:
    - sql
    - sql.cnms-e2e
    - sql.cnms-e2e.svc
    - sql.cnms-e2e.svc.cluster.local
  issuerRef:
    kind: Issuer
    name: directory-ca
---
apiVersion: v1
kind: Secret
metadata:
  name: sql-creds
  namespace: cnms-e2e
stringData:
  password: e2e-sql-pass
---
apiVersion: v1
kind: Secret
metadata:
  name: ldap-creds
  namespace: cnms-e2e
stringData:
  password: e2e-ldap-pass
---
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: allow-directory-egress
  namespace: cnms-e2e
spec:
  podSelector:
    matchLabels:
      cnms.rpp.gmbh/mailserver: mail
  policyTypes:
    - Egress
  egress:
    - to:
        - podSelector:
            matchLabels:
              app: ldap
      ports:
        - port: 3890
        - port: 9999
    - to:
        - podSelector:
            matchLabels:
              app: sql
      ports:
        - port: 5432
        - port: 9999
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: ldap-ldif
  namespace: cnms-e2e
data:
  50-people.ldif: |
    dn: ou=people,dc=example,dc=org
    objectClass: organizationalUnit
    ou: people

    dn: uid=alice,ou=people,dc=example,dc=org
    objectClass: inetOrgPerson
    objectClass: PostfixBookMailAccount
    uid: alice
    cn: Alice
    sn: Example
    mail: alice@e2e.example.org
    mailAlias: sales@e2e.example.org
    mailEnabled: TRUE
    userPassword: e2e-pass-123

    dn: uid=bob,ou=people,dc=example,dc=org
    objectClass: inetOrgPerson
    objectClass: PostfixBookMailAccount
    uid: bob
    cn: Bob
    sn: Example
    mail: bob@e2e.example.org
    mailEnabled: TRUE
    userPassword: bob-pass-456

    dn: uid=carol,ou=people,dc=example,dc=org
    objectClass: inetOrgPerson
    objectClass: PostfixBookMailAccount
    uid: carol
    cn: Carol
    sn: Example
    mail: carol@e2e.example.org
    mailAlias: carol-alias@e2e.example.org
    mailEnabled: FALSE
    userPassword: carol-pass-789
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: sql-init
  namespace: cnms-e2e
data:
  10-schema.sql: |
    CREATE TABLE cnms_users (login text PRIMARY KEY, mail text NOT NULL, password text NOT NULL);
    CREATE TABLE cnms_aliases (alias text PRIMARY KEY, target text NOT NULL);
    INSERT INTO cnms_users VALUES
      ('alice', 'alice@e2e.example.org', '{PLAIN}e2e-pass-123'),
      ('bob', 'bob@e2e.example.org', '{PLAIN}bob-pass-456'),
      ('sa.e2e', 'guarded@e2e.example.org', '{PLAIN}x');
    INSERT INTO cnms_aliases VALUES
      ('sales@e2e.example.org', 'alice@e2e.example.org'),
      ('stale@e2e.example.org', 'gone@e2e.example.org'),
      ('guarded-alias@e2e.example.org', 'guarded@e2e.example.org');
    CREATE ROLE maildir LOGIN PASSWORD 'e2e-sql-pass';
    GRANT SELECT ON cnms_users, cnms_aliases TO maildir;
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: ldap
  namespace: cnms-e2e
spec:
  replicas: 1
  strategy:
    type: Recreate
  selector:
    matchLabels:
      app: ldap
  template:
    metadata:
      labels:
        app: ldap
    spec:
      securityContext:
        runAsUser: 911
        runAsGroup: 911
        fsGroup: 911
      containers:
        - name: ldap
          image: %[1]s
          env:
            - name: OPENLDAP_BOOTSTRAP_SCHEMAS
              value: core.ldif cosine.ldif inetorgperson.ldif rfc2307bis.ldif postfix-book.ldif
            - name: OPENLDAP_BOOTSTRAP_TLS
              value: "true"
            - name: OPENLDAP_BOOTSTRAP_DATA_READONLY
              value: "true"
            - name: OPENLDAP_BOOTSTRAP_DATA_READONLY_PASSWORD_HASHED
              value: e2e-ldap-pass
          ports:
            - containerPort: 3890
          readinessProbe:
            tcpSocket:
              port: 3890
          volumeMounts:
            - name: ldif
              mountPath: /container/services/openldap-bootstrap/assets/ldif/data/custom
            - name: tls
              mountPath: /container/services/openldap/assets/certs
      volumes:
        - name: ldif
          configMap:
            name: ldap-ldif
        - name: tls
          secret:
            secretName: ldap-tls
---
apiVersion: v1
kind: Service
metadata:
  name: ldap
  namespace: cnms-e2e
spec:
  selector:
    app: ldap
  ports:
    - name: ldap
      port: 389
      targetPort: 3890
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: sql
  namespace: cnms-e2e
spec:
  replicas: 1
  strategy:
    type: Recreate
  selector:
    matchLabels:
      app: sql
  template:
    metadata:
      labels:
        app: sql
    spec:
      initContainers:
        - name: tls
          image: %[2]s
          command:
            - sh
            - -c
            - cp /in/tls.crt /in/tls.key /tls/ && chown 70:70 /tls/tls.crt /tls/tls.key && chmod 0600 /tls/tls.key
          volumeMounts:
            - name: tls-in
              mountPath: /in
            - name: tls
              mountPath: /tls
      containers:
        - name: sql
          image: %[2]s
          args:
            - postgres
            - -c
            - ssl=on
            - -c
            - ssl_cert_file=/tls/tls.crt
            - -c
            - ssl_key_file=/tls/tls.key
          env:
            - name: POSTGRES_USER
              value: admin
            - name: POSTGRES_PASSWORD
              value: e2e-sql-admin
            - name: POSTGRES_DB
              value: maildir
          ports:
            - containerPort: 5432
          readinessProbe:
            tcpSocket:
              port: 5432
          volumeMounts:
            - name: initdb
              mountPath: /docker-entrypoint-initdb.d
            - name: tls
              mountPath: /tls
            - name: data
              mountPath: /var/lib/postgresql
      volumes:
        - name: initdb
          configMap:
            name: sql-init
        - name: tls-in
          secret:
            secretName: sql-tls
        - name: tls
          emptyDir: {}
        - name: data
          emptyDir: {}
---
apiVersion: v1
kind: Service
metadata:
  name: sql
  namespace: cnms-e2e
spec:
  selector:
    app: sql
  ports:
    - name: sql
      port: 5432
---
apiVersion: cnms.rpp.gmbh/v1alpha1
kind: MailDirectory
metadata:
  name: sql-e2e
  namespace: cnms-e2e
spec:
  type: sql
  sql:
    host: sql.cnms-e2e.svc
    database: maildir
    username: maildir
    passwordSecretRef:
      name: sql-creds
    tls:
      caSecretRef:
        name: sql-tls
    aliases:
      name: cnms_aliases
---
apiVersion: cnms.rpp.gmbh/v1alpha1
kind: MailDirectory
metadata:
  name: ldap-e2e
  namespace: cnms-e2e
spec:
  type: ldap
  ldap:
    host: ldap.cnms-e2e.svc
    baseDN: ou=people,dc=example,dc=org
    bindDN: cn=readonly,dc=example,dc=org
    bindSecretRef:
      name: ldap-creds
    tls:
      caSecretRef:
        name: ldap-tls
    attributes:
      aliases: mailAlias
    activeFilter: "(&(objectClass=PostfixBookMailAccount)(mailEnabled=TRUE))"
`

// e2eDirectoryFixturesYAML renders the directory fixtures with the pinned
// images.
func e2eDirectoryFixturesYAML() string {
	return fmt.Sprintf(e2eDirectoryFixturesTemplate, images.OpenLDAP, images.PostgreSQL)
}
