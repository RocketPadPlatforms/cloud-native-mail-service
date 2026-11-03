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

package controller

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	cnmsv1alpha1 "github.com/RocketPadPlatforms/cloud-native-mail-service/api/v1alpha1"
	"github.com/RocketPadPlatforms/cloud-native-mail-service/internal/dkim"
	"github.com/RocketPadPlatforms/cloud-native-mail-service/internal/dnsrecords"
)

// lastCheckedRefresh is how old the lastChecked of a record may get
// before a verification rewrites it (ADR 0012): a verified domain must
// not write its status on every check.
const lastCheckedRefresh = time.Hour

// maxReportedRecords limits how many unverified records a condition
// message names.
const maxReportedRecords = 3

// recResult is what the last lookups found for one record.
type recResult struct {
	// verified is the last definitive answer; nil before the first one.
	verified *bool
	// unknown is true when the last lookup failed temporarily; verified
	// then still holds the answer before it (ADR 0012).
	unknown bool
	reason  string
	// checkedAt is the time of the last definitive answer; zero after a
	// restart, which only means the answer is looked up again.
	checkedAt time.Time
}

// dnsState is the verification state of one MailDomain. It lives in
// memory only: the status carries what a user needs, and after a
// restart the records are simply looked up again, which is the safe
// direction (the hold clock of a pending key is in the annotation).
type dnsState struct {
	results map[string]recResult
	// nextDue is when the next lookup round is due; zero means now.
	nextDue time.Time
	// attempt counts the rounds in a row that left a record unverified,
	// for the backoff; failures counts the rounds in a row with a
	// temporary lookup error (dnsrecords.FailuresBeforeUnknown).
	attempt, failures int
}

// dnsCache holds the dnsState of every MailDomain.
type dnsCache struct {
	mu sync.Mutex
	m  map[types.NamespacedName]*dnsState
}

func (c *dnsCache) get(key types.NamespacedName) *dnsState {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m = map[types.NamespacedName]*dnsState{}
	}
	st, ok := c.m[key]
	if !ok {
		st = &dnsState{}
		c.m[key] = st
	}
	return st
}

func (c *dnsCache) forget(key types.NamespacedName) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.m, key)
}

// recordKey identifies a record by name and value, so a changed value
// is looked up as a new record.
func recordKey(rec dnsrecords.Record) string { return rec.Name + "\x00" + rec.Value() }

// missing reports whether some record has no result yet.
func (st *dnsState) missing(recs []dnsrecords.Record) bool {
	for _, rec := range recs {
		if _, ok := st.results[recordKey(rec)]; !ok {
			return true
		}
	}
	return false
}

// due reports whether a lookup round has to run now: the schedule says
// so, or a record has no result yet (a new key, a changed spec).
func (st *dnsState) due(now time.Time, recs []dnsrecords.Record) bool {
	return !now.Before(st.nextDue) || st.missing(recs)
}

// lookup runs one round over recs, in parallel (a slow resolver must not
// hold the reconcile for the sum of the timeouts). A temporary error
// leaves the previous answer of a record alone; results of records that
// are gone are dropped. prev seeds a record without in-memory result
// from the status (after a restart).
func (st *dnsState) lookup(ctx context.Context, resolver dnsrecords.Resolver, recs []dnsrecords.Record, prev []cnmsv1alpha1.DNSRecord, now time.Time) {
	results := make([]dnsrecords.Result, len(recs))
	var wg sync.WaitGroup
	for i := range recs {
		wg.Go(func() {
			if resolver == nil {
				results[i] = dnsrecords.Result{Unknown: true, Reason: "no DNS resolver is configured"}
				return
			}
			results[i] = dnsrecords.Verify(ctx, resolver, recs[i])
		})
	}
	wg.Wait()
	next := make(map[string]recResult, len(recs))
	anyUnknown := false
	for i, rec := range recs {
		key := recordKey(rec)
		res := results[i]
		if !res.Unknown {
			next[key] = recResult{verified: &res.Verified, reason: res.Reason, checkedAt: now}
			continue
		}
		anyUnknown = true
		old, ok := st.results[key]
		if !ok {
			for _, p := range prev {
				if p.Name == rec.Name && p.Value == rec.Value() {
					old.verified = p.Verified
				}
			}
		}
		old.unknown, old.reason = true, res.Reason
		next[key] = old
	}
	st.results = next
	if anyUnknown {
		st.failures++
	} else {
		st.failures = 0
	}
}

// schedule sets the time of the next round after one ran: hourly when
// everything is verified and no key waits, else backing off from one to
// ten minutes (ADR 0012).
func (st *dnsState) schedule(now time.Time, allVerified bool) {
	st.nextDue = now.Add(dnsrecords.NextCheck(allVerified, st.attempt))
	if allVerified {
		st.attempt = 0
	} else {
		st.attempt++
	}
}

// dkimStates maps the selectors of generated keys to what the lookups
// say about their records, for the rotation: a temporary error is
// unknown and leaves the hold of a pending key alone.
func (st *dnsState) dkimStates(domain string, keys []generatedKey) map[string]dkim.DNSState {
	out := make(map[string]dkim.DNSState, len(keys))
	for _, g := range keys {
		if g.key == nil {
			continue
		}
		rec := dnsrecords.Record{Name: g.info.Selector + "._domainkey." + domain, Values: []string{g.key.TXTValue()}}
		res, ok := st.results[recordKey(rec)]
		switch {
		case !ok || res.unknown || res.verified == nil:
			out[g.info.Selector] = dkim.DNSUnknown
		case *res.verified:
			out[g.info.Selector] = dkim.DNSVerified
		default:
			out[g.info.Selector] = dkim.DNSUnverified
		}
	}
	return out
}

// composeInput builds the input of the record composition from the spec
// and the published keys (ADR 0012). The MTA-STS hosts come from the
// MailServers of the namespace (mtaSTSHosts), unless the MTASTSHosts
// override is set.
func (r *MailDomainReconciler) composeInput(ctx context.Context, md *cnmsv1alpha1.MailDomain, keys []keyEntry) (dnsrecords.Input, error) {
	spec := &md.Spec
	in := dnsrecords.Input{Domain: spec.Domain, SPF: &dnsrecords.SPF{}}
	for _, k := range keys {
		in.DKIM = append(in.DKIM, dnsrecords.DKIMKey{Selector: k.selector, Key: k.key})
	}
	if s := spec.SPF; s != nil {
		in.SPF.IP4, in.SPF.IP6, in.SPF.Include = s.IP4, s.IP6, s.Include
		if s.Qualifier != nil {
			in.SPF.Qualifier = *s.Qualifier
		}
	}
	if d := spec.DMARC; d != nil {
		in.DMARC = &dnsrecords.DMARC{Testing: d.Testing != nil && *d.Testing}
		in.DMARC.Policy = deref(d.Policy)
		in.DMARC.SubdomainPolicy = deref(d.SubdomainPolicy)
		in.DMARC.NonExistentSubdomainPolicy = deref(d.NonExistentSubdomainPolicy)
		in.DMARC.Rua, in.DMARC.Ruf = deref(d.Rua), deref(d.Ruf)
	}
	if m := spec.MTAStS; m != nil && m.Enabled != nil && *m.Enabled {
		in.MTASTS = &dnsrecords.MTASTS{Mode: deref(m.Mode)}
		hosts := r.mtaSTSHosts
		if r.MTASTSHosts != nil {
			hosts = r.MTASTSHosts
		}
		var err error
		if in.MTASTS.MX, err = hosts(ctx, md); err != nil {
			return in, err
		}
	}
	if t := spec.TLSRPT; t != nil && t.Enabled != nil && *t.Enabled {
		in.TLSRPT = &dnsrecords.TLSRPT{Rua: deref(t.Rua)}
	}
	return in, nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// reportRecords writes status.records and the conditions that describe
// the records: RecordsVerified, SPFReady, MTASTSReady and
// ReportAuthorization.
func (r *MailDomainReconciler) reportRecords(md *cnmsv1alpha1.MailDomain, composed dnsrecords.Composed, st *dnsState) {
	prev := md.Status.Records
	records := make([]cnmsv1alpha1.DNSRecord, 0, len(composed.Records))
	var unverified []string
	unknown := 0
	for _, rec := range composed.Records {
		out := cnmsv1alpha1.DNSRecord{Name: rec.Name, Type: rec.Type, Value: rec.Value()}
		res := st.results[recordKey(rec)]
		out.Verified = res.verified
		out.LastChecked = lastChecked(prev, out, res)
		records = append(records, out)
		switch {
		case res.verified == nil:
			unknown++
		case !*res.verified:
			reason := res.reason
			if reason == "" {
				reason = "not verified"
			}
			unverified = append(unverified, rec.Name+": "+reason)
		}
	}
	md.Status.Records = records
	r.setRecordsVerified(md, len(records), unverified, unknown, st)

	if composed.NoSPFSources {
		r.setCondition(md, ConditionSPFReady, metav1.ConditionFalse, ReasonNoSendingSources,
			"no sending source is known, so no SPF record is generated; add one in spec.spf")
	} else {
		r.setCondition(md, ConditionSPFReady, metav1.ConditionTrue, ReasonSPFGenerated, "the SPF record is generated from the sending sources")
	}
	enabled := md.Spec.MTAStS != nil && md.Spec.MTAStS.Enabled != nil && *md.Spec.MTAStS.Enabled
	switch {
	case !enabled:
		md.Status.MTAStS = nil
		meta.RemoveStatusCondition(&md.Status.Conditions, ConditionMTASTSReady)
	case composed.MTASTS == nil:
		md.Status.MTAStS = nil
		r.setCondition(md, ConditionMTASTSReady, metav1.ConditionFalse, ReasonNoMailServer,
			"no MailServer of this namespace serves the domain, so there is no mx host to put in the policy")
	default:
		md.Status.MTAStS = &cnmsv1alpha1.MTAStSStatus{PolicyID: composed.MTASTS.ID, Policy: composed.MTASTS.Policy}
		r.setCondition(md, ConditionMTASTSReady, metav1.ConditionTrue, ReasonPolicyAvailable,
			"the policy and its _mta-sts record are in the status; serve the policy at https://mta-sts."+md.Spec.Domain+"/.well-known/mta-sts.txt")
	}
	if len(composed.Warnings) == 0 {
		if md.Spec.DMARC != nil {
			r.setCondition(md, ConditionReportAuthorization, metav1.ConditionTrue, ReasonNotRequired,
				"every report address is in the organizational domain of the policy")
		} else {
			meta.RemoveStatusCondition(&md.Status.Conditions, ConditionReportAuthorization)
		}
		return
	}
	msgs := make([]string, len(composed.Warnings))
	for i, w := range composed.Warnings {
		msgs[i] = fmt.Sprintf("%s %s: the receiver has to publish a TXT record %s with v=DMARC1", w.Tag, w.Address, w.Record)
	}
	r.setCondition(md, ConditionReportAuthorization, metav1.ConditionFalse, ReasonReceiverRecordRequired, strings.Join(msgs, "; "))
}

// lastChecked keeps the lastChecked of the previous status unless a
// lookup gave a definitive answer and the verification changed, there
// was no time yet, or the time is older than lastCheckedRefresh.
func lastChecked(prev []cnmsv1alpha1.DNSRecord, rec cnmsv1alpha1.DNSRecord, res recResult) *metav1.Time {
	var old *cnmsv1alpha1.DNSRecord
	for i := range prev {
		if prev[i].Name == rec.Name && prev[i].Value == rec.Value {
			old = &prev[i]
		}
	}
	var oldTime *metav1.Time
	if old != nil {
		oldTime = old.LastChecked
	}
	if res.checkedAt.IsZero() {
		return oldTime
	}
	fresh := new(metav1.NewTime(res.checkedAt))
	flipped := old == nil || old.Verified == nil || rec.Verified == nil || *old.Verified != *rec.Verified
	if oldTime == nil || flipped || res.checkedAt.Sub(oldTime.Time) >= lastCheckedRefresh {
		return fresh
	}
	return oldTime
}

// setRecordsVerified sets RecordsVerified: True when every record is
// verified, False when a lookup found one missing or different, and
// Unknown while lookups have not answered yet or keep failing, never
// False (ADR 0012).
func (r *MailDomainReconciler) setRecordsVerified(md *cnmsv1alpha1.MailDomain, total int, unverified []string, unknown int, st *dnsState) {
	switch {
	case total == 0:
		r.setCondition(md, ConditionRecordsVerified, metav1.ConditionTrue, ReasonNoRecords, "no DNS record is required")
	case st.failures >= dnsrecords.FailuresBeforeUnknown:
		r.setCondition(md, ConditionRecordsVerified, metav1.ConditionUnknown, ReasonLookupFailing,
			"DNS lookups keep failing, so the records cannot be checked; check --dns-resolver and the resolver")
	case len(unverified) > 0:
		msg := fmt.Sprintf("%d of %d records are not verified: %s", len(unverified), total, strings.Join(firstN(unverified, maxReportedRecords), "; "))
		if len(unverified) > maxReportedRecords {
			msg += fmt.Sprintf("; and %d more", len(unverified)-maxReportedRecords)
		}
		r.setCondition(md, ConditionRecordsVerified, metav1.ConditionFalse, ReasonRecordsNotVerified, msg)
	case unknown > 0:
		r.setCondition(md, ConditionRecordsVerified, metav1.ConditionUnknown, ReasonChecking, "waiting for the answer of the DNS lookups")
	default:
		r.setCondition(md, ConditionRecordsVerified, metav1.ConditionTrue, ReasonAllVerified, "every required record is verified")
	}
}

func firstN(s []string, n int) []string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
